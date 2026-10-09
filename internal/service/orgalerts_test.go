package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/w4jnl/vink/internal/apply"
	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/engine"
)

func webhook(url string) json.RawMessage { return json.RawMessage(`{"url":"` + url + `"}`) }

// invalidField says whether err is a validation error on field.
func invalidField(err error, field string) bool {
	var ve *domain.ValidationError
	if !errors.As(err, &ve) {
		return false
	}
	for _, fe := range ve.Errors {
		if fe.Field == field {
			return true
		}
	}
	return false
}

// TestOrgChannelsAndRoutes: org admins keep channels of the org's own and
// routes that send there for chosen projects or all; the org's routes fire
// beside each project's.
func TestOrgChannelsAndRoutes(t *testing.T) {
	f := newFixture(t)
	withNotifier(t, f)
	ctx := context.Background()
	orgSc := domain.Scope{OrgID: f.org.ID, UserID: "u3", Role: domain.RoleAdmin, Actor: "user:a"}
	orgMember := domain.Scope{OrgID: f.org.ID, UserID: "u1", Role: domain.RoleMember, Actor: "user:j"}
	lab, err := f.svc.CreateProject(ctx, f.admin, f.org.ID, "lab", "Lab", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	labSc := domain.Scope{OrgID: f.org.ID, ProjectID: lab.ID, UserID: "u1", Role: domain.RoleMember, Actor: "user:j"}
	org := newReceiver(t)
	own := newReceiver(t)

	// only org admins
	for name, sc := range map[string]domain.Scope{"member": orgMember, "project member": f.member} {
		if _, err := f.svc.CreateOrgChannel(ctx, sc, &domain.Channel{Name: "x", Kind: domain.ChannelWebhook, Config: webhook(org.srv.URL), Enabled: true}); !errors.Is(err, domain.ErrForbidden) {
			t.Fatalf("%s create: %v", name, err)
		}
		if _, err := f.svc.ListOrgRoutes(ctx, sc); !errors.Is(err, domain.ErrForbidden) {
			t.Fatalf("%s list routes: %v", name, err)
		}
	}

	ch, err := f.svc.CreateOrgChannel(ctx, orgSc, &domain.Channel{Name: "oncall", Kind: domain.ChannelWebhook, Config: webhook(org.srv.URL), Enabled: true})
	if err != nil || !ch.IsOrg() {
		t.Fatalf("create: %+v %v", ch, err)
	}
	if _, err := f.svc.CreateOrgChannel(ctx, orgSc, &domain.Channel{Name: "oncall", Kind: domain.ChannelWebhook, Config: webhook(org.srv.URL), Enabled: true}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate name: %v", err)
	}
	// a project's channel of the same name is another channel; it comes
	// with the project's default route, the org's comes with none
	projCh, err := f.svc.CreateChannel(ctx, f.member, &domain.Channel{Name: "oncall", Kind: domain.ChannelWebhook, Config: webhook(own.srv.URL), Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if routes, _ := f.svc.ListOrgRoutes(ctx, orgSc); len(routes) != 0 {
		t.Fatalf("an org channel made a route: %d", len(routes))
	}
	if list, _ := f.svc.ListOrgChannels(ctx, orgSc); len(list) != 1 || list[0].ID != ch.ID {
		t.Fatalf("org channels: %+v", list)
	}
	if list, _ := f.svc.ListChannels(ctx, f.member); len(list) != 1 || list[0].ID != projCh.ID {
		t.Fatalf("project channels: %+v", list)
	}
	if _, err := f.svc.Channel(ctx, f.member, ch.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("a project reads an org channel: %v", err)
	}
	if _, err := f.svc.OrgChannel(ctx, orgSc, projCh.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("the org reads a project channel: %v", err)
	}

	// routes name the org's channels and projects only
	if _, err := f.svc.CreateOrgRoute(ctx, orgSc, &domain.Route{ChannelIDs: []string{projCh.ID}}); !invalidField(err, "channels") {
		t.Fatalf("a project channel: %v", err)
	}
	if _, err := f.svc.CreateOrgRoute(ctx, orgSc, &domain.Route{ChannelIDs: []string{ch.ID}, Projects: []string{"01ARZ3NDEKTSV4RRFFQ69G5FAV"}}); !invalidField(err, "projects") {
		t.Fatalf("an unknown project: %v", err)
	}
	if _, err := f.svc.CreateRoute(ctx, f.member, &domain.Route{ChannelIDs: []string{ch.ID}}); err == nil {
		t.Fatal("a project route sends to an org channel")
	}
	labOnly, err := f.svc.CreateOrgRoute(ctx, orgSc, &domain.Route{ChannelIDs: []string{ch.ID, ch.ID}, Projects: []string{lab.ID, lab.ID}, On: []domain.State{domain.StateDown}})
	if err != nil || len(labOnly.ChannelIDs) != 1 || len(labOnly.Projects) != 1 || !labOnly.Covers(lab.ID) || labOnly.Covers(f.project.ID) {
		t.Fatalf("lab only: %+v %v", labOnly, err)
	}
	everyDB, err := f.svc.CreateOrgRoute(ctx, orgSc, &domain.Route{ChannelIDs: []string{ch.ID}, MatchTags: []string{"DB"}})
	if err != nil || len(everyDB.Projects) != 0 || !everyDB.Covers(lab.ID) || !everyDB.Covers(f.project.ID) || len(everyDB.On) != 2 || everyDB.MatchTags[0] != "db" {
		t.Fatalf("every project: %+v %v", everyDB, err)
	}
	if n, _ := f.svc.OrgRouteCountForChannel(ctx, orgSc, ch.ID); n != 2 {
		t.Fatalf("routes for the channel: %d", n)
	}

	// prod's db monitor: its own default route and the every-project one
	disp := engine.NewDispatcher(f.svc, f.svc.log, f.clock.Now)
	f.heartbeat(t, "db", "1h", "5m", "db")
	tgt, _ := f.svc.ResolvePing(ctx, f.project.PingKey, "db", "", false)
	if _, _, err := f.svc.RecordPing(ctx, tgt, PingObservation{Signal: domain.SignalFail}); err != nil {
		t.Fatal(err)
	}
	if sent, failed, err := disp.RunOnce(ctx); err != nil || sent != 2 || failed != 0 {
		t.Fatalf("prod: sent=%d failed=%d err=%v", sent, failed, err)
	}
	if org.count() != 1 || own.count() != 1 || org.last()["event"] != "down" {
		t.Fatalf("prod: org=%d own=%d %v", org.count(), own.count(), org.last())
	}
	if last, _ := f.svc.LastSentForOrgChannel(ctx, orgSc, ch.ID); last == nil {
		t.Fatal("no last sent for the org channel")
	}
	// lab's untagged monitor: the lab-only route
	if _, err := f.svc.CreateMonitor(ctx, labSc, &domain.Monitor{Slug: "nightly", Name: "nightly", Kind: domain.KindHeartbeat, Tags: []string{},
		Heartbeat: &domain.HeartbeatSpec{Schedule: domain.Schedule{Period: domain.MustDuration("1d")}, Grace: domain.MustDuration("1h")}}); err != nil {
		t.Fatal(err)
	}
	labTgt, _ := f.svc.ResolvePing(ctx, lab.PingKey, "nightly", "", false)
	if _, _, err := f.svc.RecordPing(ctx, labTgt, PingObservation{Signal: domain.SignalFail}); err != nil {
		t.Fatal(err)
	}
	if sent, _, _ := disp.RunOnce(ctx); sent != 1 || org.count() != 2 || own.count() != 1 {
		t.Fatalf("lab: sent=%d org=%d own=%d", sent, org.count(), own.count())
	}
	if links, _ := org.last()["links"].(map[string]any); links == nil || !strings.Contains(links["monitor"].(string), "/o/homelab/p/lab/m/nightly") {
		t.Fatalf("lab links: %v", org.last())
	}

	// an org route repeats as a project's does
	if _, err := f.svc.UpdateOrgRoute(ctx, orgSc, labOnly.ID, &domain.Route{ChannelIDs: []string{ch.ID}, Projects: []string{lab.ID}, On: []domain.State{domain.StateDown}, RepeatEvery: 10 * time.Minute}); err != nil {
		t.Fatal(err)
	}
	f.clock.Add(11 * time.Minute)
	if sent, _, _ := disp.RunOnce(ctx); sent != 1 || org.last()["repeat"] != true {
		t.Fatalf("repeat: sent=%d %v", sent, org.last())
	}

	// a disabled org channel gets nothing new
	if off, err := f.svc.SetOrgChannelEnabled(ctx, orgSc, ch.ID, false); err != nil || off.Enabled {
		t.Fatalf("disable: %+v %v", off, err)
	}
	f.clock.Add(time.Minute)
	if _, _, err := f.svc.RecordPing(ctx, tgt, PingObservation{Signal: domain.SignalOK}); err != nil {
		t.Fatal(err)
	}
	if sent, _, _ := disp.RunOnce(ctx); sent != 1 || org.count() != 3 || own.count() != 2 {
		t.Fatalf("disabled: sent=%d org=%d own=%d", sent, org.count(), own.count())
	}

	// update keeps a secret sent back as ***, and renames
	upd, err := f.svc.UpdateOrgChannel(ctx, orgSc, ch.ID, &domain.Channel{Name: "pager", Kind: domain.ChannelWebhook, Config: webhook(org.srv.URL + "/v2"), Enabled: true})
	if err != nil || upd.Name != "pager" || !upd.Enabled || !strings.Contains(string(upd.Config), "/v2") {
		t.Fatalf("update: %+v %v", upd, err)
	}
	if err := f.svc.TestOrgChannel(ctx, orgSc, ch.ID); err != nil || org.last()["event"] != "test" {
		t.Fatalf("test: %v %v", err, org.last())
	}
	if err := f.svc.TestOrgChannel(ctx, f.member, ch.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("a member tests: %v", err)
	}

	entries, _ := f.svc.AuditLog(ctx, f.admin, AuditFilter{OrgID: f.org.ID, Changes: true})
	seen := map[string]bool{}
	for _, e := range entries.Entries {
		if e.ProjectID == "" {
			seen[e.Action] = true
		}
	}
	for _, a := range []string{"channel.create", "channel.update", "route.create", "route.update"} {
		if !seen[a] {
			t.Errorf("no org audit row for %s: %v", a, seen)
		}
	}

	// deleting the channel takes the routes it leaves with none
	if err := f.svc.DeleteOrgRoute(ctx, f.member, everyDB.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("a member deletes: %v", err)
	}
	if err := f.svc.DeleteOrgChannel(ctx, orgSc, ch.ID); err != nil {
		t.Fatal(err)
	}
	if routes, _ := f.svc.ListOrgRoutes(ctx, orgSc); len(routes) != 0 {
		t.Fatalf("routes left with no channel: %d", len(routes))
	}
	if routes, _ := f.svc.ListRoutes(ctx, f.member); len(routes) != 1 {
		t.Fatalf("the project's route went too: %d", len(routes))
	}
	if err := f.svc.DeleteOrgRoute(ctx, orgSc, everyDB.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("delete again: %v", err)
	}
}

// TestApplyOrgChannelsAndRoutes: an org file carries the org's channels
// and routes, projects by slug; prune deletes what it leaves out, and a
// file without either key leaves them alone.
func TestApplyOrgChannelsAndRoutes(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	orgRW := domain.Scope{OrgID: f.org.ID, Role: domain.RoleAdmin, KeyID: "k1", KeyAccess: domain.AccessRW, Actor: "key:org"}
	orgRO := domain.Scope{OrgID: f.org.ID, Role: domain.RoleViewer, KeyID: "k2", KeyAccess: domain.AccessRO, Actor: "key:orgro"}
	file := &apply.OrgFile{Version: 1, Org: "homelab",
		Projects: []apply.ProjectEntry{{Slug: "lab", Name: "Lab"}},
		Channels: []apply.Channel{{Name: "oncall", Kind: "webhook", Config: map[string]any{"url": "https://hooks.example.com/org"}}},
		Routes: []apply.Route{
			{Channels: []string{"oncall"}},
			{Channels: []string{"oncall"}, MatchTags: []string{"db"}, Projects: []string{"prod", "lab"}, On: []domain.State{domain.StateDown}},
		},
	}
	dry, err := f.svc.ApplyOrg(ctx, orgRW, file, ApplyOptions{DryRun: true})
	if err != nil || dry.Alerts == nil || len(dry.Alerts.Created) != 3 {
		t.Fatalf("dry run: %+v %v", dry, err)
	}
	if list, _, _ := f.svc.listOrgAlertsForFile(ctx, orgRW); len(list) != 0 {
		t.Fatal("a dry run kept the channel")
	}
	diff, err := f.svc.ApplyOrg(ctx, orgRW, file, ApplyOptions{})
	if err != nil || len(diff.Alerts.Created) != 3 {
		t.Fatalf("apply: %+v %v", diff, err)
	}
	lab, _ := f.svc.ProjectBySlug(ctx, f.org.ID, "lab")
	channels, routes, _ := f.svc.listOrgAlertsForFile(ctx, orgRW)
	if len(channels) != 1 || len(routes) != 2 {
		t.Fatalf("after apply: %d channels, %d routes", len(channels), len(routes))
	}
	for _, r := range routes {
		if len(r.MatchTags) == 1 && (len(r.Projects) != 2 || !r.Covers(lab.ID) || !r.Covers(f.project.ID)) {
			t.Fatalf("db route: %+v", r)
		}
	}
	// the project's own alerting is untouched
	if list, _ := f.svc.ListChannels(ctx, f.member); len(list) != 0 {
		t.Fatalf("project channels: %d", len(list))
	}

	// export writes them back, projects by slug, secrets redacted for ro
	out, err := f.svc.ExportOrg(ctx, orgRO, false)
	if err != nil || len(out.Channels) != 1 || len(out.Routes) != 2 {
		t.Fatalf("export: %+v %v", out, err)
	}
	var dbRoute apply.Route
	for _, r := range out.Routes {
		if len(r.MatchTags) == 1 {
			dbRoute = r
		}
	}
	if strings.Join(dbRoute.Projects, ",") != "lab,prod" {
		t.Fatalf("exported projects: %v", dbRoute.Projects)
	}
	enc, err := apply.EncodeOrg(out)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := apply.ParseOrg(enc, true)
	if err != nil {
		t.Fatalf("exported file invalid: %v\n%s", err, enc)
	}
	if again, err := f.svc.ApplyOrg(ctx, orgRW, parsed, ApplyOptions{}); err != nil || again.Changes() != 0 || len(again.Alerts.Unchanged) != 3 {
		t.Fatalf("round trip: %+v %v", again, err)
	}

	// a change of projects updates the route in place
	file.Routes[1].Projects = []string{"lab"}
	diff, err = f.svc.ApplyOrg(ctx, orgRW, file, ApplyOptions{})
	if err != nil || len(diff.Alerts.Updated) != 1 {
		t.Fatalf("update: %+v %v", diff, err)
	}

	// without the keys the org's alerting stays, prune or not
	bare := &apply.OrgFile{Version: 1, Org: "homelab", Projects: []apply.ProjectEntry{{Slug: "lab", Name: "Lab"}}}
	if d, err := f.svc.ApplyOrg(ctx, orgRW, bare, ApplyOptions{Prune: true}); err != nil || d.Alerts != nil {
		t.Fatalf("bare: %+v %v", d, err)
	}
	if channels, routes, _ := f.svc.listOrgAlertsForFile(ctx, orgRW); len(channels) != 1 || len(routes) != 2 {
		t.Fatalf("bare file pruned: %d %d", len(channels), len(routes))
	}
	// prune deletes the routes and channels the file leaves out
	file.Channels = append(file.Channels, apply.Channel{Name: "spare", Kind: "webhook", Config: map[string]any{"url": "https://hooks.example.com/spare"}})
	file.Routes = file.Routes[:1]
	if _, err := f.svc.ApplyOrg(ctx, orgRW, file, ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	file.Channels = file.Channels[1:]
	file.Routes = []apply.Route{{Channels: []string{"spare"}}}
	diff, err = f.svc.ApplyOrg(ctx, orgRW, file, ApplyOptions{Prune: true})
	if err != nil || len(diff.Alerts.Deleted) != 3 {
		t.Fatalf("prune: %+v %v", diff, err)
	}
	if channels, routes, _ := f.svc.listOrgAlertsForFile(ctx, orgRW); len(channels) != 1 || channels[0].Name != "spare" || len(routes) != 1 {
		t.Fatalf("after prune: %d %d", len(channels), len(routes))
	}

	// unknown names are refused with their place in the file
	file.Routes = []apply.Route{{Channels: []string{"nope"}}}
	if _, err := f.svc.ApplyOrg(ctx, orgRW, file, ApplyOptions{}); err == nil || !strings.Contains(err.Error(), "routes[0].channels") {
		t.Fatalf("unknown channel: %v", err)
	}
	file.Routes = []apply.Route{{Channels: []string{"spare"}, Projects: []string{"nope"}}}
	if _, err := f.svc.ApplyOrg(ctx, orgRW, file, ApplyOptions{}); err == nil || !strings.Contains(err.Error(), "routes[0].projects") {
		t.Fatalf("unknown project: %v", err)
	}
}
