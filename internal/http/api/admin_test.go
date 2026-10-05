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
