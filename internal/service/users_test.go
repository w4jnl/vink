package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/w4jnl/vink/internal/domain"
)

func TestLocalUsersAndPasswords(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	u, err := f.svc.CreateLocalUser(ctx, f.admin, "j", "j@example.com", "", "correct horse", false)
	if err != nil || u.Subject != "j" || u.DisplayName != "j" || !u.HasPassword {
		t.Fatalf("create: %+v %v", u, err)
	}
	if _, err := f.svc.CreateLocalUser(ctx, f.admin, "j", "", "", "correct horse", false); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate: %v", err)
	}
	if _, err := f.svc.CreateLocalUser(ctx, f.member, "x", "", "", "correct horse", false); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("member creating user: %v", err)
	}
	for _, bad := range []struct{ subject, pw string }{{"", "correct horse"}, {"has space", "correct horse"}, {"ok", "short"}} {
		if _, err := f.svc.CreateLocalUser(ctx, f.admin, bad.subject, "", "", bad.pw, false); err == nil {
			t.Errorf("expected validation error for %+v", bad)
		}
	}
	if got, err := f.svc.VerifyPassword(ctx, "j", "correct horse"); err != nil || got.ID != u.ID {
		t.Fatalf("verify: %v", err)
	}
	if _, err := f.svc.VerifyPassword(ctx, "j", "wrong"); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("wrong password: %v", err)
	}
	if _, err := f.svc.VerifyPassword(ctx, "nobody", "x"); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("unknown user: %v", err)
	}
	if err := f.svc.SetPassword(ctx, domain.Scope{UserID: u.ID}, u.ID, "new password!"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.VerifyPassword(ctx, "j", "new password!"); err != nil {
		t.Fatal("new password rejected")
	}
	if err := f.svc.SetPassword(ctx, domain.Scope{UserID: "other"}, u.ID, "new password!"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("other user setting password: %v", err)
	}
	if err := f.svc.SetInstanceAdmin(ctx, f.admin, "j", true); err != nil {
		t.Fatal(err)
	}
	users, _ := f.svc.ListUsers(ctx, f.admin)
	if len(users) != 1 || !users[0].InstanceAdmin {
		t.Fatalf("list: %+v", users)
	}
}

func TestProxyUsersAndHeaderMemberships(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	u, err := f.svc.EnsureProxyUser(ctx, "alice", "a@example.com", "Alice")
	if err != nil || u.HasPassword || u.Email != "a@example.com" {
		t.Fatalf("ensure: %+v %v", u, err)
	}
	again, _ := f.svc.EnsureProxyUser(ctx, "alice", "alice@example.com", "")
	if again.ID != u.ID || again.Email != "alice@example.com" || again.DisplayName != "Alice" {
		t.Fatalf("profile update: %+v", again)
	}
	second, _ := f.svc.CreateOrg(ctx, f.admin, "second", "Second")
	// a local membership survives header syncs
	if err := f.svc.SetMembership(ctx, f.admin, u.ID, second.ID, domain.RoleOwner); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.SyncHeaderMemberships(ctx, u.ID, map[string]domain.Role{"homelab": domain.RoleMember, "ghost": domain.RoleAdmin}); err != nil {
		t.Fatal(err)
	}
	ms, _ := f.svc.MembershipsForUser(ctx, u.ID)
	if len(ms) != 2 {
		t.Fatalf("memberships: %+v", ms)
	}
	for _, m := range ms {
		switch m.OrgSlug {
		case "homelab":
			if m.Role != domain.RoleMember || m.Source != "header" {
				t.Errorf("homelab: %+v", m)
			}
		case "second":
			if m.Role != domain.RoleOwner || m.Source != "local" {
				t.Errorf("second: %+v", m)
			}
		}
	}
	// role change is applied, removal prunes
	if err := f.svc.SyncHeaderMemberships(ctx, u.ID, map[string]domain.Role{"homelab": domain.RoleAdmin}); err != nil {
		t.Fatal(err)
	}
	ms, _ = f.svc.MembershipsForUser(ctx, u.ID)
	for _, m := range ms {
		if m.OrgSlug == "homelab" && m.Role != domain.RoleAdmin {
			t.Errorf("role not updated: %+v", m)
		}
	}
	if err := f.svc.SyncHeaderMemberships(ctx, u.ID, nil); err != nil {
		t.Fatal(err)
	}
	ms, _ = f.svc.MembershipsForUser(ctx, u.ID)
	if len(ms) != 1 || ms[0].OrgSlug != "second" {
		t.Fatalf("after prune: %+v", ms)
	}
	if err := f.svc.SetMembership(ctx, f.member, u.ID, second.ID, domain.RoleAdmin); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("member granting role: %v", err)
	}
	if err := f.svc.SetMembership(ctx, f.admin, u.ID, second.ID, "king"); err == nil {
		t.Fatal("bad role accepted")
	}
}

func TestAPIKeys(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	admin := f.member
	admin.Role = domain.RoleAdmin
	if _, _, err := f.svc.CreateAPIKey(ctx, f.member, "rw by member", domain.AccessRW); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("member creating rw: %v", err)
	}
	if _, _, err := f.svc.CreateAPIKey(ctx, f.viewer, "ro by viewer", domain.AccessRO); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("viewer creating ro: %v", err)
	}
	ro, roPlain, err := f.svc.CreateAPIKey(ctx, f.member, "ci", domain.AccessRO)
	if err != nil {
		t.Fatal(err)
	}
	rw, rwPlain, err := f.svc.CreateAPIKey(ctx, admin, "deploy", domain.AccessRW)
	if err != nil {
		t.Fatal(err)
	}
	if ro.Access != domain.AccessRO || rw.Access != domain.AccessRW || len(ro.Prefix) != 8 {
		t.Fatalf("keys: %+v %+v", ro, rw)
	}
	got, err := f.svc.VerifyAPIKey(ctx, rwPlain)
	if err != nil || got.ID != rw.ID || got.LastUsedAt == nil {
		t.Fatalf("verify rw: %+v %v", got, err)
	}
	// cached second verification
	if got2, err := f.svc.VerifyAPIKey(ctx, rwPlain); err != nil || got2.ID != rw.ID {
		t.Fatalf("cached verify: %v", err)
	}
	for _, bad := range []string{"", "vk_bad", rwPlain + "x", "vk_" + rw.Prefix + "_" + "wrongwrongwrongwrongwrongwrongww"} {
		if _, err := f.svc.VerifyAPIKey(ctx, bad); !errors.Is(err, domain.ErrUnauthorized) {
			t.Errorf("VerifyAPIKey(%q) = %v", bad, err)
		}
	}
	keys, _ := f.svc.ListAPIKeys(ctx, f.member)
	if len(keys) != 2 {
		t.Fatalf("list: %d", len(keys))
	}
	if err := f.svc.RevokeAPIKey(ctx, f.member, rw.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("member revoking rw: %v", err)
	}
	if err := f.svc.RevokeAPIKey(ctx, admin, rw.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.VerifyAPIKey(ctx, rwPlain); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatal("revoked key still verifies")
	}
	if err := f.svc.RevokeAPIKey(ctx, admin, rw.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("revoke twice: %v", err)
	}
	if _, err := f.svc.VerifyAPIKey(ctx, roPlain); err != nil {
		t.Fatal("ro key must still verify")
	}
	other := admin
	other.ProjectID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	if err := f.svc.RevokeAPIKey(ctx, other, ro.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cross-project revoke: %v", err)
	}
}

func TestSessions(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	u, _ := f.svc.CreateLocalUser(ctx, f.admin, "j", "", "", "correct horse", false)
	sess, err := f.svc.CreateSession(ctx, u.ID)
	if err != nil || len(sess.ID) != 43 || len(sess.CSRF) != 43 {
		t.Fatalf("create: %+v %v", sess, err)
	}
	got, err := f.svc.Session(ctx, sess.ID)
	if err != nil || got.UserID != u.ID || got.CSRF != sess.CSRF {
		t.Fatalf("load: %+v %v", got, err)
	}
	if err := f.svc.SetSessionProject(ctx, sess.ID, f.project.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = f.svc.Session(ctx, sess.ID)
	if got.LastProjectID != f.project.ID {
		t.Error("last project not stored")
	}
	// sliding: after two days the expiry moves
	f.clock.Add(48 * time.Hour)
	got, _ = f.svc.Session(ctx, sess.ID)
	if !got.ExpiresAt.Equal(f.clock.Now().Add(SessionTTL)) {
		t.Errorf("expiry not extended: %v", got.ExpiresAt)
	}
	f.clock.Add(31 * 24 * time.Hour)
	if _, err := f.svc.Session(ctx, sess.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expired session: %v", err)
	}
	if n, _ := f.svc.DeleteExpiredSessions(ctx); n != 1 {
		t.Errorf("expired cleanup removed %d", n)
	}
	s2, _ := f.svc.CreateSession(ctx, u.ID)
	if err := f.svc.DeleteSession(ctx, s2.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Session(ctx, s2.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("deleted session still loads")
	}
}

func TestBootstrap(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	// the fixture created an org and project but no user, so bootstrap is allowed
	res, err := f.svc.Bootstrap(ctx, BootstrapInput{OrgSlug: "boot", Subject: "root", Password: "rootpassword", Timezone: "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.User.InstanceAdmin || res.Project.Slug != "boot" || res.APIKey.Access != domain.AccessRW || res.APIKeyPlain == "" {
		t.Fatalf("bootstrap: %+v", res)
	}
	ms, _ := f.svc.MembershipsForUser(ctx, res.User.ID)
	if len(ms) != 1 || ms[0].Role != domain.RoleOwner {
		t.Fatalf("owner membership: %+v", ms)
	}
	if _, err := f.svc.Bootstrap(ctx, BootstrapInput{OrgSlug: "again", Subject: "x", Password: "xpasswordxx"}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("second bootstrap: %v", err)
	}
}

func TestMembershipsKeepTheLastOwner(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	alice, err := f.svc.CreateLocalUser(ctx, f.admin, "alice", "", "", "correct horse", false)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := f.svc.CreateLocalUser(ctx, f.admin, "bob", "", "", "correct horse", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.SetMembership(ctx, f.admin, alice.ID, f.org.ID, domain.RoleOwner); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.SetMembership(ctx, f.admin, bob.ID, f.org.ID, domain.RoleMember); err != nil {
		t.Fatal(err)
	}
	// alice is the only owner: she can neither be demoted nor removed
	if err := f.svc.SetMembership(ctx, f.admin, alice.ID, f.org.ID, domain.RoleAdmin); err == nil || !strings.Contains(err.Error(), "last owner") {
		t.Fatalf("demote last owner: %v", err)
	}
	if err := f.svc.RemoveMembership(ctx, f.admin, alice.ID, f.org.ID); err == nil || !strings.Contains(err.Error(), "last owner") {
		t.Fatalf("remove last owner: %v", err)
	}
	// a second owner frees her
	if err := f.svc.SetMembership(ctx, f.admin, bob.ID, f.org.ID, domain.RoleOwner); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.SetMembership(ctx, f.admin, alice.ID, f.org.ID, domain.RoleViewer); err != nil {
		t.Fatalf("demote with another owner: %v", err)
	}
	if err := f.svc.RemoveMembership(ctx, f.admin, alice.ID, f.org.ID); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := f.svc.RemoveMembership(ctx, f.admin, alice.ID, f.org.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("remove twice: %v", err)
	}
	if ms, _ := f.svc.MembershipsForUser(ctx, alice.ID); len(ms) != 0 {
		t.Fatalf("alice still has memberships: %+v", ms)
	}
	// only org admins of that org or instance admins may
	if err := f.svc.RemoveMembership(ctx, f.member, bob.ID, f.org.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("member removing: %v", err)
	}
}
