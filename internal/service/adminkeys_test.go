package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/w4jnl/vink/internal/audit"
	"github.com/w4jnl/vink/internal/domain"
)

// adminUser makes a local instance admin and the scope of their session.
func (f *fixture) adminUser(t *testing.T, subject string) (*domain.User, domain.Scope) {
	t.Helper()
	u, err := f.svc.CreateLocalUser(context.Background(), f.admin, subject, "", "", "correct horse battery", true)
	if err != nil {
		t.Fatal(err)
	}
	return u, domain.Scope{UserID: u.ID, InstanceAdmin: true, Role: domain.RoleOwner, Actor: "user:" + subject}
}

func (f *fixture) adminKey(t *testing.T, sc domain.Scope, access domain.Access) (*domain.AdminKey, string) {
	t.Helper()
	k, token, err := f.svc.CreateAdminKey(context.Background(), sc, "ci", access, 0)
	if err != nil {
		t.Fatal(err)
	}
	return k, token
}

// TestCreateAdminKey: only a signed-in instance admin or vink admin on the
// host makes one, never a key; lifetimes stay within a year.
func TestCreateAdminKey(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	_, root := f.adminUser(t, "root")
	k, _ := f.adminKey(t, root, domain.AccessRW)
	cli := domain.Scope{InstanceAdmin: true, Role: domain.RoleOwner, Actor: "cli:admin"}
	cases := []struct {
		name   string
		sc     domain.Scope
		access domain.Access
		ttl    time.Duration
		want   error
		field  string
	}{
		{"session", root, domain.AccessRW, 0, nil, ""},
		{"server host", cli, domain.AccessRO, 30 * 24 * time.Hour, nil, ""},
		{"a year", root, domain.AccessRO, 365 * 24 * time.Hour, nil, ""},
		{"not instance admin", f.member, domain.AccessRO, 0, domain.ErrForbidden, ""},
		{"admin key", AdminKeyScope(k), domain.AccessRO, 0, domain.ErrForbidden, ""},
		{"project key", domain.Scope{OrgID: f.org.ID, ProjectID: f.project.ID, Role: domain.RoleAdmin, KeyID: "k", Actor: "key:abc", InstanceAdmin: true}, domain.AccessRO, 0, domain.ErrForbidden, ""},
		{"under a day", root, domain.AccessRO, 12 * time.Hour, nil, "expires"},
		{"over a year", root, domain.AccessRO, 366 * 24 * time.Hour, nil, "expires"},
		{"bad access", root, "admin", 0, nil, "access"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, token, err := f.svc.CreateAdminKey(ctx, tc.sc, " deploy ", tc.access, tc.ttl)
			switch {
			case tc.want != nil:
				if !errors.Is(err, tc.want) {
					t.Fatalf("want %v, got %v", tc.want, err)
				}
				return
			case tc.field != "":
				ve, ok := domain.AsValidation(err)
				if !ok || ve.Errors[0].Field != tc.field {
					t.Fatalf("want a %s problem, got %v", tc.field, err)
				}
				return
			case err != nil:
				t.Fatal(err)
			}
			if !strings.HasPrefix(token, "vka_"+got.Prefix+"_") || got.Name != "deploy" || got.Access != tc.access {
				t.Fatalf("key %+v token %s", got, token)
			}
			ttl := tc.ttl
			if ttl == 0 {
				ttl = AdminKeyDefaultTTL
			}
			if !got.ExpiresAt.Equal(start.Add(ttl)) {
				t.Errorf("expires %v, want %v", got.ExpiresAt, start.Add(ttl))
			}
			if got.CreatedBy != tc.sc.UserID {
				t.Errorf("created by %q, want %q", got.CreatedBy, tc.sc.UserID)
			}
		})
	}
	if n := f.auditCount(t, "adminkey.create"); n != 4 {
		t.Errorf("adminkey.create audited %d times, want 4", n)
	}
}

// TestVerifyAdminKey: a vka_ token resolves to its key and nothing else
// does; an expired key is refused on the cached path too, saying when.
func TestVerifyAdminKey(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	_, root := f.adminUser(t, "root")
	k, token, err := f.svc.CreateAdminKey(ctx, root, "ci", domain.AccessRO, 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.svc.VerifyAdminKey(ctx, token, "192.0.2.1")
	if err != nil || got.ID != k.ID {
		t.Fatalf("verify: %+v %v", got, err)
	}
	_, ptoken, err := f.svc.CreateAPIKey(ctx, f.member, "p", domain.AccessRO)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", ptoken, strings.Replace(ptoken, "vk_", "vka_", 1), token[:len(token)-1] + "x", "vka_" + k.Prefix + "_short"} {
		if _, err := f.svc.VerifyAdminKey(ctx, bad, ""); !errors.Is(err, domain.ErrUnauthorized) {
			t.Errorf("%q: want unauthorized, got %v", bad, err)
		}
	}
	if _, err := f.svc.VerifyAPIKey(ctx, token); !errors.Is(err, domain.ErrUnauthorized) {
		t.Errorf("an admin key must not verify as a project key, got %v", err)
	}
	// still inside the cache's five minutes when it expires
	f.clock.Set(k.ExpiresAt.Add(-time.Minute))
	if _, err := f.svc.VerifyAdminKey(ctx, token, ""); err != nil {
		t.Fatalf("a minute before expiry: %v", err)
	}
	f.clock.Set(k.ExpiresAt)
	_, err = f.svc.VerifyAdminKey(ctx, token, "")
	var ke *KeyExpiredError
	if !errors.As(err, &ke) || !errors.Is(err, domain.ErrUnauthorized) || !strings.Contains(err.Error(), "expired on "+k.ExpiresAt.Format("2 Jan 2006")) {
		t.Fatalf("expired, cached: %v", err)
	}
	// and from a process that never saw it
	other := New(f.svc.DB(), nil, slog.New(slog.NewTextHandler(io.Discard, nil)), DefaultConfig())
	other.SetClock(f.clock.Now)
	if _, err := other.VerifyAdminKey(ctx, token, ""); !errors.As(err, &ke) {
		t.Fatalf("expired, cold: %v", err)
	}
}

// TestRevokeAdminKey: a revoke holds at once, in this process and in the
// server when vink admin on the host revokes; ro keys cannot revoke.
func TestRevokeAdminKey(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	_, root := f.adminUser(t, "root")
	a, atoken := f.adminKey(t, root, domain.AccessRW)
	b, btoken := f.adminKey(t, root, domain.AccessRO)
	for _, tok := range []string{atoken, btoken} {
		if _, err := f.svc.VerifyAdminKey(ctx, tok, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.svc.RevokeAdminKey(ctx, AdminKeyScope(b), a.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("an ro key revoked a key: %v", err)
	}
	if err := f.svc.RevokeAdminKey(ctx, f.member, a.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("a member revoked a key: %v", err)
	}
	if err := f.svc.RevokeAdminKey(ctx, AdminKeyScope(a), b.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.VerifyAdminKey(ctx, btoken, ""); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("revoked in-process, still verifies: %v", err)
	}
	// vink admin key revoke on the host: another Service, same database
	other := New(f.svc.DB(), nil, slog.New(slog.NewTextHandler(io.Discard, nil)), DefaultConfig())
	cli := domain.Scope{InstanceAdmin: true, Role: domain.RoleOwner, Actor: "cli:admin"}
	if err := other.RevokeAdminKey(ctx, cli, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.VerifyAdminKey(ctx, atoken, ""); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("revoked by another process, still verifies from the cache: %v", err)
	}
	if err := f.svc.RevokeAdminKey(ctx, root, a.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("revoking twice: %v", err)
	}
	if keys, _ := f.svc.ListAdminKeys(ctx, root); len(keys) != 0 {
		t.Fatalf("revoked keys listed: %d", len(keys))
	}
	if n := f.auditCount(t, "adminkey.revoke"); n != 2 {
		t.Errorf("adminkey.revoke audited %d times, want 2", n)
	}
}

// TestAdminKeyTouch: use is recorded once a minute, or at once from a new
// address.
func TestAdminKeyTouch(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	_, root := f.adminUser(t, "root")
	k, token := f.adminKey(t, root, domain.AccessRO)
	last := func() (time.Time, string) {
		t.Helper()
		keys, err := f.svc.ListAdminKeys(ctx, root)
		if err != nil || len(keys) != 1 || keys[0].ID != k.ID {
			t.Fatalf("list: %v %v", keys, err)
		}
		if keys[0].LastUsedAt == nil {
			return time.Time{}, keys[0].LastUsedIP
		}
		return *keys[0].LastUsedAt, keys[0].LastUsedIP
	}
	steps := []struct {
		after  time.Duration
		ip     string
		wantAt time.Duration
		wantIP string
	}{
		{0, "192.0.2.1", 0, "192.0.2.1"},
		{30 * time.Second, "192.0.2.1", 0, "192.0.2.1"},
		{40 * time.Second, "192.0.2.9", 40 * time.Second, "192.0.2.9"},
		{2 * time.Minute, "192.0.2.9", 2 * time.Minute, "192.0.2.9"},
	}
	for _, st := range steps {
		f.clock.Set(start.Add(st.after))
		if _, err := f.svc.VerifyAdminKey(ctx, token, st.ip); err != nil {
			t.Fatal(err)
		}
		at, ip := last()
		if !at.Equal(start.Add(st.wantAt)) || ip != st.wantIP {
			t.Errorf("after %v from %s: last used %v from %s, want %v from %s", st.after, st.ip, at, ip, start.Add(st.wantAt), st.wantIP)
		}
	}
}

// TestAdminKeysGoWithTheirCreator: a creator who stops being instance
// admin, by hand or by the groups, or is disabled, takes their keys along;
// the keys of others stay.
func TestAdminKeysGoWithTheirCreator(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	_, root := f.adminUser(t, "root")
	keep, keepToken := f.adminKey(t, root, domain.AccessRW)
	_, alice := f.adminUser(t, "alice")
	_, aliceToken := f.adminKey(t, alice, domain.AccessRW)
	bob, bobScope := f.adminUser(t, "bob")
	_, bobToken := f.adminKey(t, bobScope, domain.AccessRW)
	if err := f.svc.ApplyAuthPolicy(ctx, proxyPolicy(RolesGroups)); err != nil {
		t.Fatal(err)
	}
	carol, err := f.svc.EnsureExternalUser(ctx, "carol", "", "", "proxy")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.SetDerivedInstanceAdmin(ctx, carol, true, "group"); err != nil {
		t.Fatal(err)
	}
	_, carolToken := f.adminKey(t, domain.Scope{UserID: carol.ID, InstanceAdmin: true, Role: domain.RoleOwner, Actor: "user:carol"}, domain.AccessRO)
	for _, tok := range []string{aliceToken, bobToken, carolToken} {
		if _, err := f.svc.VerifyAdminKey(ctx, tok, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.svc.SetInstanceAdmin(ctx, root, "alice", false); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.SetUserDisabled(ctx, root, bob.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.SetDerivedInstanceAdmin(ctx, carol, false, "group"); err != nil {
		t.Fatal(err)
	}
	for name, tok := range map[string]string{"demoted": aliceToken, "disabled": bobToken, "left the group": carolToken} {
		if _, err := f.svc.VerifyAdminKey(ctx, tok, ""); !errors.Is(err, domain.ErrUnauthorized) {
			t.Errorf("%s: the key still verifies: %v", name, err)
		}
	}
	if _, err := f.svc.VerifyAdminKey(ctx, keepToken, ""); err != nil {
		t.Fatalf("root's key went too: %v", err)
	}
	if keys, _ := f.svc.ListAdminKeys(ctx, root); len(keys) != 1 || keys[0].ID != keep.ID {
		t.Fatalf("keys left: %+v", keys)
	}
	var reasons []string
	rows, err := f.svc.DB().Reader.QueryContext(ctx, `SELECT json_extract(detail, '$.reason') FROM audit WHERE act = 'adminkey.revoke' ORDER BY at, rowid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			t.Fatal(err)
		}
		reasons = append(reasons, r)
	}
	want := []string{"creator is no longer instance admin", "creator was disabled", "creator is no longer instance admin"}
	if strings.Join(reasons, "|") != strings.Join(want, "|") {
		t.Errorf("reasons %q, want %q", reasons, want)
	}
	var by string
	if err := f.svc.DB().Reader.QueryRowContext(ctx, `SELECT disabled_by FROM users WHERE id = ?`, bob.ID).Scan(&by); err != nil || by != "root" {
		t.Errorf("disabled_by %q %v", by, err)
	}
}

// TestAdminKeyLimits: what an admin key may do beyond a person's session,
// and what it may not: it makes agents, but resets nobody who is instance
// admin; its audit rows name it.
func TestAdminKeyLimits(t *testing.T) {
	f := newFixture(t)
	ctx := audit.WithRequest(context.Background(), audit.Request{Via: audit.ViaAPI})
	_, root := f.adminUser(t, "root")
	k, _ := f.adminKey(t, root, domain.AccessRW)
	sc := AdminKeyScope(k)
	if sc.IsOrgKey() || !sc.IsAdminKey() || !sc.IsKey() {
		t.Fatalf("scope kinds: org %v admin %v key %v", sc.IsOrgKey(), sc.IsAdminKey(), sc.IsKey())
	}
	plain, err := f.svc.CreateLocalUser(ctx, root, "plain", "", "", "correct horse battery", false)
	if err != nil {
		t.Fatal(err)
	}
	other, _ := f.adminUser(t, "other")
	if _, _, err := f.svc.CreateResetLink(ctx, sc, other.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Errorf("reset link for an instance admin: %v", err)
	}
	if err := f.svc.ResetTOTP(ctx, sc, other.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Errorf("two-factor reset for an instance admin: %v", err)
	}
	if _, _, err := f.svc.CreateResetLink(ctx, sc, plain.ID); err != nil {
		t.Errorf("reset link for a plain account: %v", err)
	}
	if err := f.svc.ResetTOTP(ctx, sc, plain.ID); err != nil {
		t.Errorf("two-factor reset for a plain account: %v", err)
	}
	if _, _, err := f.svc.CreateResetLink(ctx, root, other.ID); err != nil {
		t.Errorf("a session may reset an instance admin: %v", err)
	}
	orgSc := sc
	orgSc.OrgID = f.org.ID
	if _, _, err := f.svc.CreateAgent(ctx, orgSc, "edge", nil); err != nil {
		t.Fatalf("admin key making an agent: %v", err)
	}
	orgKey := domain.Scope{OrgID: f.org.ID, Role: domain.RoleAdmin, KeyID: "k", Actor: "key:abc"}
	if _, _, err := f.svc.CreateAgent(ctx, orgKey, "edge2", nil); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("org key making an agent: %v", err)
	}
	var actor, kind, via string
	if err := f.svc.DB().Reader.QueryRowContext(ctx, `SELECT actor, actor_kind, via FROM audit WHERE act = 'agent.create'`).Scan(&actor, &kind, &via); err != nil {
		t.Fatal(err)
	}
	if actor != "ci" || kind != audit.KindKey || via != "api vka_"+k.Prefix {
		t.Errorf("audit actor %q kind %q via %q", actor, kind, via)
	}
}
