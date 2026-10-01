package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/w4jnl/vink/internal/domain"
)

func TestInstanceOrgs(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	marit, _ := f.svc.CreateLocalUser(ctx, f.admin, "marit", "marit@acme.example", "Marit", "correct horse", false)
	fifty, two := int64(50), int64(2)

	// only instance admins; an unknown owner is a field error
	if _, err := f.svc.CreateOrgWithOwner(ctx, f.member, "acme", "Acme", "", nil, nil); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("member creating an org: %v", err)
	}
	if _, err := f.svc.CreateOrgWithOwner(ctx, f.admin, "acme", "Acme", "nobody", nil, nil); err == nil || !strings.Contains(err.Error(), "no user named nobody") {
		t.Fatalf("unknown owner: %v", err)
	}
	org, err := f.svc.CreateOrgWithOwner(ctx, f.admin, "acme", "Acme", "marit", &fifty, &two)
	if err != nil || org.QuotaMonitors == nil || *org.QuotaMonitors != 50 || org.QuotaAgents == nil || *org.QuotaAgents != 2 {
		t.Fatalf("create with quota: %+v %v", org, err)
	}
	ms, _ := f.svc.ListMembers(ctx, domain.Scope{OrgID: org.ID, InstanceAdmin: true, Role: domain.RoleOwner})
	if len(ms) != 1 || ms[0].UserID != marit.ID || ms[0].Role != domain.RoleOwner {
		t.Fatalf("first owner: %+v", ms)
	}

	// summaries count projects, owners, monitors and agents per org
	sums, err := f.svc.OrgSummaries(ctx, f.admin)
	if err != nil || len(sums) != 2 {
		t.Fatalf("summaries: %+v %v", sums, err)
	}
	byslug := map[string]OrgSummary{}
	for _, s := range sums {
		byslug[s.Org.Slug] = s
	}
	if a := byslug["acme"]; a.Projects != 0 || len(a.Owners) != 1 || a.Owners[0] != "marit" || a.Monitors != 0 {
		t.Fatalf("acme summary: %+v", a)
	}
	f.heartbeat(t, "nightly", "1h", "5m")
	sums, _ = f.svc.OrgSummaries(ctx, f.admin)
	for _, s := range sums {
		if s.Org.Slug == "homelab" && (s.Projects != 1 || s.Monitors != 1 || len(s.Owners) != 0) {
			t.Fatalf("homelab summary: %+v", s)
		}
	}
	if _, err := f.svc.OrgSummaries(ctx, f.member); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("member summaries: %v", err)
	}

	// update: a changed quota is one org.update row, an unchanged save none
	before, _ := f.svc.AuditCounts(ctx, f.admin, AuditFilter{OrgID: org.ID})
	ten := int64(10)
	got, err := f.svc.UpdateOrg(ctx, f.admin, org.ID, "Acme Labs", &ten, nil)
	if err != nil || got.Name != "Acme Labs" || *got.QuotaMonitors != 10 || got.QuotaAgents != nil {
		t.Fatalf("update: %+v %v", got, err)
	}
	if _, err := f.svc.UpdateOrg(ctx, f.admin, org.ID, "Acme Labs", &ten, nil); err != nil {
		t.Fatal(err)
	}
	after, _ := f.svc.AuditCounts(ctx, f.admin, AuditFilter{OrgID: org.ID})
	if after.Changes != before.Changes+1 {
		t.Fatalf("org.update rows: %d -> %d", before.Changes, after.Changes)
	}
	page, _ := f.svc.AuditLog(ctx, f.admin, AuditFilter{OrgID: org.ID, Changes: true})
	if len(page.Entries) == 0 || page.Entries[0].Action != "org.update" || !strings.Contains(strings.Join(anyStrings(page.Entries[0].Detail["fields"]), ","), "quota_monitors") {
		t.Fatalf("org.update entry: %+v", page.Entries)
	}
	if got, err := f.svc.UpdateOrg(ctx, f.admin, org.ID, "", nil, nil); err != nil || got.Name != "acme" {
		t.Fatalf("empty name falls back to the slug: %+v %v", got, err)
	}
}

func TestInstanceUsers(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	root, _ := f.svc.CreateLocalUser(ctx, f.admin, "root", "", "Root", "correct horse", true)
	rootSc := domain.Scope{InstanceAdmin: true, UserID: root.ID, Role: domain.RoleOwner, Actor: "user:root"}
	bob, _ := f.svc.CreateLocalUser(ctx, f.admin, "bob", "bob@example.com", "Bob", "correct horse", false)
	_ = f.svc.SetMembership(ctx, f.admin, bob.ID, f.org.ID, domain.RoleMember)
	proxied, _ := f.svc.EnsureProxyUser(ctx, "anne", "anne@example.com", "Anne")

	users, err := f.svc.ListInstanceUsers(ctx, rootSc)
	if err != nil || len(users) != 3 {
		t.Fatalf("list: %d %v", len(users), err)
	}
	var bobRow InstanceUser
	for _, u := range users {
		if u.ID == bob.ID {
			bobRow = u
		}
	}
	if len(bobRow.Memberships) != 1 || bobRow.Memberships[0].OrgSlug != "homelab" || bobRow.Memberships[0].Role != domain.RoleMember || bobRow.LastSeenAt != nil {
		t.Fatalf("bob row: %+v", bobRow)
	}
	if _, err := f.svc.ListInstanceUsers(ctx, f.member); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("member listing: %v", err)
	}

	// disabling signs out everywhere and is refused for yourself
	sess, _ := f.svc.CreateSessionWith(ctx, bob.ID, "203.0.113.9", "curl")
	if err := f.svc.SetUserDisabled(ctx, rootSc, root.ID, true); err == nil || !strings.Contains(err.Error(), "cannot disable yourself") {
		t.Fatalf("self disable: %v", err)
	}
	if err := f.svc.SetUserDisabled(ctx, rootSc, bob.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Session(ctx, sess.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("session after disable: %v", err)
	}
	u, _ := f.svc.UserByID(ctx, bob.ID)
	if !u.Disabled() || u.DisabledBy != "root" {
		t.Fatalf("disabled: %+v", u)
	}
	if err := f.svc.SetUserDisabled(ctx, rootSc, bob.ID, false); err != nil {
		t.Fatal(err)
	}
	if u, _ = f.svc.UserByID(ctx, bob.ID); u.Disabled() {
		t.Fatal("still disabled")
	}

	// reset links: local only, a day, once
	if _, _, err := f.svc.CreateResetLink(ctx, rootSc, proxied.ID); err == nil || !strings.Contains(err.Error(), "only local accounts") {
		t.Fatalf("proxy reset link: %v", err)
	}
	if _, _, err := f.svc.CreateResetLink(ctx, f.member, bob.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("member reset link: %v", err)
	}
	token, expires, err := f.svc.CreateResetLink(ctx, rootSc, bob.ID)
	if err != nil || !strings.HasPrefix(token, "rs_") || expires.Sub(f.clock.Now()) != ResetLinkTTL {
		t.Fatalf("reset link: %s %v %v", token, expires, err)
	}
	link, err := f.svc.ResetLinkByToken(ctx, token)
	if err != nil || !link.Open || link.Subject != "bob" || link.CreatedBy != "Root" {
		t.Fatalf("by token: %+v %v", link, err)
	}
	if _, err := f.svc.ResetLinkByToken(ctx, "rs_nope"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown link: %v", err)
	}
	if _, err := f.svc.ResetPasswordByToken(ctx, token, "short"); err == nil || !strings.Contains(err.Error(), "at least") {
		t.Fatalf("short password: %v", err)
	}
	sess, _ = f.svc.CreateSessionWith(ctx, bob.ID, "203.0.113.9", "curl")
	if _, err := f.svc.ResetPasswordByToken(ctx, token, "a brand new passphrase"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.VerifyPassword(ctx, "bob", "a brand new passphrase"); err != nil {
		t.Fatalf("new password: %v", err)
	}
	if _, err := f.svc.Session(ctx, sess.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("session after reset: %v", err)
	}
	if link, _ = f.svc.ResetLinkByToken(ctx, token); link.Open {
		t.Fatal("link open after use")
	}
	if _, err := f.svc.ResetPasswordByToken(ctx, token, "another new passphrase"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("second use: %v", err)
	}
	token, _, _ = f.svc.CreateResetLink(ctx, rootSc, bob.ID)
	f.clock.Add(ResetLinkTTL + time.Minute)
	if link, _ = f.svc.ResetLinkByToken(ctx, token); link.Open {
		t.Fatal("link open after a day")
	}

	// two-factor reset is for instance admins or the account itself
	if err := f.svc.ResetTOTP(ctx, f.member, bob.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("member resetting totp: %v", err)
	}
	if err := f.svc.ResetTOTP(ctx, rootSc, bob.ID); err != nil {
		t.Fatal(err)
	}

	// it is all in the log, as the instance admin's doing
	page, err := f.svc.AuditLog(ctx, rootSc, AuditFilter{Since: start, Actor: "root"})
	if err != nil {
		t.Fatal(err)
	}
	var actions []string
	for _, e := range page.Entries {
		actions = append(actions, e.Action)
	}
	for _, want := range []string{"user.disable", "user.enable", "user.reset_link", "user.totp_reset"} {
		if !strings.Contains(strings.Join(actions, " "), want) {
			t.Errorf("missing %s in %v", want, actions)
		}
	}
}
