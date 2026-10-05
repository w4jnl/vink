package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/w4jnl/vink/internal/adminapi"
	"github.com/w4jnl/vink/internal/auth"
	"github.com/w4jnl/vink/internal/config"
	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/http/middleware"
	"github.com/w4jnl/vink/internal/service"
)

// adminEnv adds an instance admin, root, with a session and an rw and an
// ro admin key.
type adminEnv struct {
	*env
	root           *domain.User
	rootCookie     *http.Cookie
	rootCSRF       string
	rwKey, roKey   *domain.AdminKey
	rwTok, roToken string
}

func newAdminEnv(t *testing.T) *adminEnv {
	t.Helper()
	e := &adminEnv{env: newEnv(t)}
	ctx := context.Background()
	var err error
	e.root, err = e.svc.CreateLocalUser(ctx, domain.Scope{InstanceAdmin: true, Actor: "test"}, "root", "", "", "correct horse", true)
	if err != nil {
		t.Fatal(err)
	}
	rootSc := domain.Scope{UserID: e.root.ID, InstanceAdmin: true, Role: domain.RoleOwner, Actor: "user:root"}
	if e.rwKey, e.rwTok, err = e.svc.CreateAdminKey(ctx, rootSc, "ci", domain.AccessRW, 30*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if e.roKey, e.roToken, err = e.svc.CreateAdminKey(ctx, rootSc, "dash", domain.AccessRO, 0); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	p, err := e.authn.Login(rec, httptest.NewRequest("POST", "/login", nil), "root", "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	e.rootCookie, e.rootCSRF = rec.Result().Cookies()[0], p.CSRF()
	return e
}

func (e *adminEnv) rootSession(method, path string, csrf bool) resp {
	return e.do(method, Prefix+path, nil, func(r *http.Request) {
		r.AddCookie(e.rootCookie)
		if csrf {
			r.Header.Set(auth.CSRFHeader, e.rootCSRF)
		}
	})
}

// TestMeKeyKinds: /me says what kind of key the caller holds, and when an
// admin key expires.
func TestMeKeyKinds(t *testing.T) {
	e := newAdminEnv(t)
	_, orgTok, err := e.svc.CreateOrgAPIKey(context.Background(), domain.Scope{OrgID: e.org.ID, Role: domain.RoleAdmin, Actor: "test"}, "org", domain.AccessRO)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		token, kind, org string
		expires          bool
	}{
		{e.rw, "project", "homelab", false},
		{orgTok, "org", "homelab", false},
		{e.rwTok, "admin", "", true},
	} {
		var me meOut
		r := e.key(tc.token, "GET", "/me", nil)
		r.json(t, &me)
		if r.code != 200 || me.Key == nil || me.Key.Kind != tc.kind || (me.Key.ExpiresAt != nil) != tc.expires {
			t.Fatalf("%s: %d %s", tc.kind, r.code, r.body)
		}
		if (me.Org != nil && me.Org.Slug != tc.org) || (me.Org == nil && tc.org != "") {
			t.Errorf("%s: org %+v", tc.kind, me.Org)
		}
		if tc.expires && !me.Key.ExpiresAt.Equal(e.rwKey.ExpiresAt) {
			t.Errorf("expires_at %v, want %v", me.Key.ExpiresAt, e.rwKey.ExpiresAt)
		}
	}
}

// TestAdminKeysRoutes: who reaches /api/v1/admin, and what an admin key
// reaches elsewhere: nothing.
func TestAdminKeysRoutes(t *testing.T) {
	e := newAdminEnv(t)
	var list page[adminapi.AdminKey]
	r := e.key(e.rwTok, "GET", "/admin/keys", nil)
	r.json(t, &list)
	if r.code != 200 || len(list.Items) != 2 || list.Items[0].CreatedBy != "root" || list.Items[0].Key != "" {
		t.Fatalf("list: %d %s", r.code, r.body)
	}
	if strings.Contains(string(r.body), "hash") || strings.Contains(string(r.body), e.rwTok) {
		t.Fatalf("list shows secrets: %s", r.body)
	}
	cases := []struct {
		name   string
		resp   resp
		code   int
		detail string
	}{
		{"ro admin key lists", e.key(e.roToken, "GET", "/admin/keys", nil), 200, ""},
		{"ro admin key revokes", e.key(e.roToken, "DELETE", "/admin/keys/"+e.rwKey.ID, nil), 403, "read-only"},
		{"project key", e.key(e.rw, "GET", "/admin/keys", nil), 403, "instance admin key"},
		{"no credentials", e.do("GET", Prefix+"/admin/keys", nil, nil), 401, ""},
		{"member session", e.do("GET", Prefix+"/admin/keys", nil, func(r *http.Request) { r.AddCookie(e.cookie) }), 403, "instance admins only"},
		{"admin session", e.rootSession("GET", "/admin/keys", false), 200, ""},
		{"admin session, no CSRF", e.rootSession("DELETE", "/admin/keys/"+e.roKey.ID, false), 403, "CSRF"},
		{"admin key on a project route", e.key(e.rwTok, "GET", "/monitors", nil), 403, "acts only on /api/v1/admin"},
		{"admin key on an org route", e.key(e.rwTok, "GET", "/orgs/homelab/agents", nil), 403, "acts only on /api/v1/admin"},
		{"admin key on a project path", e.key(e.rwTok, "GET", "/orgs/homelab/projects/prod/monitors", nil), 403, "acts only on /api/v1/admin"},
		{"unknown key", e.key(e.rwTok, "DELETE", "/admin/keys/nope", nil), 404, ""},
	}
	for _, tc := range cases {
		if tc.resp.code != tc.code || !strings.Contains(string(tc.resp.body), tc.detail) {
			t.Errorf("%s: %d %s, want %d with %q", tc.name, tc.resp.code, tc.resp.body, tc.code, tc.detail)
		}
	}
	if r := e.rootSession("DELETE", "/admin/keys/"+e.roKey.ID, true); r.code != 204 {
		t.Fatalf("session revoke: %d %s", r.code, r.body)
	}
	if r := e.key(e.roToken, "GET", "/admin/keys", nil); r.code != 401 {
		t.Fatalf("revoked key: %d %s", r.code, r.body)
	}
	var audit struct{ Actor, Via string }
	if err := e.svc.DB().Reader.QueryRowContext(context.Background(), `SELECT actor, via FROM audit WHERE act = 'adminkey.revoke'`).Scan(&audit.Actor, &audit.Via); err != nil || audit.Actor != "root" || audit.Via != "api" {
		t.Errorf("audit %+v %v", audit, err)
	}
	// the key revokes itself, and is gone at once
	if r := e.key(e.rwTok, "DELETE", "/admin/keys/"+e.rwKey.ID, nil); r.code != 204 {
		t.Fatalf("self revoke: %d %s", r.code, r.body)
	}
	if r := e.key(e.rwTok, "GET", "/me", nil); r.code != 401 {
		t.Fatalf("after revoking itself: %d %s", r.code, r.body)
	}
}

// TestAdminKeyExpiredSaysSo: an expired key is a 401 that says when.
func TestAdminKeyExpiredSaysSo(t *testing.T) {
	e := newAdminEnv(t)
	if r := e.key(e.rwTok, "GET", "/admin/keys", nil); r.code != 200 {
		t.Fatalf("fresh: %d %s", r.code, r.body)
	}
	e.now = e.rwKey.ExpiresAt.Add(time.Second)
	r := e.key(e.rwTok, "GET", "/admin/keys", nil)
	if r.code != 401 || !strings.Contains(string(r.body), "this admin key expired on "+e.rwKey.ExpiresAt.Format("2 Jan 2006")) {
		t.Fatalf("expired: %d %s", r.code, r.body)
	}
}

// TestAdminKeyNetworks: auth.admin_keys.allowed_cidrs is checked against
// the client address, through trusted proxies, before the key is.
func TestAdminKeyNetworks(t *testing.T) {
	e := newAdminEnv(t)
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := config.Default()
	cfg.Auth.AdminKeys.AllowedCIDRs = []string{"10.1.0.0/16"}
	authn, err := auth.New(e.svc, cfg.Auth, "http://localhost:8080", quiet)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(e.svc, authn, quiet).Mount(mux)
	srv := middleware.Chain(mux, middleware.RequestID, middleware.RealIP([]string{"192.0.2.10/32"}))
	call := func(token, peer, xff string) resp {
		req := httptest.NewRequest("GET", Prefix+"/admin/keys", nil)
		req.RemoteAddr = peer + ":1234"
		req.Header.Set("Authorization", "Bearer "+token)
		if xff != "" {
			req.Header.Set("X-Forwarded-For", xff)
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return resp{code: rec.Code, body: rec.Body.Bytes()}
	}
	bogus := "vka_" + e.rwKey.Prefix + "_" + strings.Repeat("x", 32)
	for _, tc := range []struct {
		name, token, peer, xff string
		code                   int
	}{
		{"inside", e.rwTok, "10.1.2.3", "", 200},
		{"outside", e.rwTok, "203.0.113.9", "", 403},
		{"through the proxy, inside", e.rwTok, "192.0.2.10", "10.1.2.3", 200},
		{"through the proxy, outside", e.rwTok, "192.0.2.10", "203.0.113.9", 403},
		{"forged header from outside", e.rwTok, "203.0.113.9", "10.1.2.3", 403},
		{"a bad key from outside is refused before it is checked", bogus, "203.0.113.9", "", 403},
		{"a bad key from inside", bogus, "10.1.2.3", "", 401},
	} {
		r := call(tc.token, tc.peer, tc.xff)
		if r.code != tc.code {
			t.Errorf("%s: %d %s, want %d", tc.name, r.code, r.body, tc.code)
		}
		if tc.code == 403 && !strings.Contains(string(r.body), "not accepted from") {
			t.Errorf("%s: %s", tc.name, r.body)
		}
	}
	// project keys do not care where they come from
	req := httptest.NewRequest("GET", Prefix+"/me", nil)
	req.RemoteAddr = "203.0.113.9:1"
	req.Header.Set("Authorization", "Bearer "+e.rw)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("project key from outside: %d", rec.Code)
	}
}

// TestAdminRoutesWalk walks every admin route with an rw admin key, in
// the order a person would: status, Location and body; the audit names
// the key.
func TestAdminRoutesWalk(t *testing.T) {
	e := newAdminEnv(t)
	ctx := context.Background()
	if err := e.svc.ApplyAuthPolicy(ctx, service.AuthPolicy{
		Proxy: service.ProviderPolicy{Enabled: true, Roles: service.RolesVink, StripRealm: true, Lowercase: true},
		OIDC:  service.ProviderPolicy{Roles: service.RolesGroups},
	}); err != nil {
		t.Fatal(err)
	}
	var keyID string
	steps := []struct {
		name     string
		method   string
		path     func() string
		body     any
		code     int
		location string
		check    func(t *testing.T, r resp)
	}{
		{"create org", "POST", fixed("/admin/orgs"), map[string]any{"slug": "acme2", "name": "Acme 2", "owner": "j", "quota_monitors": 10}, 201, "/api/v1/admin/orgs/acme2", func(t *testing.T, r resp) {
			var o adminapi.Org
			r.json(t, &o)
			if o.Slug != "acme2" || len(o.Owners) != 1 || o.Owners[0] != "j" || o.QuotaMonitors == nil || *o.QuotaMonitors != 10 || o.QuotaAgents != nil {
				t.Fatalf("%s", r.body)
			}
		}},
		{"list orgs", "GET", fixed("/admin/orgs"), nil, 200, "", func(t *testing.T, r resp) {
			var p page[adminapi.Org]
			r.json(t, &p)
			if len(p.Items) != 3 {
				t.Fatalf("%s", r.body)
			}
		}},
		{"get org", "GET", fixed("/admin/orgs/homelab"), nil, 200, "", func(t *testing.T, r resp) {
			var o adminapi.Org
			r.json(t, &o)
			if o.Projects != 2 {
				t.Fatalf("%s", r.body)
			}
		}},
		{"unknown org", "GET", fixed("/admin/orgs/nope"), nil, 404, "", nil},
		{"null clears a quota", "PATCH", fixed("/admin/orgs/acme2"), map[string]any{"quota_monitors": nil, "quota_agents": 2}, 200, "", func(t *testing.T, r resp) {
			var o adminapi.Org
			r.json(t, &o)
			if o.QuotaMonitors != nil || o.QuotaAgents == nil || *o.QuotaAgents != 2 || o.Name != "Acme 2" {
				t.Fatalf("%s", r.body)
			}
		}},
		{"a field left out stays", "PATCH", fixed("/admin/orgs/acme2"), map[string]any{"name": "Acme Two"}, 200, "", func(t *testing.T, r resp) {
			var o adminapi.Org
			r.json(t, &o)
			if o.Name != "Acme Two" || o.QuotaAgents == nil || *o.QuotaAgents != 2 {
				t.Fatalf("%s", r.body)
			}
		}},
		{"unknown field", "PATCH", fixed("/admin/orgs/acme2"), map[string]any{"slug": "x"}, 400, "", nil},
		{"create org key", "POST", fixed("/admin/orgs/acme2/keys"), map[string]any{"name": "gitops", "access": "rw"}, 201, "/api/v1/admin/orgs/acme2/keys/", func(t *testing.T, r resp) {
			var k adminapi.OrgKey
			r.json(t, &k)
			if !strings.HasPrefix(k.Key, "vk_"+k.Prefix+"_") || k.Access != domain.AccessRW {
				t.Fatalf("%s", r.body)
			}
			keyID = k.ID
		}},
		{"list org keys", "GET", fixed("/admin/orgs/acme2/keys"), nil, 200, "", func(t *testing.T, r resp) {
			var p page[adminapi.OrgKey]
			r.json(t, &p)
			if len(p.Items) != 1 || p.Items[0].Key != "" {
				t.Fatalf("%s", r.body)
			}
		}},
		{"revoke org key", "DELETE", func() string { return "/admin/orgs/acme2/keys/" + keyID }, nil, 204, "", nil},
		{"create agent", "POST", fixed("/admin/orgs/acme2/agents"), map[string]any{"name": "edge", "labels": map[string]string{"site": "dc2"}}, 201, "/api/v1/admin/orgs/acme2/agents/edge", func(t *testing.T, r resp) {
			var a adminapi.Agent
			r.json(t, &a)
			if !strings.HasPrefix(a.Token, "vat_") || !strings.Contains(a.Command, a.Token) || a.Labels["site"] != "dc2" {
				t.Fatalf("%s", r.body)
			}
		}},
		{"list agents", "GET", fixed("/admin/orgs/acme2/agents"), nil, 200, "", func(t *testing.T, r resp) {
			var p page[adminapi.Agent]
			r.json(t, &p)
			if len(p.Items) != 1 || p.Items[0].Token != "" || p.Items[0].Name != "edge" {
				t.Fatalf("%s", r.body)
			}
		}},
		{"revoke agent", "DELETE", fixed("/admin/orgs/acme2/agents/edge"), nil, 204, "", nil},
		{"create local user", "POST", fixed("/admin/users"), map[string]any{"subject": "bob", "password": "correct horse battery"}, 201, "/api/v1/admin/users/bob", func(t *testing.T, r resp) {
			var u adminapi.User
			r.json(t, &u)
			if u.Source != "local" || u.InstanceAdmin || len(u.Roles) != 0 {
				t.Fatalf("%s", r.body)
			}
		}},
		{"create proxy user, normalised", "POST", fixed("/admin/users"), map[string]any{"subject": "JDoe@CORP.EXAMPLE", "source": "proxy"}, 201, "/api/v1/admin/users/jdoe", nil},
		{"proxy user with a password", "POST", fixed("/admin/users"), map[string]any{"subject": "x", "source": "proxy", "password": "correct horse battery"}, 422, "", nil},
		{"existing user", "POST", fixed("/admin/users"), map[string]any{"subject": "jdoe", "source": "proxy"}, 409, "", nil},
		{"grant", "PUT", fixed("/admin/users/jdoe/orgs/acme2"), map[string]any{"role": "admin"}, 200, "", func(t *testing.T, r resp) {
			var u adminapi.User
			r.json(t, &u)
			if len(u.Roles) != 1 || u.Roles[0] != (adminapi.Role{Org: "acme2", Role: domain.RoleAdmin, Source: "local"}) {
				t.Fatalf("%s", r.body)
			}
		}},
		{"get user", "GET", fixed("/admin/users/jdoe"), nil, 200, "", nil},
		{"list users", "GET", fixed("/admin/users"), nil, 200, "", func(t *testing.T, r resp) {
			var p page[adminapi.User]
			r.json(t, &p)
			if len(p.Items) != 4 {
				t.Fatalf("%d users: %s", len(p.Items), r.body)
			}
		}},
		{"promote", "PATCH", fixed("/admin/users/bob"), map[string]any{"instance_admin": true}, 200, "", nil},
		{"no reset link for an instance admin by key", "POST", fixed("/admin/users/bob/reset-link"), nil, 403, "", nil},
		{"disable and demote", "PATCH", fixed("/admin/users/bob"), map[string]any{"instance_admin": false, "disabled": true}, 200, "", func(t *testing.T, r resp) {
			var u adminapi.User
			r.json(t, &u)
			if u.InstanceAdmin || !u.Disabled || u.DisabledBy != "ci" {
				t.Fatalf("%s", r.body)
			}
		}},
		{"reset link", "POST", fixed("/admin/users/bob/reset-link"), nil, 200, "", func(t *testing.T, r resp) {
			var l adminapi.ResetLink
			r.json(t, &l)
			if !strings.HasPrefix(l.URL, "http://localhost:8080/reset/rs_") || l.ExpiresAt.IsZero() {
				t.Fatalf("%s", r.body)
			}
		}},
		{"two-factor reset", "POST", fixed("/admin/users/bob/totp-reset"), nil, 204, "", nil},
		{"ungrant", "DELETE", fixed("/admin/users/jdoe/orgs/acme2"), nil, 204, "", nil},
		{"delete an org with projects", "DELETE", fixed("/admin/orgs/homelab"), nil, 422, "", nil},
		{"delete org", "DELETE", fixed("/admin/orgs/acme2"), nil, 204, "", nil},
		{"deleted org", "GET", fixed("/admin/orgs/acme2"), nil, 404, "", nil},
	}
	for _, st := range steps {
		r := e.key(e.rwTok, st.method, st.path(), st.body)
		if r.code != st.code {
			t.Fatalf("%s: %d %s, want %d", st.name, r.code, r.body, st.code)
		}
		if st.location != "" && !strings.HasPrefix(r.hdr.Get("Location"), st.location) {
			t.Errorf("%s: Location %q, want %s…", st.name, r.hdr.Get("Location"), st.location)
		}
		if st.check != nil {
			t.Run(st.name, func(t *testing.T) { st.check(t, r) })
		}
	}
	var kind, via string
	if err := e.svc.DB().Reader.QueryRowContext(ctx, `SELECT actor_kind, via FROM audit WHERE act = 'org.create' AND target = 'acme2'`).Scan(&kind, &via); err != nil || kind != "key" || via != "api vka_"+e.rwKey.Prefix {
		t.Errorf("audit: %q %q %v", kind, via, err)
	}
}

func fixed(p string) func() string { return func() string { return p } }

// TestAdminGrantWhileGroupsDecide: through the API as anywhere, a role the
// proxy's groups give cannot be changed in vink while they decide.
func TestAdminGrantWhileGroupsDecide(t *testing.T) {
	e := newAdminEnv(t)
	ctx := context.Background()
	if err := e.svc.ApplyAuthPolicy(ctx, service.AuthPolicy{
		Proxy: service.ProviderPolicy{Enabled: true, Roles: service.RolesGroups},
		OIDC:  service.ProviderPolicy{Roles: service.RolesGroups},
	}); err != nil {
		t.Fatal(err)
	}
	carol, err := e.svc.EnsureProxyUser(ctx, "carol", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.SyncHeaderMemberships(ctx, carol.ID, map[string]domain.Role{"homelab": domain.RoleMember}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, method, path string
		body               any
		detail             string
	}{
		{"grant", "PUT", "/admin/users/carol/orgs/homelab", map[string]any{"role": "admin"}, "groups"},
		{"ungrant", "DELETE", "/admin/users/carol/orgs/homelab", nil, "groups"},
		{"promote", "PATCH", "/admin/users/carol", map[string]any{"instance_admin": true}, "auth.proxy.roles is groups"},
		{"create an admin", "POST", "/admin/users", map[string]any{"subject": "dave", "source": "proxy", "instance_admin": true}, "auth.proxy.roles is groups"},
	} {
		r := e.key(e.rwTok, tc.method, tc.path, tc.body)
		if r.code != 422 || !strings.Contains(string(r.body), tc.detail) {
			t.Errorf("%s: %d %s", tc.name, r.code, r.body)
		}
	}
	if _, err := e.svc.UserBySubject(ctx, "dave"); err == nil {
		t.Error("a refused create left an account")
	}
}

// TestProblemDetailWithoutSentinel: a refusal's detail is the reason,
// without "forbidden" or "conflict" again in front of it.
func TestProblemDetailWithoutSentinel(t *testing.T) {
	e := newAdminEnv(t)
	for _, tc := range []struct {
		r    resp
		want string
	}{
		{e.key(e.roToken, "DELETE", "/admin/keys/"+e.rwKey.ID, nil), `"detail":"read-only API key"`},
		{e.key(e.rwTok, "GET", "/monitors", nil), `"detail":"an instance admin key acts only on /api/v1/admin; use a project or org key"`},
		{e.key(e.rwTok, "POST", "/admin/orgs", map[string]any{"slug": "homelab"}), `"detail":"`},
	} {
		if !strings.Contains(string(tc.r.body), tc.want) || strings.Contains(string(tc.r.body), `"detail":"forbidden`) || strings.Contains(string(tc.r.body), `"detail":"conflict`) {
			t.Errorf("%d %s", tc.r.code, tc.r.body)
		}
	}
}
