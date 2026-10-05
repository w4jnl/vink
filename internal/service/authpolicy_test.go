package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/w4jnl/vink/internal/domain"
)

func proxyPolicy(roles string, admins ...string) AuthPolicy {
	return AuthPolicy{
		Proxy: ProviderPolicy{Enabled: true, Roles: roles, InstanceAdmins: admins, StripRealm: true, Lowercase: true, AdminGroup: "vink:admin"},
		OIDC:  ProviderPolicy{Roles: RolesGroups},
	}
}

func (f *fixture) auditCount(t *testing.T, act string) int {
	t.Helper()
	var n int
	if err := f.svc.DB().Reader.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM audit WHERE act = ?`, act).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestRoleSourceTakeover: switching the proxy to roles set in vink turns
// its group-derived roles into ordinary ones once, audited; a second start
// does nothing; switching back keeps them; another process on the same
// database sees the policy the server applied.
func TestRoleSourceTakeover(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	alice, _ := f.svc.EnsureProxyUser(ctx, "alice", "", "")
	if err := f.svc.SyncHeaderMemberships(ctx, alice.ID, map[string]domain.Role{"homelab": domain.RoleAdmin}); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.ApplyAuthPolicy(ctx, proxyPolicy(RolesGroups)); err != nil {
		t.Fatal(err)
	}
	if n := f.auditCount(t, "auth.roles"); n != 0 {
		t.Fatalf("groups to groups audited %d times", n)
	}
	if err := f.svc.ApplyAuthPolicy(ctx, proxyPolicy(RolesVink)); err != nil {
		t.Fatal(err)
	}
	ms, _ := f.svc.MembershipsForUser(ctx, alice.ID)
	if len(ms) != 1 || ms[0].Source != "local" || ms[0].Role != domain.RoleAdmin {
		t.Fatalf("after the switch: %+v", ms)
	}
	if err := f.svc.ApplyAuthPolicy(ctx, proxyPolicy(RolesVink)); err != nil {
		t.Fatal(err)
	}
	if n := f.auditCount(t, "auth.roles"); n != 1 {
		t.Fatalf("the takeover is audited once, got %d", n)
	}
	// another process on the same database (vink admin with --db)
	other := New(f.svc.DB(), nil, slog.New(slog.NewTextHandler(io.Discard, nil)), DefaultConfig())
	if p := other.AuthPolicy(ctx); p.Proxy.Roles != RolesVink || !p.Proxy.Enabled {
		t.Fatalf("saved policy: %+v", p)
	}
	// back to groups: the roles set in vink stay
	if err := f.svc.ApplyAuthPolicy(ctx, proxyPolicy(RolesGroups)); err != nil {
		t.Fatal(err)
	}
	if ms, _ := f.svc.MembershipsForUser(ctx, alice.ID); len(ms) != 1 || ms[0].Source != "local" {
		t.Fatalf("after switching back: %+v", ms)
	}
	if n := f.auditCount(t, "auth.roles"); n != 2 {
		t.Fatalf("the switch back is audited, got %d", n)
	}
}

// TestInstanceAdminBySourceAndMode: who may change a person's instance
// admin flag in vink, by where they sign in, the mode and the list.
func TestInstanceAdminBySourceAndMode(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name    string
		policy  AuthPolicy
		source  string
		promote bool // promote first, then demote
		listed  bool
		want    string // "" allowed, else part of the error
	}{
		{"local, groups", proxyPolicy(RolesGroups), "local", true, false, ""},
		{"proxy, groups", proxyPolicy(RolesGroups), "proxy", true, false, "instance_admin_group while auth.proxy.roles is groups"},
		{"proxy, vink", proxyPolicy(RolesVink), "proxy", true, false, ""},
		{"proxy, vink, listed", proxyPolicy(RolesVink, "bob"), "proxy", false, true, "listed in auth.proxy.instance_admins"},
		{"proxy, groups, proxy off", AuthPolicy{Proxy: ProviderPolicy{Roles: RolesGroups}}, "proxy", true, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			if _, err := f.svc.CreateLocalUser(ctx, f.admin, "root", "", "", "correct horse battery", true); err != nil {
				t.Fatal(err)
			}
			if err := f.svc.ApplyAuthPolicy(ctx, tc.policy); err != nil {
				t.Fatal(err)
			}
			var bob *domain.User
			var err error
			if tc.source == "local" {
				bob, err = f.svc.CreateLocalUser(ctx, f.admin, "bob", "", "", "correct horse battery", false)
			} else {
				bob, err = f.svc.EnsureExternalUser(ctx, "bob", "", "", tc.source)
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.listed {
				if err := f.svc.SetDerivedInstanceAdmin(ctx, bob, true, "config"); err != nil {
					t.Fatal(err)
				}
			}
			err = nil
			if tc.promote {
				err = f.svc.SetInstanceAdmin(ctx, f.admin, "bob", true)
			}
			if err == nil {
				err = f.svc.SetInstanceAdmin(ctx, f.admin, "bob", false)
			}
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("refused: %v", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}

// TestLastInstanceAdmin: nobody, a key or the CLI included, demotes or
// disables the only active instance admin.
func TestLastInstanceAdmin(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	root, _ := f.svc.CreateLocalUser(ctx, f.admin, "root", "", "", "correct horse battery", true)
	if err := f.svc.SetInstanceAdmin(ctx, f.admin, "root", false); err == nil || !strings.Contains(err.Error(), "last instance admin") {
		t.Fatalf("demote the last: %v", err)
	}
	if err := f.svc.SetUserDisabled(ctx, f.admin, root.ID, true); err == nil || !strings.Contains(err.Error(), "last instance admin") {
		t.Fatalf("disable the last: %v", err)
	}
	if _, err := f.svc.CreateLocalUser(ctx, f.admin, "second", "", "", "correct horse battery", true); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.SetInstanceAdmin(ctx, f.admin, "root", false); err != nil {
		t.Fatalf("demote with another admin: %v", err)
	}
}

// TestRolesFromGroupsAreLocked: in groups mode a role the proxy's groups
// gave cannot be changed or removed in vink (the next request would undo
// it); in vink mode it can. An org admin cannot make an owner.
func TestRolesFromGroupsAreLocked(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	alice, _ := f.svc.EnsureProxyUser(ctx, "alice", "", "")
	if err := f.svc.SyncHeaderMemberships(ctx, alice.ID, map[string]domain.Role{"homelab": domain.RoleMember}); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.ApplyAuthPolicy(ctx, proxyPolicy(RolesGroups)); err != nil {
		t.Fatal(err)
	}
	orgAdmin := domain.Scope{OrgID: f.org.ID, UserID: "u0", Role: domain.RoleAdmin, Actor: "user:a"}
	if err := f.svc.SetMembership(ctx, orgAdmin, alice.ID, f.org.ID, domain.RoleAdmin); err == nil || !strings.Contains(err.Error(), "comes from the proxy’s groups") {
		t.Fatalf("change a group role: %v", err)
	}
	if err := f.svc.RemoveMembership(ctx, orgAdmin, alice.ID, f.org.ID); err == nil || !strings.Contains(err.Error(), `auth.proxy.roles = "vink"`) {
		t.Fatalf("remove a group role: %v", err)
	}
	if err := f.svc.SetMembership(ctx, orgAdmin, alice.ID, f.org.ID, domain.RoleOwner); err == nil || !strings.Contains(err.Error(), "only an owner") {
		t.Fatalf("an org admin makes an owner: %v", err)
	}
	if err := f.svc.ApplyAuthPolicy(ctx, proxyPolicy(RolesVink)); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.SetMembership(ctx, orgAdmin, alice.ID, f.org.ID, domain.RoleAdmin); err != nil {
		t.Fatalf("change in vink mode: %v", err)
	}
	if err := f.svc.RemoveMembership(ctx, orgAdmin, alice.ID, f.org.ID); err != nil {
		t.Fatalf("remove in vink mode: %v", err)
	}
}

// TestAddMemberByName: org admins add a proxy person by sign-in name in
// vink mode, before the first visit; local accounts, people whose groups
// decide, members already in, and a mode without vink are refused.
func TestAddMemberByName(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	orgAdmin := domain.Scope{OrgID: f.org.ID, UserID: "u0", Role: domain.RoleAdmin, Actor: "user:a"}
	if _, err := f.svc.AddMember(ctx, orgAdmin, "jdoe", domain.RoleMember); err == nil || !strings.Contains(err.Error(), "set to vink") {
		t.Fatalf("groups mode: %v", err)
	}
	if err := f.svc.ApplyAuthPolicy(ctx, AuthPolicy{
		Proxy: ProviderPolicy{Enabled: true, Roles: RolesVink, StripRealm: true, Lowercase: true},
		OIDC:  ProviderPolicy{Enabled: true, Roles: RolesGroups},
	}); err != nil {
		t.Fatal(err)
	}
	u, err := f.svc.AddMember(ctx, orgAdmin, "JDoe@CORP.EXAMPLE", domain.RoleMember)
	if err != nil || u.Subject != "jdoe" || u.Source != "proxy" {
		t.Fatalf("add a new name: %+v %v", u, err)
	}
	ms, _ := f.svc.MembershipsForUser(ctx, u.ID)
	if len(ms) != 1 || ms[0].Role != domain.RoleMember || ms[0].Source != "local" {
		t.Fatalf("membership: %+v", ms)
	}
	if _, err := f.svc.AddMember(ctx, orgAdmin, "jdoe", domain.RoleAdmin); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("again: %v", err)
	}
	if _, err := f.svc.CreateLocalUser(ctx, f.admin, "lou", "", "", "correct horse battery", false); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.AddMember(ctx, orgAdmin, "lou", domain.RoleMember); err == nil || !strings.Contains(err.Error(), "local account") {
		t.Fatalf("a local account: %v", err)
	}
	if _, err := f.svc.EnsureExternalUser(ctx, "olga", "", "", "oidc"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.AddMember(ctx, orgAdmin, "olga", domain.RoleMember); err == nil || !strings.Contains(err.Error(), "groups of auth.oidc") {
		t.Fatalf("an OIDC person in groups mode: %v", err)
	}
	if _, err := f.svc.AddMember(ctx, orgAdmin, "newowner", domain.RoleOwner); err == nil || !strings.Contains(err.Error(), "only an owner") {
		t.Fatalf("an owner by an admin: %v", err)
	}
	if _, err := f.svc.AddMember(ctx, f.member, "x", domain.RoleViewer); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("a member adds: %v", err)
	}
	// instance admins create provider accounts directly, normalised the same way
	p, err := f.svc.CreateProviderUser(ctx, f.admin, "ASmith@CORP", "", "", "proxy")
	if err != nil || p.Subject != "asmith" {
		t.Fatalf("provider account: %+v %v", p, err)
	}
	if _, err := f.svc.CreateProviderUser(ctx, f.admin, "asmith", "", "", "proxy"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate: %v", err)
	}
	if _, err := f.svc.CreateProviderUser(ctx, f.admin, "x", "", "", "local"); err == nil {
		t.Fatal("a local source needs a password")
	}
	if _, err := f.svc.CreateProviderUser(ctx, orgAdmin, "y", "", "", "proxy"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("an org admin: %v", err)
	}
}
