package service

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/w4jnl/vink/internal/db"
	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/secrets"
)

func statusPageFromRow(r db.StatusPage) *domain.StatusPage {
	p := &domain.StatusPage{
		ID: r.ID, OrgID: r.OrgID, Slug: r.Slug, Title: r.Title, MatchTags: domain.ParseTags(r.MatchTags),
		Projects: domain.ParseTags(r.Projects), GroupBy: r.GroupBy, Incidents: r.Incidents, Public: r.Public,
		CreatedAt: domain.FromMillis(r.CreatedAt), UpdatedAt: domain.FromMillis(r.UpdatedAt),
	}
	if r.ProjectID != nil {
		p.ProjectID = *r.ProjectID
	}
	if p.MatchTags == nil {
		p.MatchTags = []string{}
	}
	if p.Projects == nil {
		p.Projects = []string{}
	}
	if r.PasswordHash != nil {
		p.PasswordHash = *r.PasswordHash
	}
	if r.CustomDomain != nil {
		p.CustomDomain = *r.CustomDomain
	}
	return p
}

// idsJSON encodes project ids as they are; tagsJSON would lowercase them.
func idsJSON(ids []string) string {
	if len(ids) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(ids)
	return string(b)
}

// preparePage normalises the page and turns a plaintext password into a
// hash; an empty password keeps the current hash on a private page. The
// page's owner (OrgID, ProjectID) must be set first: validation depends on
// whether it is an org's page.
func preparePage(p *domain.StatusPage, password, currentHash string) error {
	p.Normalize()
	switch {
	case p.Public:
		p.PasswordHash = ""
	case password != "":
		hash, err := secrets.HashPassword(password)
		if err != nil {
			return err
		}
		p.PasswordHash = hash
	default:
		p.PasswordHash = currentHash
	}
	return p.Validate()
}

// CreateStatusPage adds a page; the slug is unique across the instance.
func (s *Service) CreateStatusPage(ctx context.Context, sc domain.Scope, p *domain.StatusPage, password string) (*domain.StatusPage, error) {
	if err := requireEdit(sc); err != nil {
		return nil, err
	}
	p.OrgID, p.ProjectID = sc.OrgID, sc.ProjectID
	if err := preparePage(p, password, ""); err != nil {
		return nil, err
	}
	now := s.now()
	var out *domain.StatusPage
	err := s.db.Tx(ctx, func(q *db.Queries) error {
		row, err := q.CreateStatusPage(ctx, db.CreateStatusPageParams{
			ID: domain.NewID(), OrgID: sc.OrgID, ProjectID: &sc.ProjectID, Slug: p.Slug, Title: p.Title, MatchTags: tagsJSON(p.MatchTags),
			Projects: idsJSON(p.Projects), GroupBy: p.GroupBy, Incidents: p.Incidents, Public: p.Public,
			PasswordHash: ptrs(p.PasswordHash), CustomDomain: ptrs(p.CustomDomain), CreatedAt: domain.Millis(now), UpdatedAt: domain.Millis(now),
		})
		if err != nil {
			return conflictIfUnique(err, "the address "+p.Slug+" is taken")
		}
		out = statusPageFromRow(row)
		e := projectEntry(sc, "page.create", out.Slug, out.ID)
		e.After = pageSnapshot(out, nil)
		return s.record(ctx, q, sc, e)
	})
	if err != nil {
		return nil, err
	}
	s.log.Info("status page created", "project_id", sc.ProjectID, "page", p.Slug, "actor", sc.Actor)
	return out, nil
}

// StatusPage returns one page of the project.
func (s *Service) StatusPage(ctx context.Context, sc domain.Scope, slug string) (*domain.StatusPage, error) {
	if err := requireProject(sc); err != nil {
		return nil, err
	}
	row, err := s.db.Read().GetStatusPage(ctx, db.GetStatusPageParams{ProjectID: &sc.ProjectID, Slug: slug})
	if err != nil {
		return nil, notFoundIfNoRows(err, "status page")
	}
	return statusPageFromRow(row), nil
}

// ListStatusPages lists the project's pages by title.
func (s *Service) ListStatusPages(ctx context.Context, sc domain.Scope) ([]*domain.StatusPage, error) {
	if err := requireProject(sc); err != nil {
		return nil, err
	}
	rows, err := s.db.Read().ListStatusPages(ctx, &sc.ProjectID)
	if err != nil {
		return nil, err
	}
	out := make([]*domain.StatusPage, 0, len(rows))
	for _, r := range rows {
		out = append(out, statusPageFromRow(r))
	}
	return out, nil
}

// UpdateStatusPage replaces a page. The slug may change; an empty
// password keeps the current one.
func (s *Service) UpdateStatusPage(ctx context.Context, sc domain.Scope, slug string, p *domain.StatusPage, password string) (*domain.StatusPage, error) {
	if err := requireEdit(sc); err != nil {
		return nil, err
	}
	cur, err := s.StatusPage(ctx, sc, slug)
	if err != nil {
		return nil, err
	}
	next := *p
	next.OrgID, next.ProjectID = cur.OrgID, cur.ProjectID
	if next.Slug == "" {
		next.Slug = cur.Slug
	}
	if err := preparePage(&next, password, cur.PasswordHash); err != nil {
		return nil, err
	}
	var out *domain.StatusPage
	err = s.db.Tx(ctx, func(q *db.Queries) error {
		row, err := q.UpdateStatusPage(ctx, db.UpdateStatusPageParams{
			Slug: next.Slug, Title: next.Title, MatchTags: tagsJSON(next.MatchTags), Incidents: next.Incidents, Public: next.Public,
			PasswordHash: ptrs(next.PasswordHash), CustomDomain: ptrs(next.CustomDomain),
			UpdatedAt: domain.Millis(s.now()), ProjectID: &sc.ProjectID, ID: cur.ID,
		})
		if err != nil {
			return conflictIfUnique(err, "the address "+next.Slug+" is taken")
		}
		out = statusPageFromRow(row)
		e := projectEntry(sc, "page.update", out.Slug, out.ID)
		e.Before, e.After = pageSnapshot(cur, nil), pageSnapshot(out, nil)
		e.Detail = map[string]any{"fields": changedFields(e.Before, e.After)}
		return s.record(ctx, q, sc, e)
	})
	if err != nil {
		return nil, err
	}
	s.log.Info("status page updated", "project_id", sc.ProjectID, "page", next.Slug, "actor", sc.Actor)
	return out, nil
}

// DeleteStatusPage removes a page.
func (s *Service) DeleteStatusPage(ctx context.Context, sc domain.Scope, slug string) error {
	if err := requireEdit(sc); err != nil {
		return err
	}
	cur, err := s.StatusPage(ctx, sc, slug)
	if err != nil {
		return err
	}
	err = s.db.Tx(ctx, func(q *db.Queries) error {
		n, err := q.DeleteStatusPage(ctx, db.DeleteStatusPageParams{ProjectID: &sc.ProjectID, Slug: slug})
		if err != nil {
			return err
		}
		if n == 0 {
			return domain.NotFound("status page")
		}
		e := projectEntry(sc, "page.delete", cur.Slug, cur.ID)
		e.Before = pageSnapshot(cur, nil)
		return s.record(ctx, q, sc, e)
	})
	if err != nil {
		return err
	}
	s.log.Info("status page deleted", "project_id", sc.ProjectID, "page", slug, "actor", sc.Actor)
	return nil
}

// StatusPageBySlug finds a page for the public route.
func (s *Service) StatusPageBySlug(ctx context.Context, slug string) (*domain.StatusPage, error) {
	row, err := s.db.Read().GetStatusPageBySlug(ctx, slug)
	if err != nil {
		return nil, notFoundIfNoRows(err, "status page")
	}
	return statusPageFromRow(row), nil
}

// StatusPageByDomain finds the page served on a custom host name.
func (s *Service) StatusPageByDomain(ctx context.Context, host string) (*domain.StatusPage, error) {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return nil, domain.NotFound("status page")
	}
	row, err := s.db.Read().GetStatusPageByDomain(ctx, &host)
	if err != nil {
		return nil, notFoundIfNoRows(err, "status page")
	}
	return statusPageFromRow(row), nil
}

// CheckStatusPassword verifies a visitor's password.
func (s *Service) CheckStatusPassword(p *domain.StatusPage, password string) bool {
	return p.HasPassword() && secrets.VerifyPassword(p.PasswordHash, password)
}

// StatusToken signs a 24-hour cookie for a private page.
func (s *Service) StatusToken(p *domain.StatusPage) string {
	return s.keyring.Sign("status:"+p.Slug, p.ID, s.now().Add(24*time.Hour))
}

// VerifyStatusToken checks a visitor's cookie.
func (s *Service) VerifyStatusToken(p *domain.StatusPage, token string) bool {
	id, err := s.keyring.Verify("status:"+p.Slug, token, s.now())
	return err == nil && id == p.ID
}

// PublicStatus is what a status page shows.
type PublicStatus struct {
	Page *domain.StatusPage
	// Projects are the projects on the page, in page order: a project
	// page's own, or an org page's chosen ones (all of them by name).
	Projects []*domain.Project
	// Location is the page's clock: the project's timezone, or on an org
	// page the one its projects share, else UTC.
	Location *time.Location
	Monitors []*domain.Monitor
	Groups   []StatusGroup
	// Incidents are the open ones, newest first. The banner reads them even
	// when the page does not list them.
	Incidents []*domain.Incident
	// PastIncidents were resolved within the page's window, newest first,
	// at most MaxPastIncidents; PastMore counts the ones left out.
	PastIncidents []*domain.Incident
	PastMore      int
	Maintenance   bool
	Down          int
	Late          int
	Events        map[string][]*domain.Event
	GeneratedAt   time.Time
	projectByID   map[string]*domain.Project
}

// MaxPastIncidents caps the past incidents a page lists.
const MaxPastIncidents = 50

// StatusGroup is one tag's or one project's monitors, sorted by name.
type StatusGroup struct {
	Name     string
	Monitors []*domain.Monitor
}

// ProjectOf returns the project a monitor or incident belongs to.
func (st *PublicStatus) ProjectOf(projectID string) *domain.Project {
	return st.projectByID[projectID]
}

// pageProjects lists the projects a page shows, in page order.
func (s *Service) pageProjects(ctx context.Context, p *domain.StatusPage) ([]*domain.Project, error) {
	if !p.IsOrg() {
		project, err := s.ProjectByID(ctx, p.ProjectID)
		if err != nil {
			return nil, err
		}
		return []*domain.Project{project}, nil
	}
	rows, err := s.db.Read().ListProjects(ctx, p.OrgID)
	if err != nil {
		return nil, err
	}
	all := make([]*domain.Project, 0, len(rows))
	byID := make(map[string]*domain.Project, len(rows))
	for _, r := range rows {
		pr := projectFromRow(r)
		all = append(all, pr)
		byID[pr.ID] = pr
	}
	if len(p.Projects) == 0 {
		sort.SliceStable(all, func(i, j int) bool { return strings.ToLower(all[i].Name) < strings.ToLower(all[j].Name) })
		return all, nil
	}
	out := make([]*domain.Project, 0, len(p.Projects))
	for _, id := range p.Projects {
		if pr, ok := byID[id]; ok { // a deleted project drops off the page
			out = append(out, pr)
		}
	}
	return out, nil
}

// pageLocation is the timezone a page's projects share, else UTC.
func pageLocation(projects []*domain.Project) *time.Location {
	tz := ""
	for i, pr := range projects {
		switch {
		case i == 0:
			tz = pr.Timezone
		case pr.Timezone != tz:
			return time.UTC
		}
	}
	loc, err := time.LoadLocation(tz)
	if err != nil || tz == "" {
		return time.UTC
	}
	return loc
}

// PublicStatus gathers the page's monitors from its projects, their last 90
// days of events, the open incidents, the incidents resolved within the
// page's window, and whether maintenance covers any of the monitors.
func (s *Service) PublicStatus(ctx context.Context, p *domain.StatusPage, now time.Time) (*PublicStatus, error) {
	projects, err := s.pageProjects(ctx, p)
	if err != nil {
		return nil, err
	}
	out := &PublicStatus{Page: p, Projects: projects, Location: pageLocation(projects), Events: map[string][]*domain.Event{}, GeneratedAt: now,
		projectByID: make(map[string]*domain.Project, len(projects))}
	_, days := p.IncidentWindow()
	shown := map[string]bool{}
	byProject := map[string][]*domain.Monitor{}
	for _, pr := range projects {
		out.projectByID[pr.ID] = pr
		if err := s.gatherProject(ctx, p, pr, now, days, out, shown, byProject); err != nil {
			return nil, err
		}
	}
	if p.IsOrg() && p.GroupBy == domain.GroupByProject {
		for _, pr := range projects {
			if ms := byProject[pr.ID]; len(ms) > 0 {
				sortByName(ms)
				out.Groups = append(out.Groups, StatusGroup{Name: pr.Name, Monitors: ms})
			}
		}
	} else {
		out.Groups = groupByTag(p, out.Monitors)
	}
	sort.SliceStable(out.Incidents, func(i, j int) bool { return out.Incidents[i].OpenedAt.After(out.Incidents[j].OpenedAt) })
	sort.SliceStable(out.PastIncidents, func(i, j int) bool { return out.PastIncidents[i].OpenedAt.After(out.PastIncidents[j].OpenedAt) })
	if n := len(out.PastIncidents); n > MaxPastIncidents {
		out.PastIncidents, out.PastMore = out.PastIncidents[:MaxPastIncidents], n-MaxPastIncidents
	}
	return out, nil
}

// gatherProject adds one project's shown monitors, events, incidents and
// maintenance to out.
func (s *Service) gatherProject(ctx context.Context, p *domain.StatusPage, pr *domain.Project, now time.Time, days int,
	out *PublicStatus, shown map[string]bool, byProject map[string][]*domain.Monitor) error {
	q := s.db.Read()
	rows, err := q.ListMonitors(ctx, pr.ID)
	if err != nil {
		return err
	}
	var mine []*domain.Monitor
	for _, r := range rows {
		m, err := monitorFromRow(r)
		if err != nil {
			return err
		}
		if !p.Shows(m) {
			continue
		}
		mine = append(mine, m)
		shown[m.ID] = true
		switch m.State {
		case domain.StateDown:
			out.Down++
		case domain.StateLate:
			out.Late++
		}
	}
	out.Monitors = append(out.Monitors, mine...)
	byProject[pr.ID] = mine
	events, err := q.ListProjectEventsSince(ctx, db.ListProjectEventsSinceParams{ProjectID: pr.ID, At: domain.Millis(now.Add(-90 * 24 * time.Hour))})
	if err != nil {
		return err
	}
	for _, e := range events {
		if shown[e.MonitorID] {
			out.Events[e.MonitorID] = append(out.Events[e.MonitorID], eventFromRow(e))
		}
	}
	open, err := q.ListOpenIncidents(ctx, pr.ID)
	if err != nil {
		return err
	}
	for _, r := range open {
		if shown[r.MonitorID] {
			out.Incidents = append(out.Incidents, incidentFrom(incidentRow{r.ID, r.MonitorID, r.ProjectID, r.OpenedAt, r.ResolvedAt, r.AckedBy, r.AckedAt, r.OpenEventID, r.CloseEventID, r.MonitorSlug, r.MonitorName, r.MonitorTags, r.Reason}))
		}
	}
	if days > 0 {
		since := now.Add(-time.Duration(days) * 24 * time.Hour)
		recent, err := q.ListIncidents(ctx, db.ListIncidentsParams{ProjectID: pr.ID, Since: ptri(domain.Millis(since)), Limit: 1000})
		if err != nil {
			return err
		}
		for _, r := range recent {
			if r.ResolvedAt != nil && shown[r.MonitorID] {
				out.PastIncidents = append(out.PastIncidents, incidentFrom(incidentRow{r.ID, r.MonitorID, r.ProjectID, r.OpenedAt, r.ResolvedAt, r.AckedBy, r.AckedAt, r.OpenEventID, r.CloseEventID, r.MonitorSlug, r.MonitorName, r.MonitorTags, r.Reason}))
			}
		}
	}
	windows, err := s.listMaintenance(ctx, q, pr.ID)
	if err != nil {
		return err
	}
	for _, w := range windows {
		if _, active := w.ActiveAt(now); !active {
			continue
		}
		for _, m := range mine {
			if w.Covers(m) {
				out.Maintenance = true
			}
		}
	}
	return nil
}

// groupByTag puts each monitor in its first tag's group, groups in
// match_tags order, then the rest by name.
func groupByTag(p *domain.StatusPage, monitors []*domain.Monitor) []StatusGroup {
	groups := map[string]*StatusGroup{}
	var order []string
	for _, m := range monitors {
		name := p.Group(m)
		g, ok := groups[name]
		if !ok {
			g = &StatusGroup{Name: name}
			groups[name] = g
			order = append(order, name)
		}
		g.Monitors = append(g.Monitors, m)
	}
	rank := func(name string) int {
		for i, t := range p.MatchTags {
			if t == name {
				return i
			}
		}
		return len(p.MatchTags)
	}
	sort.SliceStable(order, func(i, j int) bool {
		if rank(order[i]) != rank(order[j]) {
			return rank(order[i]) < rank(order[j])
		}
		return order[i] < order[j]
	})
	out := make([]StatusGroup, 0, len(order))
	for _, name := range order {
		g := groups[name]
		sortByName(g.Monitors)
		out = append(out, *g)
	}
	return out
}

func sortByName(ms []*domain.Monitor) {
	sort.SliceStable(ms, func(i, j int) bool { return strings.ToLower(ms[i].Name) < strings.ToLower(ms[j].Name) })
}

// ErrStatusLocked says the visitor has not entered the page's password.
var ErrStatusLocked = errors.New("status page locked")
