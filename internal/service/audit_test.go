package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/w4jnl/vink/internal/apply"
	"github.com/w4jnl/vink/internal/audit"
	"github.com/w4jnl/vink/internal/domain"
)

func (f *fixture) auditAll(t *testing.T, sc domain.Scope, filter AuditFilter) []AuditEntry {
	t.Helper()
	page, err := f.svc.AuditLog(context.Background(), sc, filter)
	if err != nil {
		t.Fatalf("audit log: %v", err)
	}
	return page.Entries
}

func TestAuditRecordsEveryActionInTheSameTransaction(t *testing.T) {
	f := newFixture(t)
	ctx := audit.WithRequest(context.Background(), audit.Request{Via: audit.ViaWeb, RequestID: "01REQ", RemoteAddr: "10.0.4.12"})
	anne, err := f.svc.CreateLocalUser(ctx, f.admin, "anne", "", "", "correct horse", false)
	if err != nil {
		t.Fatal(err)
	}
	orgAdmin := domain.Scope{OrgID: f.org.ID, UserID: anne.ID, Role: domain.RoleAdmin, Actor: "user:anne"}
	member := f.member

	// a monitor is created, changed and deleted
	m, err := f.svc.CreateMonitor(ctx, member, &domain.Monitor{Slug: "api", Name: "API", Kind: domain.KindHTTP, Pull: &domain.PullSpec{Interval: domain.MustDuration("60s"), HTTP: &domain.HTTPCheck{URL: "https://api.example.com"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.SetMembership(ctx, orgAdmin, anne.ID, f.org.ID, domain.RoleMember); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.UpdateMonitor(ctx, member, "api", &domain.Monitor{Name: "API", Pull: &domain.PullSpec{Interval: domain.MustDuration("30s"), HTTP: &domain.HTTPCheck{URL: "https://api.example.com"}}}); err != nil {
		t.Fatal(err)
	}
	// a failed create leaves no row
	if _, err := f.svc.CreateMonitor(ctx, member, &domain.Monitor{Slug: "api", Kind: domain.KindHeartbeat, Heartbeat: &domain.HeartbeatSpec{Schedule: domain.Schedule{Period: domain.MustDuration("1h")}}}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate: %v", err)
	}
	// a channel with a secret, redacted in the snapshot
	ch, err := f.svc.CreateChannel(ctx, member, &domain.Channel{Name: "ops", Kind: domain.ChannelWebhook, Enabled: true, Config: []byte(`{"url":"https://hooks.example.com/x","headers":{"Authorization":"Bearer s3cret"}}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.svc.CreateAPIKey(ctx, member, "deploy", domain.AccessRO); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.SetMembership(ctx, orgAdmin, anne.ID, f.org.ID, domain.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	// the apply row, and no row for a dry run
	file := &apply.File{Version: 1, Monitors: []apply.Monitor{{Slug: "api", Kind: domain.KindHTTP, HTTP: &domain.HTTPCheck{URL: "https://api.example.com"}}, {Slug: "db", Kind: domain.KindTCP, TCP: &domain.TCPCheck{Host: "db", Port: 5432}}}}
	if _, err := f.svc.Apply(ctx, member, file, ApplyOptions{DryRun: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Apply(ctx, member, file, ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.DeleteMonitor(ctx, member, "api"); err != nil {
		t.Fatal(err)
	}
	// a state flip by vink joins the log through events
	f.clock.Add(time.Minute)
	hb := f.heartbeat(t, "nightly", "1h", "5m")
	tgt, _ := f.svc.ResolvePing(ctx, f.project.PingKey, hb.Slug, "", false)
	if _, _, err := f.svc.RecordPing(ctx, tgt, PingObservation{Signal: domain.SignalOK}); err != nil {
		t.Fatal(err)
	}

	entries := f.auditAll(t, orgAdmin, AuditFilter{})
	actions := make([]string, 0, len(entries))
	for _, e := range entries {
		actions = append(actions, e.Action)
	}
	want := []string{"state.up", "monitor.create", "monitor.delete", "apply", "member.role", "key.create", "channel.create", "monitor.update", "member.role", "monitor.create", "project.create", "org.create"}
	if strings.Join(actions, ",") != strings.Join(want, ",") {
		t.Fatalf("actions newest first:\n got %v\nwant %v", actions, want)
	}
	by := map[string]AuditEntry{} // the newest row per action and target
	for _, e := range entries {
		if _, seen := by[e.Action+"/"+e.Target]; !seen {
			by[e.Action+"/"+e.Target] = e
		}
	}
	created := by["monitor.create/api"]
	if created.Actor != "j" || created.ActorKind != audit.KindUser || created.Via != "web" || created.RequestID != "01REQ" || created.Remote != "10.0.4.12" || created.ProjectID != f.project.ID || created.OrgID != f.org.ID || created.TargetID != m.ID || !strings.Contains(created.After, "kind: http") || created.Before != "" {
		t.Fatalf("monitor.create: %+v", created)
	}
	upd := by["monitor.update/api"]
	// the default interval is left out of the snapshot, so the diff shows the new line alone
	if strings.Contains(upd.Before, "interval") || !strings.Contains(upd.After, "interval: 30s") || upd.Detail["fields"] == nil || !strings.Contains(strings.Join(anyStrings(upd.Detail["fields"]), ","), "interval") {
		t.Fatalf("monitor.update: %+v", upd)
	}
	chRow := by["channel.create/ops"]
	if chRow.TargetID != ch.ID || !strings.Contains(chRow.After, "***") || strings.Contains(chRow.After, "s3cret") {
		t.Fatalf("channel secret must be masked: %q", chRow.After)
	}
	if k := by["key.create/deploy"]; k.Detail["access"] != "ro" || k.Detail["prefix"] == "" {
		t.Fatalf("key.create: %+v", k)
	}
	if r := by["member.role/"]; r.Action != "" {
		t.Fatalf("member.role must name the subject")
	}
	var role AuditEntry
	for _, e := range entries {
		if e.Action == "member.role" {
			role = e // the newest: member to admin
			break
		}
	}
	if role.OrgID != f.org.ID || role.ProjectID != "" || role.Detail["to"] != "admin" || role.Detail["from"] != "member" || role.Actor != "anne" {
		t.Fatalf("member.role: %+v", role)
	}
	ap := by["apply/vink.yaml"]
	if ap.Detail["created"] != float64(1) || ap.Detail["updated"] != float64(1) {
		t.Fatalf("apply counts: %+v", ap.Detail)
	}
	st := by["state.up/nightly"]
	if st.Source != "event" || st.ActorKind != audit.KindSystem || st.Actor != "vink" || st.Via != "ping" || st.Detail["from"] != "new" {
		t.Fatalf("state row: %+v", st)
	}

	// kinds, actor and project filters; counts for the chips
	if got := f.auditAll(t, orgAdmin, AuditFilter{Access: true}); len(got) != 3 || got[0].Action != "member.role" || got[1].Action != "key.create" || got[2].Action != "member.role" {
		t.Fatalf("access filter: %+v", got)
	}
	if got := f.auditAll(t, orgAdmin, AuditFilter{State: true}); len(got) != 1 || got[0].Action != "state.up" {
		t.Fatalf("state filter: %+v", got)
	}
	if got := f.auditAll(t, orgAdmin, AuditFilter{Actor: "anne"}); len(got) != 2 {
		t.Fatalf("actor filter: %+v", got)
	}
	if got := f.auditAll(t, orgAdmin, AuditFilter{ProjectID: f.project.ID}); len(got) != 9 {
		t.Fatalf("project filter: %d", len(got))
	}
	counts, err := f.svc.AuditCounts(ctx, orgAdmin, AuditFilter{})
	if err != nil || counts.Changes != 8 || counts.Access != 3 || counts.State != 1 {
		t.Fatalf("counts: %+v %v", counts, err)
	}
	actors, _ := f.svc.AuditActors(ctx, orgAdmin, AuditFilter{})
	if strings.Join(actors, ",") != "anne,j,test:admin" {
		t.Fatalf("actors: %v", actors)
	}

	// a member sees project rows only; a key sees nothing; another org sees nothing of this one
	if got := f.auditAll(t, member, AuditFilter{}); len(got) != 9 {
		t.Fatalf("member view: %d", len(got))
	}
	for _, e := range f.auditAll(t, member, AuditFilter{}) {
		if e.ProjectID == "" {
			t.Fatalf("member must not see org rows: %+v", e)
		}
	}
	key := domain.Scope{OrgID: f.org.ID, ProjectID: f.project.ID, Role: domain.RoleAdmin, KeyID: "k", KeyAccess: domain.AccessRW, Actor: "key:abc"}
	if _, err := f.svc.AuditLog(ctx, key, AuditFilter{}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("key: %v", err)
	}
	other, _ := f.svc.CreateOrg(ctx, f.admin, "acme", "Acme")
	otherAdmin := domain.Scope{OrgID: other.ID, UserID: "u9", Role: domain.RoleOwner, Actor: "user:o"}
	// acme sees its own creation and nothing of homelab, even when it asks
	if got := f.auditAll(t, otherAdmin, AuditFilter{}); len(got) != 1 || got[0].Action != "org.create" || got[0].Target != "acme" {
		t.Fatalf("another org must see only its own rows: %+v", got)
	}
	if got := f.auditAll(t, otherAdmin, AuditFilter{OrgID: f.org.ID}); len(got) != 1 || got[0].OrgID != other.ID {
		t.Fatalf("asking for another org must not help: %+v", got)
	}
	if _, err := f.svc.AuditLog(ctx, otherAdmin, AuditFilter{ProjectID: f.project.ID}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("another org's project: %v", err)
	}
	// the instance admin sees every org, org.create included
	all := f.auditAll(t, f.admin, AuditFilter{})
	if len(all) < 13 || all[0].Action != "org.create" || all[0].Target != "acme" || all[0].OrgID != other.ID {
		t.Fatalf("instance view: %d %+v", len(all), all[0])
	}

	// pages: 50 rows, then a cursor to the rest
	for i := range 55 {
		if _, err := f.svc.CreateMonitor(ctx, member, &domain.Monitor{Slug: "m" + string(rune('a'+i%26)) + string(rune('a'+i/26)), Kind: domain.KindHeartbeat, Heartbeat: &domain.HeartbeatSpec{Schedule: domain.Schedule{Period: domain.MustDuration("1h")}}}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := f.svc.AuditLog(ctx, orgAdmin, AuditFilter{})
	if err != nil || len(first.Entries) != 50 || !first.More || first.NextID == "" {
		t.Fatalf("first page: %d more=%v", len(first.Entries), first.More)
	}
	second, err := f.svc.AuditLog(ctx, orgAdmin, AuditFilter{BeforeAt: first.NextAt, BeforeID: first.NextID})
	if err != nil || len(second.Entries) != 17 || second.More {
		t.Fatalf("second page: %d more=%v", len(second.Entries), second.More)
	}
	if second.Entries[0].ID >= first.Entries[49].ID {
		t.Fatal("pages must not overlap")
	}
	// the period: nothing older than since
	if got := f.auditAll(t, orgAdmin, AuditFilter{Since: f.clock.Now().Add(time.Second)}); len(got) != 0 {
		t.Fatalf("since: %d", len(got))
	}
}

func anyStrings(v any) []string {
	raw, _ := v.([]any)
	out := make([]string, 0, len(raw))
	for _, s := range raw {
		out = append(out, s.(string))
	}
	return out
}

func TestSignInsAreAudited(t *testing.T) {
	f := newFixture(t)
	ctx := audit.WithRequest(context.Background(), audit.Request{Via: audit.ViaWeb, RemoteAddr: "203.0.113.9"})
	u, err := f.svc.CreateLocalUser(ctx, f.admin, "bob", "", "", "correct horse", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.RecordSignIn(ctx, nil, "bob", "password", false); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.RecordSignIn(ctx, u, "bob", "password", true); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.RecordSignOut(ctx, u); err != nil {
		t.Fatal(err)
	}
	all := f.auditAll(t, f.admin, AuditFilter{Access: true})
	if len(all) != 4 || all[0].Action != "user.signout" || all[1].Action != "user.signin" || all[1].Detail["method"] != "password" || all[2].Action != "user.signin_failed" || all[2].ActorID != "" || all[2].Actor != "bob" || all[3].Action != "user.create" || all[3].Actor != "test:admin" {
		t.Fatalf("sign-in rows: %+v", all)
	}
	if all[1].Remote != "203.0.113.9" || all[1].Via != "web" {
		t.Fatalf("request facts: %+v", all[1])
	}
}
