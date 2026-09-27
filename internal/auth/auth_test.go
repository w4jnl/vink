package auth

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/w4jnl/vink/internal/config"
	"github.com/w4jnl/vink/internal/db/dbtest"
	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/service"
)

type env struct {
	svc     *service.Service
	org     *domain.Org
	project *domain.Project
	user    *domain.User
}

func newEnv(t *testing.T) *env {
	t.Helper()
	d := dbtest.Open(t)
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := service.New(d, nil, quiet, service.DefaultConfig())
	ctx := context.Background()
	admin := domain.Scope{InstanceAdmin: true, Role: domain.RoleOwner}
	org, _ := svc.CreateOrg(ctx, admin, "homelab", "Homelab")
	project, _ := svc.CreateProject(ctx, admin, org.ID, "prod", "Prod", "UTC")
	user, err := svc.CreateLocalUser(ctx, admin, "j", "j@example.com", "J", "correct horse", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.SetMembership(ctx, admin, user.ID, org.ID, domain.RoleMember); err != nil {
		t.Fatal(err)
	}
	return &env{svc: svc, org: org, project: project, user: user}
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func localAuth(t *testing.T, e *env) *Authenticator {
	t.Helper()
	cfg := config.Default().Auth
	a, err := New(e.svc, cfg, "http://localhost:8080", quietLog())
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestLocalLoginSessionAndCSRF(t *testing.T) {
	e := newEnv(t)
	a := localAuth(t, e)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/login", nil)
	req.RemoteAddr = "203.0.113.1:1"
	if _, err := a.Login(rec, req, "j", "wrong"); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("wrong password: %v", err)
	}
	p, err := a.Login(rec, req, "j", "correct horse")
	if err != nil || p.User.ID != e.user.ID || p.Session == nil || p.Source != "session" {
		t.Fatalf("login: %+v %v", p, err)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != CookieName || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode || cookies[0].Secure {
		t.Fatalf("cookie: %+v", cookies)
	}
	// identify from the cookie
	req2 := httptest.NewRequest("GET", "/", nil)
	req2.AddCookie(cookies[0])
	p2, err := a.Identify(req2)
	if err != nil || p2 == nil || p2.User.ID != e.user.ID {
		t.Fatalf("identify: %+v %v", p2, err)
	}
	if role, ok := p2.RoleIn(e.org.ID); !ok || role != domain.RoleMember {
		t.Errorf("RoleIn = %v %v", role, ok)
	}
	if _, ok := p2.RoleIn("other"); ok {
		t.Error("role in unknown org")
	}
	// CSRF: header or form field must match
	req3 := httptest.NewRequest("POST", "/x", strings.NewReader(CSRFField+"="+p2.CSRF()))
	req3.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if !a.CheckCSRF(req3, p2) {
		t.Error("form token rejected")
	}
	req4 := httptest.NewRequest("POST", "/x", nil)
	req4.Header.Set(CSRFHeader, p2.CSRF())
	if !a.CheckCSRF(req4, p2) {
		t.Error("header token rejected")
	}
	req5 := httptest.NewRequest("POST", "/x", nil)
	req5.Header.Set(CSRFHeader, "nope")
	if a.CheckCSRF(req5, p2) || a.CheckCSRF(httptest.NewRequest("POST", "/x", nil), p2) {
		t.Error("bad or missing token accepted")
	}
	// user scope in own project; not found elsewhere
	sc, err := a.UserScope(p2, e.project)
	if err != nil || sc.Role != domain.RoleMember || sc.UserID != e.user.ID || sc.ProjectID != e.project.ID {
		t.Fatalf("user scope: %+v %v", sc, err)
	}
	other := &domain.Project{ID: "x", OrgID: "other-org"}
	if _, err := a.UserScope(p2, other); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("foreign project must be not found: %v", err)
	}
	if _, err := a.UserScope(nil, e.project); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("nil principal: %v", err)
	}
	// logout clears
	rec2 := httptest.NewRecorder()
	if err := a.Logout(rec2, req2); err != nil {
		t.Fatal(err)
	}
	if c := rec2.Result().Cookies(); len(c) != 1 || c[0].MaxAge != -1 {
		t.Errorf("logout cookie: %+v", c)
	}
	if p3, _ := a.Identify(req2); p3 != nil {
		t.Error("session must be gone after logout")
	}
	// anonymous
	if p, err := a.Identify(httptest.NewRequest("GET", "/", nil)); p != nil || err != nil {
		t.Errorf("anonymous: %+v %v", p, err)
	}
}

func TestLoginRateLimits(t *testing.T) {
	e := newEnv(t)
	a := localAuth(t, e)
	rec := httptest.NewRecorder()
	limited := false
	for i := 0; i < 8; i++ {
		req := httptest.NewRequest("POST", "/login", nil)
		req.RemoteAddr = "203.0.113.1:1"
		if _, err := a.Login(rec, req, "j", "wrong"); errors.Is(err, domain.ErrRateLimited) {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("per-subject limit never hit")
	}
	// another IP, another subject: still fine
	req := httptest.NewRequest("POST", "/login", nil)
	req.RemoteAddr = "198.51.100.1:1"
	if _, err := a.Login(rec, req, "someone", "x"); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("unrelated login: %v", err)
	}
}

func TestSecureCookieOnHTTPS(t *testing.T) {
	e := newEnv(t)
	a, _ := New(e.svc, config.Default().Auth, "https://vink.example.com", quietLog())
	rec := httptest.NewRecorder()
	if _, err := a.Login(rec, httptest.NewRequest("POST", "/login", nil), "j", "correct horse"); err != nil {
		t.Fatal(err)
	}
	if !rec.Result().Cookies()[0].Secure {
		t.Error("cookie must be Secure behind https")
	}
}

func TestKeyScope(t *testing.T) {
	e := newEnv(t)
	a := localAuth(t, e)
	ctx := context.Background()
	admin := domain.Scope{OrgID: e.org.ID, ProjectID: e.project.ID, Role: domain.RoleAdmin}
	_, roPlain, _ := e.svc.CreateAPIKey(ctx, admin, "ro", domain.AccessRO)
	_, rwPlain, _ := e.svc.CreateAPIKey(ctx, admin, "rw", domain.AccessRW)
	ro, err := a.KeyScope(ctx, roPlain)
	if err != nil || ro.Role != domain.RoleViewer || ro.KeyAccess != domain.AccessRO || ro.ProjectID != e.project.ID || !ro.IsKey() {
		t.Fatalf("ro scope: %+v %v", ro, err)
	}
	rw, err := a.KeyScope(ctx, rwPlain)
	if err != nil || rw.Role != domain.RoleAdmin || !strings.HasPrefix(rw.Actor, "key:") {
		t.Fatalf("rw scope: %+v %v", rw, err)
	}
	if _, err := a.KeyScope(ctx, "vk_nope"); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("bad key: %v", err)
	}
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+rwPlain)
	if tok, ok := BearerToken(req); !ok || tok != rwPlain {
		t.Error("BearerToken")
	}
	req.Header.Set("Authorization", "Basic xyz")
	if _, ok := BearerToken(req); ok {
		t.Error("non-bearer accepted")
	}
}

func proxyAuth(t *testing.T, e *env, mutate func(*config.Auth)) *Authenticator {
	t.Helper()
	cfg := config.Default().Auth
	cfg.Proxy.Enabled = true
	cfg.Proxy.Secret = "s3cret"
	cfg.Proxy.TrustedCIDRs = []string{"10.0.0.0/8"}
	cfg.Local.Enabled = false
	if mutate != nil {
		mutate(&cfg)
	}
	a, err := New(e.svc, cfg, "https://vink.example.com", quietLog())
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func proxyReq(peer, secret, user, groups string) *http.Request {
	req := httptest.NewRequest("GET", "/o/homelab/p/prod", nil)
	req.RemoteAddr = peer
	if secret != "" {
		req.Header.Set("X-Auth-Proxy-Secret", secret)
	}
	if user != "" {
		req.Header.Set("Remote-User", user)
	}
	req.Header.Set("Remote-Email", "alice@example.com")
	req.Header.Set("Remote-Name", "Alice")
	if groups != "" {
		req.Header.Set("Remote-Groups", groups)
	}
	return req
}

func TestProxyIdentity(t *testing.T) {
	e := newEnv(t)
	a := proxyAuth(t, e, nil)
	// untrusted peer, wrong secret, no user: all anonymous
	for name, req := range map[string]*http.Request{
		"untrusted peer": proxyReq("203.0.113.5:1", "s3cret", "alice", "vink:homelab:admin"),
		"wrong secret":   proxyReq("10.0.0.1:1", "wrong", "alice", "vink:homelab:admin"),
		"no secret":      proxyReq("10.0.0.1:1", "", "alice", "vink:homelab:admin"),
		"no user":        proxyReq("10.0.0.1:1", "s3cret", "", "vink:homelab:admin"),
	} {
		if p, err := a.Identify(req); p != nil || err != nil {
			t.Errorf("%s: %+v %v", name, p, err)
		}
	}
	// valid: user created on first sight, realm stripped, lowercased, role mapped
	p, err := a.Identify(proxyReq("10.0.0.1:1", "s3cret", "Alice@CORP.EXAMPLE", "staff, vink:homelab:admin, vink:homelab:viewer, vink:nope:owner"))
	if err != nil || p == nil {
		t.Fatalf("identify: %+v %v", p, err)
	}
	if p.User.Subject != "alice" || p.User.Email != "alice@example.com" || p.Source != "proxy" || p.Session != nil || p.CSRF() != "" {
		t.Fatalf("principal: %+v", p)
	}
	if role, ok := p.RoleIn(e.org.ID); !ok || role != domain.RoleAdmin {
		t.Fatalf("role: %v %v", role, ok)
	}
	if p.InstanceAdmin {
		t.Error("not an instance admin")
	}
	// proxy requests never need CSRF tokens
	if !a.CheckCSRF(httptest.NewRequest("POST", "/x", nil), p) {
		t.Error("proxy principal must pass CSRF")
	}
	// groups change on the next request: role drops to viewer
	p, _ = a.Identify(proxyReq("10.0.0.1:1", "s3cret", "alice", "vink:homelab:viewer"))
	if role, _ := p.RoleIn(e.org.ID); role != domain.RoleViewer {
		t.Fatalf("role after change: %v", role)
	}
	// groups removed: no membership, instance admin group grants admin
	p, _ = a.Identify(proxyReq("10.0.0.1:1", "s3cret", "alice", "vink:admin"))
	if _, ok := p.RoleIn(e.org.ID); !ok || !p.InstanceAdmin {
		t.Fatalf("instance admin: %+v", p)
	}
	p, _ = a.Identify(proxyReq("10.0.0.1:1", "s3cret", "alice", ""))
	if _, ok := p.RoleIn(e.org.ID); ok {
		t.Fatal("membership must be pruned")
	}
}

func TestProxyGroupMapAndDefaultOrg(t *testing.T) {
	e := newEnv(t)
	a := proxyAuth(t, e, func(c *config.Auth) {
		c.Proxy.GroupMap = map[string]string{"CN=Monitoring Admins": "homelab:admin"}
		c.Proxy.DefaultOrg = "homelab"
		c.Proxy.StripRealm = false
		c.Proxy.Lowercase = false
	})
	p, err := a.Identify(proxyReq("10.0.0.1:1", "s3cret", "Bob@CORP", "CN=Monitoring Admins"))
	if err != nil || p.User.Subject != "Bob@CORP" {
		t.Fatalf("subject kept as is: %+v %v", p, err)
	}
	if role, _ := p.RoleIn(e.org.ID); role != domain.RoleAdmin {
		t.Fatalf("group_map role: %v", role)
	}
	p, _ = a.Identify(proxyReq("10.0.0.1:1", "s3cret", "Carol", "nothing"))
	if role, ok := p.RoleIn(e.org.ID); !ok || role != domain.RoleViewer {
		t.Fatalf("default org viewer: %v %v", role, ok)
	}
}

func TestProxyWithLocalFallback(t *testing.T) {
	e := newEnv(t)
	a := proxyAuth(t, e, func(c *config.Auth) { c.Local.Enabled = true })
	rec := httptest.NewRecorder()
	p, err := a.Login(rec, httptest.NewRequest("POST", "/login", nil), "j", "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "203.0.113.9:1"
	req.AddCookie(rec.Result().Cookies()[0])
	got, err := a.Identify(req)
	if err != nil || got == nil || got.User.ID != p.User.ID || got.Source != "session" {
		t.Fatalf("session fallback: %+v %v", got, err)
	}
}

func TestIdentityMiddlewareAndContext(t *testing.T) {
	e := newEnv(t)
	a := localAuth(t, e)
	var seen *Principal
	h := a.Identity(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = PrincipalFrom(r.Context())
		ctx := WithScope(r.Context(), domain.Scope{ProjectID: "p"})
		if sc, ok := ScopeFrom(ctx); !ok || sc.ProjectID != "p" {
			t.Error("scope round trip")
		}
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	if seen != nil {
		t.Error("anonymous request must carry a nil principal")
	}
	rec := httptest.NewRecorder()
	if _, err := a.Login(rec, httptest.NewRequest("POST", "/login", nil), "j", "correct horse"); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(rec.Result().Cookies()[0])
	h.ServeHTTP(httptest.NewRecorder(), req)
	if seen == nil || seen.User.Subject != "j" {
		t.Errorf("principal from middleware: %+v", seen)
	}
	if _, ok := ScopeFrom(context.Background()); ok {
		t.Error("empty context has no scope")
	}
}
