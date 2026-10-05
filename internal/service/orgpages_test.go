package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/w4jnl/vink/internal/domain"
)

// orgPages extends the fixture with a second project in the org, a
// monitor slug both projects use, and another org that must stay out.
type orgPages struct {
	*fixture
	staging  *domain.Project
	orgAdmin domain.Scope
	stagingM domain.Scope
	other    *domain.Project
}

func newOrgPages(t *testing.T) *orgPages {
	t.Helper()
	f := newFixture(t)
	ctx := context.Background()
	staging, err := f.svc.CreateProject(ctx, f.admin, f.org.ID, "staging", "Staging", "Europe/Amsterdam")
	if err != nil {
		t.Fatal(err)
	}
	otherOrg, err := f.svc.CreateOrg(ctx, f.admin, "acme", "Acme")
	if err != nil {
		t.Fatal(err)
	}
	other, err := f.svc.CreateProject(ctx, f.admin, otherOrg.ID, "x", "Acme X", "Europe/Amsterdam")
	if err != nil {
		t.Fatal(err)
	}
	o := &orgPages{
		fixture: f, staging: staging, other: other,
		orgAdmin: domain.Scope{OrgID: f.org.ID, UserID: "u0", Role: domain.RoleAdmin, Actor: "user:a"},
		stagingM: domain.Scope{OrgID: f.org.ID, ProjectID: staging.ID, UserID: "u1", Role: domain.RoleMember, Actor: "user:j"},
	}
	f.heartbeat(t, "backup", "1d", "1h", "backup")
	f.heartbeat(t, "api", "1h", "5m", "prod")
	o.monitorIn(t, o.stagingM, "api", "prod")
	o.monitorIn(t, domain.Scope{OrgID: otherOrg.ID, ProjectID: other.ID, Role: domain.RoleMember, Actor: "user:x"}, "secret", "prod")
	return o
}

func (o *orgPages) monitorIn(t *testing.T, sc domain.Scope, slug string, tags ...string) {
	t.Helper()
	_, err := o.svc.CreateMonitor(context.Background(), sc, &domain.Monitor{
		Slug: slug, Name: slug, Kind: domain.KindHeartbeat, Tags: tags,
		Heartbeat: &domain.HeartbeatSpec{Schedule: domain.Schedule{Period: domain.MustDuration("1h")}, Grace: domain.MustDuration("5m")},
	})
	if err != nil {
		t.Fatalf("create %s: %v", slug, err)
	}
}

// ping records a signal for a monitor of a project.
func (o *orgPages) ping(t *testing.T, project *domain.Project, slug string, signal domain.Signal) {
	t.Helper()
	ctx := context.Background()
	tgt, err := o.svc.ResolvePing(ctx, project.PingKey, slug, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := o.svc.RecordPing(ctx, tgt, PingObservation{Signal: signal, At: o.clock.Now()}); err != nil {
		t.Fatal(err)
	}
}

func TestOrgStatusPageManagement(t *testing.T) {
	o := newOrgPages(t)
	ctx := context.Background()
	page, err := o.svc.CreateOrgStatusPage(ctx, o.orgAdmin, &domain.StatusPage{Title: "Everything", Slug: "all", Public: true}, "")
	if err != nil {
		t.Fatal(err)
	}
	if !page.IsOrg() || page.OrgID != o.org.ID || page.GroupBy != domain.GroupByProject || page.Incidents != domain.IncidentsOpen || len(page.Projects) != 0 {
		t.Fatalf("created: %+v", page)
	}
	// members manage project pages, not the org's
	if _, err := o.svc.CreateOrgStatusPage(ctx, o.member, &domain.StatusPage{Title: "Mine", Slug: "mine", Public: true}, ""); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("member: %v", err)
	}
	if _, err := o.svc.ListOrgStatusPages(ctx, o.member); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("member list: %v", err)
	}
	// another org's project cannot be listed
	_, err = o.svc.CreateOrgStatusPage(ctx, o.orgAdmin, &domain.StatusPage{Title: "Sneaky", Slug: "sneaky", Public: true, Projects: []string{o.other.ID}}, "")
	if ve, ok := domain.AsValidation(err); !ok || !strings.Contains(ve.Error(), "not a project of this org") {
		t.Fatalf("foreign project: %v", err)
	}
	// addresses are shared with project pages
	if _, err := o.svc.CreateStatusPage(ctx, o.member, &domain.StatusPage{Title: "Clash", Slug: "all", Public: true}, ""); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("slug clash: %v", err)
	}
	upd, err := o.svc.UpdateOrgStatusPage(ctx, o.orgAdmin, "all", &domain.StatusPage{Title: "Staging", Slug: "staging-status", Public: true,
		Projects: []string{o.staging.ID}, GroupBy: "tag", Incidents: "30d"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if upd.Slug != "staging-status" || upd.GroupBy != domain.GroupByTag || upd.Incidents != "30d" || strings.Join(upd.Projects, ",") != o.staging.ID {
		t.Fatalf("updated: %+v", upd)
	}
	pages, err := o.svc.ListOrgStatusPages(ctx, o.orgAdmin)
	if err != nil || len(pages) != 1 || pages[0].Slug != "staging-status" {
		t.Fatalf("list: %v %+v", err, pages)
	}
	// the project's own list does not show the org's page, and the org
	// page cannot be reached through a project
	if list, _ := o.svc.ListStatusPages(ctx, o.member); len(list) != 0 {
		t.Fatalf("project list shows %d pages", len(list))
	}
	if _, err := o.svc.StatusPage(ctx, o.member, "staging-status"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("org page through a project: %v", err)
	}
	if err := o.svc.DeleteOrgStatusPage(ctx, o.orgAdmin, "staging-status"); err != nil {
		t.Fatal(err)
	}
	if _, err := o.svc.OrgStatusPage(ctx, o.orgAdmin, "staging-status"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
	var rows int
	var snapshot string
	err = o.svc.DB().Reader.QueryRowContext(ctx, `SELECT COUNT(*), MAX(COALESCE(spec_after, '')) FROM audit WHERE act LIKE 'page.%' AND org_id = ? AND project_id IS NULL`, o.org.ID).Scan(&rows, &snapshot)
	if err != nil || rows != 3 || !strings.Contains(snapshot, "projects: [staging]") || !strings.Contains(snapshot, "incidents: 30d") {
		t.Fatalf("audit: %d rows, %v, snapshot %q", rows, err, snapshot)
	}
}

func TestOrgStatusPageShowsTheOrgsProjects(t *testing.T) {
	o := newOrgPages(t)
	ctx := context.Background()
	page := &domain.StatusPage{OrgID: o.org.ID, Slug: "all", Title: "All", Public: true}
	page.Normalize()
	st, err := o.svc.PublicStatus(ctx, page, o.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got := groupsOf(st); got != "Production: api backup | Staging: api" {
		t.Fatalf("by project: %s", got)
	}
	if st.Location.String() != "Europe/Amsterdam" || len(st.Projects) != 2 {
		t.Errorf("location %s, %d projects", st.Location, len(st.Projects))
	}
	for _, m := range st.Monitors {
		if m.ProjectID == o.other.ID {
			t.Fatalf("another org's monitor on the page: %s", m.Slug)
		}
		if st.ProjectOf(m.ProjectID) == nil {
			t.Fatalf("no project for %s", m.Slug)
		}
	}

	page.GroupBy, page.MatchTags = domain.GroupByTag, []string{"prod", "backup"}
	st, _ = o.svc.PublicStatus(ctx, page, o.clock.Now())
	if got := groupsOf(st); got != "prod: api api | backup: backup" {
		t.Fatalf("by tag: %s", got)
	}

	page.Projects, page.GroupBy, page.MatchTags = []string{o.staging.ID, "deleted-project"}, domain.GroupByProject, nil
	st, _ = o.svc.PublicStatus(ctx, page, o.clock.Now())
	if got := groupsOf(st); got != "Staging: api" {
		t.Fatalf("chosen projects: %s", got)
	}

	// projects on different clocks show UTC
	if _, err := o.svc.CreateProject(ctx, o.admin, o.org.ID, "tokyo", "Tokyo", "Asia/Tokyo"); err != nil {
		t.Fatal(err)
	}
	page.Projects = nil
	st, _ = o.svc.PublicStatus(ctx, page, o.clock.Now())
	if st.Location != time.UTC {
		t.Errorf("mixed timezones: %s", st.Location)
	}

	// a project page is unchanged: its own monitors, its own clock
	project := &domain.StatusPage{OrgID: o.org.ID, ProjectID: o.project.ID, Slug: "prod", Title: "Prod", Public: true}
	project.Normalize()
	st, _ = o.svc.PublicStatus(ctx, project, o.clock.Now())
	if got := groupsOf(st); got != "backup: backup | prod: api" || st.Location.String() != "Europe/Amsterdam" {
		t.Fatalf("project page: %s %s", got, st.Location)
	}
}

func TestStatusPageIncidents(t *testing.T) {
	o := newOrgPages(t)
	ctx := context.Background()
	// staging's api fails and recovers, prod's api fails and stays down
	o.ping(t, o.staging, "api", domain.SignalFail)
	o.clock.Add(12 * time.Minute)
	o.ping(t, o.staging, "api", domain.SignalOK)
	o.clock.Add(time.Hour)
	o.ping(t, o.project, "api", domain.SignalFail)

	page := &domain.StatusPage{OrgID: o.org.ID, Slug: "all", Title: "All", Public: true, Incidents: "7d"}
	page.Normalize()
	st, err := o.svc.PublicStatus(ctx, page, o.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	if st.Down != 1 || len(st.Incidents) != 1 || st.Incidents[0].ProjectID != o.project.ID {
		t.Fatalf("open: down %d, %+v", st.Down, st.Incidents)
	}
	if len(st.PastIncidents) != 1 || st.PastIncidents[0].ProjectID != o.staging.ID || st.PastIncidents[0].ResolvedAt == nil ||
		st.PastIncidents[0].ResolvedAt.Sub(st.PastIncidents[0].OpenedAt) != 12*time.Minute {
		t.Fatalf("past: %+v", st.PastIncidents)
	}
	// open only: no past; the window ends: none either
	page.Incidents = domain.IncidentsOpen
	if st, _ = o.svc.PublicStatus(ctx, page, o.clock.Now()); len(st.PastIncidents) != 0 || len(st.Incidents) != 1 {
		t.Fatalf("open only: %d past, %d open", len(st.PastIncidents), len(st.Incidents))
	}
	page.Incidents = "7d"
	if st, _ = o.svc.PublicStatus(ctx, page, o.clock.Now().Add(8*24*time.Hour)); len(st.PastIncidents) != 0 {
		t.Fatalf("after the window: %d past", len(st.PastIncidents))
	}
	// none still gathers the open ones, for the banner
	page.Incidents = domain.IncidentsNone
	if st, _ = o.svc.PublicStatus(ctx, page, o.clock.Now()); len(st.Incidents) != 1 || len(st.PastIncidents) != 0 {
		t.Fatalf("none: %d open, %d past", len(st.Incidents), len(st.PastIncidents))
	}
}

// groupsOf reads "Group: slug slug | Group: slug".
func groupsOf(st *PublicStatus) string {
	var parts []string
	for _, g := range st.Groups {
		var slugs []string
		for _, m := range g.Monitors {
			slugs = append(slugs, m.Slug)
		}
		parts = append(parts, g.Name+": "+strings.Join(slugs, " "))
	}
	return strings.Join(parts, " | ")
}
