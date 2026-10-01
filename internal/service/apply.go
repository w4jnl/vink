package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/w4jnl/vink/internal/apply"
	"github.com/w4jnl/vink/internal/db"
	"github.com/w4jnl/vink/internal/domain"
)

// ApplyOptions steer an apply.
type ApplyOptions struct {
	// DryRun computes the diff and rolls everything back.
	DryRun bool
	// Prune deletes monitors, channels and routes the file does not name.
	Prune bool
}

var errDryRun = errors.New("dry run")

// Apply brings the project to the file in one transaction and returns
// the diff. Monitor state is never touched; a monitor whose kind changes
// is recreated.
func (s *Service) Apply(ctx context.Context, sc domain.Scope, f *apply.File, o ApplyOptions) (*apply.Diff, error) {
	if err := requireEdit(sc); err != nil {
		return nil, err
	}
	diff := &apply.Diff{DryRun: o.DryRun, Created: []string{}, Updated: []string{}, Recreated: []string{}, Deleted: []string{}, Unchanged: []string{}}
	err := s.db.Tx(ctx, func(q *db.Queries) error {
		tx := s.inTx(q)
		tx.noAudit = true // the apply row below stands for every change inside it
		if err := tx.applyFile(ctx, sc, f, o, diff); err != nil {
			return err
		}
		if o.DryRun {
			return errDryRun
		}
		e := projectEntry(sc, "apply", "vink.yaml", "")
		e.Detail = applyDetail(diff, o.Prune)
		return s.record(ctx, q, sc, e)
	})
	if err != nil && !errors.Is(err, errDryRun) {
		return nil, err
	}
	if !o.DryRun {
		s.log.Info("apply", "project_id", sc.ProjectID, "created", len(diff.Created), "updated", len(diff.Updated), "recreated", len(diff.Recreated), "deleted", len(diff.Deleted), "actor", sc.Actor)
		s.bus.Publish(engineChanged(sc.ProjectID))
	}
	return diff, nil
}

func validation(field, msg string) error {
	return (&domain.ValidationError{Errors: []domain.FieldError{{Field: field, Msg: msg}}}).OrNil()
}

func (s *Service) applyFile(ctx context.Context, sc domain.Scope, f *apply.File, o ApplyOptions, diff *apply.Diff) error {
	project, err := s.Project(ctx, sc)
	if err != nil {
		return err
	}
	// project
	if p := f.Project; p != nil {
		if p.Slug != "" && p.Slug != project.Slug {
			return validation("project.slug", fmt.Sprintf("the file is for %s, this project is %s", p.Slug, project.Slug))
		}
		name, tz := project.Name, project.Timezone
		if p.Name != "" {
			name = p.Name
		}
		if p.Timezone != "" {
			tz = p.Timezone
		}
		if name != project.Name || tz != project.Timezone {
			if _, err := s.UpdateProject(ctx, sc, name, tz); err != nil {
				return err
			}
			diff.Updated = append(diff.Updated, "project "+project.Slug)
		} else {
			diff.Unchanged = append(diff.Unchanged, "project "+project.Slug)
		}
	}
	// channels
	channels, err := s.ListChannels(ctx, sc)
	if err != nil {
		return err
	}
	byName := map[string]*domain.Channel{}
	for _, c := range channels {
		byName[c.Name] = c
	}
	keep := map[string]bool{}
	for i, c := range f.Channels {
		if c.Name == "" {
			return validation(fmt.Sprintf("channels[%d].name", i), "must not be empty")
		}
		cfg, err := c.ConfigJSON()
		if err != nil {
			return err
		}
		enabled := c.Enabled == nil || *c.Enabled
		want := &domain.Channel{Name: c.Name, Kind: domain.ChannelKind(c.Kind), Config: cfg, Enabled: enabled}
		label := "channel " + c.Name
		keep[c.Name] = true
		if cur, ok := byName[c.Name]; ok {
			if cur.Kind == want.Kind && cur.Enabled == enabled && configMatches(cur.Config, cfg) {
				diff.Unchanged = append(diff.Unchanged, label)
				continue
			}
			if _, err := s.UpdateChannel(ctx, sc, cur.ID, want); err != nil {
				return prefixField(err, fmt.Sprintf("channels[%d].", i))
			}
			diff.Updated = append(diff.Updated, label)
			continue
		}
		created, err := s.CreateChannel(ctx, sc, want)
		if err != nil {
			return prefixField(err, fmt.Sprintf("channels[%d].", i))
		}
		byName[c.Name] = created
		diff.Created = append(diff.Created, label)
	}
	// routes
	routes, err := s.ListRoutes(ctx, sc)
	if err != nil {
		return err
	}
	byKey := map[string]*domain.Route{}
	for _, r := range routes {
		byKey[routeKey(r.MatchTags, r.ChannelNames())] = r
	}
	keepRoutes := map[string]bool{}
	for i, r := range f.Routes {
		ids := make([]string, 0, len(r.Channels))
		for _, name := range r.Channels {
			ch, ok := byName[name]
			if !ok {
				return validation(fmt.Sprintf("routes[%d].channels", i), "unknown channel "+name)
			}
			ids = append(ids, ch.ID)
		}
		on := r.On
		if len(on) == 0 {
			on = []domain.State{domain.StateDown, domain.StateUp}
		}
		want := &domain.Route{MatchTags: domain.NormalizeTags(r.MatchTags), ChannelIDs: ids, On: on, RepeatEvery: r.RepeatEvery.Std(), Priority: r.Priority}
		key := routeKey(want.MatchTags, r.Channels)
		label := "route " + routeLabel(want.MatchTags, r.Channels)
		keepRoutes[key] = true
		if cur, ok := byKey[key]; ok {
			if sameStates(cur.On, on) && cur.RepeatEvery == want.RepeatEvery && cur.Priority == want.Priority {
				diff.Unchanged = append(diff.Unchanged, label)
				continue
			}
			if _, err := s.UpdateRoute(ctx, sc, cur.ID, want); err != nil {
				return prefixField(err, fmt.Sprintf("routes[%d].", i))
			}
			diff.Updated = append(diff.Updated, label)
			continue
		}
		if _, err := s.CreateRoute(ctx, sc, want); err != nil {
			return prefixField(err, fmt.Sprintf("routes[%d].", i))
		}
		diff.Created = append(diff.Created, label)
	}
	// maintenance windows
	windows, err := s.ListMaintenance(ctx, sc)
	if err != nil {
		return err
	}
	winByName := map[string]*domain.Maintenance{}
	for _, w := range windows {
		winByName[w.Name] = w
	}
	for i, m := range f.Maintenance {
		want, err := m.ToDomain()
		if err != nil {
			return prefixField(err, fmt.Sprintf("maintenance[%d].", i))
		}
		label := "maintenance " + m.Name
		if cur, ok := winByName[m.Name]; ok {
			if want.Timezone == "" {
				want.Timezone = cur.Timezone
			}
			probe := *want
			probe.Normalize()
			if sameWindow(cur, &probe) {
				diff.Unchanged = append(diff.Unchanged, label)
				continue
			}
			if _, err := s.UpdateMaintenance(ctx, sc, cur.ID, want); err != nil {
				return prefixField(err, fmt.Sprintf("maintenance[%d].", i))
			}
			diff.Updated = append(diff.Updated, label)
			continue
		}
		if _, err := s.CreateMaintenance(ctx, sc, want); err != nil {
			return prefixField(err, fmt.Sprintf("maintenance[%d].", i))
		}
		diff.Created = append(diff.Created, label)
	}
	// status pages
	pages, err := s.ListStatusPages(ctx, sc)
	if err != nil {
		return err
	}
	pageBySlug := map[string]*domain.StatusPage{}
	for _, p := range pages {
		pageBySlug[p.Slug] = p
	}
	for i, p := range f.StatusPages {
		want, password := p.ToDomain()
		want.Normalize()
		label := "page " + want.Slug
		if cur, ok := pageBySlug[want.Slug]; ok {
			same := cur.Title == want.Title && sameTags(cur.MatchTags, want.MatchTags) && cur.CustomDomain == want.CustomDomain && cur.Public == want.Public
			if same && password == "" {
				diff.Unchanged = append(diff.Unchanged, label)
				continue
			}
			if _, err := s.UpdateStatusPage(ctx, sc, cur.Slug, want, password); err != nil {
				return prefixField(err, fmt.Sprintf("status_pages[%d].", i))
			}
			diff.Updated = append(diff.Updated, label)
			continue
		}
		if _, err := s.CreateStatusPage(ctx, sc, want, password); err != nil {
			return prefixField(err, fmt.Sprintf("status_pages[%d].", i))
		}
		diff.Created = append(diff.Created, label)
	}
	// monitors
	monitors, err := s.ListMonitors(ctx, sc, MonitorFilter{})
	if err != nil {
		return err
	}
	monBySlug := map[string]*domain.Monitor{}
	for _, m := range monitors {
		monBySlug[m.Slug] = m
	}
	keepMonitors := map[string]bool{}
	for i, m := range f.Monitors {
		want := m.ToDomain()
		want.Slug = strings.TrimSpace(want.Slug)
		if want.Name == "" {
			want.Name = want.Slug
		}
		label := "monitor " + want.Slug
		keepMonitors[want.Slug] = true
		cur, ok := monBySlug[want.Slug]
		switch {
		case ok && cur.Kind != want.Kind:
			if err := s.DeleteMonitor(ctx, sc, cur.Slug); err != nil {
				return err
			}
			if _, err := s.CreateMonitor(ctx, sc, want); err != nil {
				return prefixField(err, fmt.Sprintf("monitors[%d].", i))
			}
			diff.Recreated = append(diff.Recreated, label)
		case ok:
			probe := *want
			probe.Normalize()
			if sameMonitor(cur, &probe) {
				diff.Unchanged = append(diff.Unchanged, label)
				continue
			}
			if _, err := s.UpdateMonitor(ctx, sc, cur.Slug, want); err != nil {
				return prefixField(err, fmt.Sprintf("monitors[%d].", i))
			}
			diff.Updated = append(diff.Updated, label)
		default:
			if _, err := s.CreateMonitor(ctx, sc, want); err != nil {
				return prefixField(err, fmt.Sprintf("monitors[%d].", i))
			}
			diff.Created = append(diff.Created, label)
		}
	}
	if !o.Prune {
		return nil
	}
	for _, m := range monitors {
		if !keepMonitors[m.Slug] {
			if err := s.DeleteMonitor(ctx, sc, m.Slug); err != nil {
				return err
			}
			diff.Deleted = append(diff.Deleted, "monitor "+m.Slug)
		}
	}
	for _, r := range routes {
		if !keepRoutes[routeKey(r.MatchTags, r.ChannelNames())] {
			if err := s.DeleteRoute(ctx, sc, r.ID); err != nil && !errors.Is(err, domain.ErrNotFound) {
				return err
			}
			diff.Deleted = append(diff.Deleted, "route "+routeLabel(r.MatchTags, r.ChannelNames()))
		}
	}
	for _, c := range channels {
		if !keep[c.Name] {
			if err := s.DeleteChannel(ctx, sc, c.ID); err != nil && !errors.Is(err, domain.ErrNotFound) {
				return err
			}
			diff.Deleted = append(diff.Deleted, "channel "+c.Name)
		}
	}
	return nil
}

// prefixField names the list entry a validation error belongs to.
func prefixField(err error, prefix string) error {
	ve, ok := domain.AsValidation(err)
	if !ok {
		return err
	}
	out := &domain.ValidationError{}
	for _, fe := range ve.Errors {
		out.Errors = append(out.Errors, domain.FieldError{Field: prefix + fe.Field, Msg: fe.Msg})
	}
	return out.OrNil()
}

// configMatches compares a stored config with the file's: *** in the
// file keeps a secret, and every stored key must be named.
func configMatches(stored, file json.RawMessage) bool {
	var s, f map[string]any
	if json.Unmarshal(stored, &s) != nil || json.Unmarshal(file, &f) != nil {
		return false
	}
	for k, v := range f {
		if v == "***" {
			continue
		}
		if !reflect.DeepEqual(s[k], v) {
			return false
		}
	}
	for k := range s {
		if _, ok := f[k]; !ok {
			return false
		}
	}
	return true
}

func routeKey(tags, channels []string) string {
	t := append([]string(nil), domain.NormalizeTags(tags)...)
	c := append([]string(nil), channels...)
	sort.Strings(t)
	sort.Strings(c)
	return strings.Join(t, ",") + "|" + strings.Join(c, ",")
}

func routeLabel(tags, channels []string) string {
	t := "*"
	if len(tags) > 0 {
		t = strings.Join(tags, ",")
	}
	return t + " → " + strings.Join(channels, ", ")
}

func sameStates(a, b []domain.State) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[domain.State]bool{}
	for _, s := range a {
		seen[s] = true
	}
	for _, s := range b {
		if !seen[s] {
			return false
		}
	}
	return true
}

func sameWindow(a, b *domain.Maintenance) bool {
	if a.Weekly != b.Weekly || a.Timezone != b.Timezone || !sameTags(a.MatchTags, b.MatchTags) {
		return false
	}
	if a.Weekly {
		return reflect.DeepEqual(a.Days, b.Days) && a.From == b.From && a.To == b.To
	}
	return timesEqual(a.StartsAt, b.StartsAt) && timesEqual(a.EndsAt, b.EndsAt)
}

func sameTags(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sameMonitor(cur, want *domain.Monitor) bool {
	if cur.Name != want.Name || !sameTags(cur.Tags, want.Tags) {
		return false
	}
	a, err1 := cur.SpecJSON()
	b, err2 := want.SpecJSON()
	if err1 != nil || err2 != nil {
		return false
	}
	var ma, mb any
	_ = json.Unmarshal(a, &ma)
	_ = json.Unmarshal(b, &mb)
	return reflect.DeepEqual(ma, mb)
}

// Export writes the project in the apply file's form. Secrets are
// redacted unless asked for by someone who may edit.
func (s *Service) Export(ctx context.Context, sc domain.Scope, secrets bool) (*apply.File, error) {
	if err := requireProject(sc); err != nil {
		return nil, err
	}
	if secrets && !sc.CanEdit() {
		return nil, domain.ErrForbidden
	}
	project, err := s.Project(ctx, sc)
	if err != nil {
		return nil, err
	}
	f := &apply.File{Version: 1, Project: &apply.Project{Slug: project.Slug, Name: project.Name, Timezone: project.Timezone}}
	channels, err := s.ListChannels(ctx, sc)
	if err != nil {
		return nil, err
	}
	for _, c := range channels {
		cfg := c.Config
		if !secrets {
			cfg = RedactConfig(c.Kind, c.Config)
		}
		var m map[string]any
		_ = json.Unmarshal(cfg, &m)
		ch := apply.Channel{Name: c.Name, Kind: string(c.Kind), Config: m}
		if !c.Enabled {
			off := false
			ch.Enabled = &off
		}
		f.Channels = append(f.Channels, ch)
	}
	routes, err := s.ListRoutes(ctx, sc)
	if err != nil {
		return nil, err
	}
	for _, r := range routes {
		f.Routes = append(f.Routes, apply.Route{MatchTags: r.MatchTags, Channels: r.ChannelNames(), On: r.On, RepeatEvery: domain.Duration(r.RepeatEvery), Priority: r.Priority})
	}
	windows, err := s.ListMaintenance(ctx, sc)
	if err != nil {
		return nil, err
	}
	for _, w := range windows {
		f.Maintenance = append(f.Maintenance, apply.MaintenanceFrom(w))
	}
	monitors, err := s.ListMonitors(ctx, sc, MonitorFilter{})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(monitors, func(i, j int) bool { return monitors[i].Slug < monitors[j].Slug })
	for _, m := range monitors {
		f.Monitors = append(f.Monitors, apply.MonitorFrom(m))
	}
	pages, err := s.ListStatusPages(ctx, sc)
	if err != nil {
		return nil, err
	}
	for _, p := range pages {
		sp := apply.StatusPage{Slug: p.Slug, Title: p.Title, MatchTags: p.MatchTags, CustomDomain: p.CustomDomain}
		if !p.Public {
			private := false
			sp.Public = &private
		}
		f.StatusPages = append(f.StatusPages, sp)
	}
	return f, nil
}

func timesEqual(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

// requireOrgFile says who may export or apply a whole org: an org key, or
// a person who administers the org. A project key never acts for the org.
func requireOrgFile(sc domain.Scope) error {
	switch {
	case sc.OrgID == "":
		return domain.ErrForbidden
	case sc.IsKey():
		if !sc.IsOrgKey() {
			return domain.ErrForbidden
		}
		return nil
	case !sc.InstanceAdmin && !sc.CanAdminOrg():
		return domain.ErrForbidden
	}
	return nil
}

// ExportOrg writes every project of the org as one file; secrets need
// write access, as for a project export.
func (s *Service) ExportOrg(ctx context.Context, sc domain.Scope, secrets bool) (*apply.OrgFile, error) {
	if err := requireOrgFile(sc); err != nil {
		return nil, err
	}
	if secrets && !sc.CanEdit() {
		return nil, domain.ErrForbidden
	}
	org, err := s.OrgByID(ctx, sc.OrgID)
	if err != nil {
		return nil, err
	}
	projects, err := s.ListProjects(ctx, sc)
	if err != nil {
		return nil, err
	}
	out := &apply.OrgFile{Version: 1, Org: org.Slug, Projects: []apply.ProjectEntry{}}
	for _, p := range projects {
		psc := sc
		psc.ProjectID = p.ID
		f, err := s.Export(ctx, psc, secrets)
		if err != nil {
			return nil, err
		}
		out.Projects = append(out.Projects, apply.EntryFrom(f))
	}
	return out, nil
}

// ApplyOrg brings every project named in the file to it, in one
// transaction: a project that does not exist is created, projects the
// file does not name are left alone, and no project is ever deleted.
func (s *Service) ApplyOrg(ctx context.Context, sc domain.Scope, f *apply.OrgFile, o ApplyOptions) (*apply.OrgDiff, error) {
	if err := requireOrgFile(sc); err != nil {
		return nil, err
	}
	if !sc.CanEdit() {
		return nil, domain.ErrForbidden
	}
	org, err := s.OrgByID(ctx, sc.OrgID)
	if err != nil {
		return nil, err
	}
	if f.Org != "" && f.Org != org.Slug {
		return nil, validation("org", fmt.Sprintf("the file is for %s, this org is %s", f.Org, org.Slug))
	}
	seen := map[string]bool{}
	for i, e := range f.Projects {
		if !domain.ValidSlug(e.Slug) {
			return nil, validation(fmt.Sprintf("projects[%d].slug", i), "must be lowercase letters, digits and dashes")
		}
		if seen[e.Slug] {
			return nil, validation(fmt.Sprintf("projects[%d].slug", i), e.Slug+" is listed twice")
		}
		seen[e.Slug] = true
	}
	diff := &apply.OrgDiff{DryRun: o.DryRun, Org: org.Slug, Projects: []apply.ProjectDiff{}}
	var touched []string
	err = s.db.Tx(ctx, func(q *db.Queries) error {
		tx := s.inTx(q)
		tx.noAudit = true // one apply row per project stands for the changes inside it
		for i, e := range f.Projects {
			project, err := tx.ProjectBySlug(ctx, sc.OrgID, e.Slug)
			created := false
			if errors.Is(err, domain.ErrNotFound) {
				name, tz := e.Name, e.Timezone
				if tz == "" {
					tz = "UTC"
				}
				project, err = tx.CreateProject(ctx, sc, sc.OrgID, e.Slug, name, tz)
				created = true
			}
			if err != nil {
				return prefixField(err, fmt.Sprintf("projects[%d].", i))
			}
			psc := sc
			psc.ProjectID = project.ID
			pd := apply.ProjectDiff{Slug: e.Slug, Created: created, Diff: apply.Diff{DryRun: o.DryRun, Created: []string{}, Updated: []string{}, Recreated: []string{}, Deleted: []string{}, Unchanged: []string{}}}
			if err := tx.applyFile(ctx, psc, e.File(), o, &pd.Diff); err != nil {
				return prefixField(err, fmt.Sprintf("projects[%d].", i))
			}
			diff.Projects = append(diff.Projects, pd)
			touched = append(touched, project.ID)
			if !o.DryRun {
				entry := projectEntry(psc, "apply", "org file", "")
				entry.Detail = applyDetail(&pd.Diff, o.Prune)
				entry.Detail["project_created"] = created
				if err := s.record(ctx, q, psc, entry); err != nil {
					return err
				}
			}
		}
		if o.DryRun {
			return errDryRun
		}
		return nil
	})
	if err != nil && !errors.Is(err, errDryRun) {
		return nil, err
	}
	if !o.DryRun {
		s.log.Info("apply org", "org_id", sc.OrgID, "projects", len(diff.Projects), "changes", diff.Changes(), "actor", sc.Actor)
		for _, id := range touched {
			s.bus.Publish(engineChanged(id))
		}
	}
	return diff, nil
}

// applyDetail is what the audit row keeps of an apply: the counts.
func applyDetail(d *apply.Diff, prune bool) map[string]any {
	return map[string]any{"created": len(d.Created), "updated": len(d.Updated), "recreated": len(d.Recreated), "deleted": len(d.Deleted), "unchanged": len(d.Unchanged), "prune": prune}
}
