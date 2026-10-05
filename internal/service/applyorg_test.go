package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/w4jnl/vink/internal/apply"
	"github.com/w4jnl/vink/internal/domain"
)

func TestApplyOrgAndExportOrg(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	orgRW := domain.Scope{OrgID: f.org.ID, Role: domain.RoleAdmin, KeyID: "k1", KeyAccess: domain.AccessRW, Actor: "key:org"}
	orgRO := domain.Scope{OrgID: f.org.ID, Role: domain.RoleViewer, KeyID: "k2", KeyAccess: domain.AccessRO, Actor: "key:orgro"}
	projectKey := domain.Scope{OrgID: f.org.ID, ProjectID: f.project.ID, Role: domain.RoleAdmin, KeyID: "k3", KeyAccess: domain.AccessRW, Actor: "key:proj"}
	orgAdmin := domain.Scope{OrgID: f.org.ID, UserID: "u3", Role: domain.RoleAdmin, Actor: "user:a"}

	file := &apply.OrgFile{Version: 1, Org: "homelab", Projects: []apply.ProjectEntry{
		{Slug: "prod", Name: "Production", Channels: []apply.Channel{{Name: "ops", Kind: "webhook", Config: map[string]any{"url": "https://hooks.example.com/x"}}},
			Monitors: []apply.Monitor{{Slug: "web", Kind: domain.KindHTTP, HTTP: &domain.HTTPCheck{URL: "https://example.com"}}}},
		{Slug: "lab", Name: "Lab", Timezone: "Europe/London", Monitors: []apply.Monitor{{Slug: "nightly", Schedule: &domain.Schedule{Period: domain.MustDuration("1d")}, Grace: domain.MustDuration("1h")}}},
	}}

	// who may: org keys and org admins; not project keys, not members, not ro for apply
	for name, sc := range map[string]domain.Scope{"project key": projectKey, "member": f.member} {
		if _, err := f.svc.ApplyOrg(ctx, sc, file, ApplyOptions{}); !errors.Is(err, domain.ErrForbidden) {
			t.Fatalf("%s apply: %v", name, err)
		}
		if _, err := f.svc.ExportOrg(ctx, sc, false); !errors.Is(err, domain.ErrForbidden) {
			t.Fatalf("%s export: %v", name, err)
		}
	}
	if _, err := f.svc.ApplyOrg(ctx, orgRO, file, ApplyOptions{}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("ro apply: %v", err)
	}
	if _, err := f.svc.ExportOrg(ctx, orgRO, true); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("ro export with secrets: %v", err)
	}
	wrong := *file
	wrong.Org = "acme"
	if _, err := f.svc.ApplyOrg(ctx, orgRW, &wrong, ApplyOptions{}); err == nil || !strings.Contains(err.Error(), "the file is for acme") {
		t.Fatalf("wrong org: %v", err)
	}
	dup := *file
	dup.Projects = append([]apply.ProjectEntry{}, file.Projects...)
	dup.Projects = append(dup.Projects, apply.ProjectEntry{Slug: "prod"})
	if _, err := f.svc.ApplyOrg(ctx, orgRW, &dup, ApplyOptions{}); err == nil || !strings.Contains(err.Error(), "listed twice") {
		t.Fatalf("duplicate: %v", err)
	}

	// a dry run creates nothing
	dry, err := f.svc.ApplyOrg(ctx, orgRW, file, ApplyOptions{DryRun: true})
	if err != nil || !dry.DryRun || len(dry.Projects) != 2 || !dry.Projects[1].Created || dry.Changes() != 4 {
		t.Fatalf("dry run: %+v %v", dry, err)
	}
	if _, err := f.svc.ProjectBySlug(ctx, f.org.ID, "lab"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("dry run must not create: %v", err)
	}

	// the real apply: prod updated, lab created with its monitor
	diff, err := f.svc.ApplyOrg(ctx, orgRW, file, ApplyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(diff.Projects) != 2 || diff.Projects[0].Created || !diff.Projects[1].Created || len(diff.Projects[0].Diff.Created) != 2 || len(diff.Projects[1].Diff.Created) != 1 {
		t.Fatalf("diff: %+v", diff)
	}
	lab, err := f.svc.ProjectBySlug(ctx, f.org.ID, "lab")
	if err != nil || lab.Name != "Lab" || lab.Timezone != "Europe/London" || lab.PingKey == "" {
		t.Fatalf("lab: %+v %v", lab, err)
	}
	labScope := domain.Scope{OrgID: f.org.ID, ProjectID: lab.ID, Role: domain.RoleAdmin, Actor: "test"}
	if m, err := f.svc.MonitorBySlug(ctx, labScope, "nightly"); err != nil || m.Heartbeat == nil {
		t.Fatalf("nightly: %v", err)
	}
	if p, _ := f.svc.ProjectBySlug(ctx, f.org.ID, "prod"); p.Name != "Production" {
		t.Fatalf("prod renamed: %+v", p)
	}

	// again: nothing changes; an org admin's session may apply too
	again, err := f.svc.ApplyOrg(ctx, orgAdmin, file, ApplyOptions{})
	if err != nil || again.Changes() != 0 {
		t.Fatalf("re-apply: %+v %v", again, err)
	}

	// export round-trips, secrets redacted for the ro key
	exported, err := f.svc.ExportOrg(ctx, orgRO, false)
	if err != nil || exported.Org != "homelab" || len(exported.Projects) != 2 || exported.Projects[0].Slug != "lab" || exported.Projects[1].Slug != "prod" {
		t.Fatalf("export: %+v %v", exported, err)
	}
	if ch := exported.Projects[1].Channels; len(ch) != 1 || ch[0].Config["url"] != "https://hooks.example.com/x" {
		t.Fatalf("export channel: %+v", ch)
	}
	out, err := apply.EncodeOrg(exported)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := apply.ParseOrg(out, true)
	if err != nil {
		t.Fatalf("exported file invalid: %v\n%s", err, out)
	}
	if d, err := f.svc.ApplyOrg(ctx, orgRW, parsed, ApplyOptions{}); err != nil || d.Changes() != 0 {
		t.Fatalf("round trip apply: %+v %v", d, err)
	}

	// a project the file does not name is left alone; a file error inside a
	// project names the project
	only := &apply.OrgFile{Version: 1, Org: "homelab", Projects: []apply.ProjectEntry{{Slug: "lab", Monitors: []apply.Monitor{{Slug: "bad slug!"}}}}}
	if _, err := f.svc.ApplyOrg(ctx, orgRW, only, ApplyOptions{}); err == nil || !strings.Contains(err.Error(), "projects[0].") {
		t.Fatalf("nested error: %v", err)
	}
	if m, err := f.svc.MonitorBySlug(ctx, f.member, "web"); err != nil || m == nil {
		t.Fatalf("prod untouched: %v", err)
	}
}

// TestApplyOrgStatusPages: an org file creates and updates the org's own
// pages after its projects, names projects by slug both ways, never
// deletes a page, and a dry run keeps nothing.
func TestApplyOrgStatusPages(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	orgRW := domain.Scope{OrgID: f.org.ID, Role: domain.RoleAdmin, KeyID: "k1", KeyAccess: domain.AccessRW, Actor: "key:org"}
	file := &apply.OrgFile{Version: 1, Org: "homelab",
		Projects:    []apply.ProjectEntry{{Slug: "lab", Name: "Lab"}},
		StatusPages: []apply.StatusPage{{Slug: "everything", Title: "Everything", Projects: []string{"lab", "prod"}, GroupBy: "tag", Incidents: "30d"}},
	}
	dry, err := f.svc.ApplyOrg(ctx, orgRW, file, ApplyOptions{DryRun: true})
	if err != nil || dry.StatusPages == nil || len(dry.StatusPages.Created) != 1 || dry.Changes() != 2 {
		t.Fatalf("dry run: %+v %v", dry, err)
	}
	if list, _ := f.svc.listOrgPagesForFile(ctx, orgRW); len(list) != 0 {
		t.Fatal("a dry run kept the page")
	}
	// the page names lab, which the same file creates
	diff, err := f.svc.ApplyOrg(ctx, orgRW, file, ApplyOptions{})
	if err != nil || len(diff.StatusPages.Created) != 1 {
		t.Fatalf("apply: %+v %v", diff, err)
	}
	page, err := f.svc.OrgStatusPage(ctx, orgRW, "everything")
	lab, _ := f.svc.ProjectBySlug(ctx, f.org.ID, "lab")
	if err != nil || len(page.Projects) != 2 || page.Projects[0] != lab.ID || page.Projects[1] != f.project.ID || page.GroupBy != "tag" || page.Incidents != "30d" {
		t.Fatalf("page: %+v %v", page, err)
	}
	// export writes it back by slug; applying the export changes nothing
	out, err := f.svc.ExportOrg(ctx, orgRW, false)
	if err != nil || len(out.StatusPages) != 1 || strings.Join(out.StatusPages[0].Projects, ",") != "lab,prod" || out.StatusPages[0].GroupBy != "tag" || out.StatusPages[0].Incidents != "30d" {
		t.Fatalf("export: %+v %v", out.StatusPages, err)
	}
	again, err := f.svc.ApplyOrg(ctx, orgRW, out, ApplyOptions{})
	if err != nil || again.Changes() != 0 || len(again.StatusPages.Unchanged) != 1 {
		t.Fatalf("round trip: %+v %v", again, err)
	}
	// a change updates; a page the file leaves out stays
	file.StatusPages = []apply.StatusPage{{Slug: "everything", Title: "Everything", Projects: []string{"prod"}}}
	diff, err = f.svc.ApplyOrg(ctx, orgRW, file, ApplyOptions{Prune: true})
	if err != nil || len(diff.StatusPages.Updated) != 1 {
		t.Fatalf("update: %+v %v", diff, err)
	}
	page, _ = f.svc.OrgStatusPage(ctx, orgRW, "everything")
	if page.GroupBy != "project" || page.Incidents != "open" || len(page.Projects) != 1 {
		t.Fatalf("updated: %+v", page)
	}
	file.StatusPages = nil
	if _, err := f.svc.ApplyOrg(ctx, orgRW, file, ApplyOptions{Prune: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.OrgStatusPage(ctx, orgRW, "everything"); err != nil {
		t.Fatalf("a page the file leaves out must stay: %v", err)
	}
	// unknown projects and a project page's address are refused
	file.StatusPages = []apply.StatusPage{{Slug: "x", Title: "X", Projects: []string{"nope"}}}
	if _, err := f.svc.ApplyOrg(ctx, orgRW, file, ApplyOptions{}); err == nil || !strings.Contains(err.Error(), "status_pages[0].projects") {
		t.Fatalf("unknown project: %v", err)
	}
	if _, err := f.svc.CreateStatusPage(ctx, f.member, &domain.StatusPage{Slug: "prod-status", Title: "Prod", Public: true}, ""); err != nil {
		t.Fatal(err)
	}
	file.StatusPages = []apply.StatusPage{{Slug: "prod-status", Title: "Taken"}}
	if _, err := f.svc.ApplyOrg(ctx, orgRW, file, ApplyOptions{}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("a project page's address: %v", err)
	}
	// incidents round-trips through a project file too
	pf := &apply.File{Version: 1, StatusPages: []apply.StatusPage{{Slug: "prod-status", Title: "Prod", Incidents: "7d"}}}
	if _, err := f.svc.Apply(ctx, f.member, pf, ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	exported, err := f.svc.Export(ctx, f.member, false)
	if err != nil || len(exported.StatusPages) != 1 || exported.StatusPages[0].Incidents != "7d" {
		t.Fatalf("project export: %+v %v", exported.StatusPages, err)
	}
	if d, err := f.svc.Apply(ctx, f.member, exported, ApplyOptions{}); err != nil || d.Changes() != 0 {
		t.Fatalf("project round trip: %+v %v", d, err)
	}
}
