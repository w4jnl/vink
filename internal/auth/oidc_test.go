package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/w4jnl/vink/internal/auth/oidctest"
	"github.com/w4jnl/vink/internal/config"
	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/service"
)

func oidcAuth(t *testing.T, e *env, p *oidctest.Provider, mutate func(*config.Auth)) *Authenticator {
	t.Helper()
	cfg := config.Default().Auth
	p.Configure(&cfg)
	if mutate != nil {
		mutate(&cfg)
	}
	a, err := New(e.svc, cfg, "http://localhost:8080", quietLog())
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// start runs OIDCStart and returns the provider URL and the flight cookie.
func start(t *testing.T, a *Authenticator, next string) (string, *http.Cookie) {
	t.Helper()
	rec := httptest.NewRecorder()
	to, err := a.OIDCStart(rec, httptest.NewRequest("GET", "/auth/oidc/start", nil), next)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == OIDCCookie && c.Value != "" {
			return to, c
		}
	}
	t.Fatal("no flight cookie")
	return "", nil
}

func callback(a *Authenticator, path string, cookie *http.Cookie) (*Principal, string, *httptest.ResponseRecorder, error) {
	req := httptest.NewRequest("GET", path, nil)
	req.RemoteAddr = "203.0.113.9:1"
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	p, next, err := a.OIDCCallback(rec, req)
	return p, next, rec, err
}

func TestOIDCSignIn(t *testing.T) {
	e := newEnv(t)
	p := oidctest.New(t)
	p.Claims = map[string]any{"preferred_username": "Alice", "email": "alice@example.com", "name": "Alice Jansen", "groups": []string{"vink:homelab:admin", "vink:admin", "other"}}
	a := oidcAuth(t, e, p, nil)
	ctx := context.Background()

	// the authorization request: PKCE, nonce, state, the callback on the base URL
	to, cookie := start(t, a, "/o/homelab/p/prod")
	u, err := url.Parse(to)
	if err != nil || !strings.HasPrefix(to, p.Server.URL+"/auth?") {
		t.Fatalf("auth url: %s %v", to, err)
	}
	q := u.Query()
	if q.Get("redirect_uri") != "http://localhost:8080/auth/oidc/callback" || q.Get("scope") != "openid profile email groups" || q.Get("code_challenge_method") != "S256" {
		t.Fatalf("auth query: %v", q)
	}
	if !cookie.HttpOnly {
		t.Error("flight cookie must be HttpOnly")
	}

	// the callback: no cookie, a wrong state, the provider's own error
	if _, _, _, err := callback(a, "/auth/oidc/callback?code=x&state="+q.Get("state"), nil); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("no cookie: %v", err)
	}
	var oe OIDCError
	if _, _, _, err := callback(a, "/auth/oidc/callback?code=x&state=forged", cookie); !errors.As(err, &oe) || oe.Code != "state_mismatch" || !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("forged state: %v", err)
	}
	if _, _, _, err := callback(a, "/auth/oidc/callback?error=access_denied&state="+q.Get("state"), cookie); !errors.As(err, &oe) || oe.Code != "access_denied" {
		t.Fatalf("provider error: %v", err)
	}
	if p.Tokens != 0 {
		t.Fatal("tokens were exchanged before the state checked out")
	}

	// the real thing
	cb := p.Visit(t, to)
	principal, next, rec, err := callback(a, cb, cookie)
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	if next != "/o/homelab/p/prod" || principal.User.Subject != "alice" || principal.User.Email != "alice@example.com" || principal.User.DisplayName != "Alice Jansen" || principal.User.Source != "oidc" || !principal.InstanceAdmin || principal.Source != "session" {
		t.Fatalf("principal: %+v next %s", principal, next)
	}
	if len(principal.Memberships) != 1 || principal.Memberships[0].OrgID != e.org.ID || principal.Memberships[0].Role != domain.RoleAdmin || principal.Memberships[0].Source != "oidc" {
		t.Fatalf("memberships: %+v", principal.Memberships)
	}
	var session, cleared bool
	for _, c := range rec.Result().Cookies() {
		switch c.Name {
		case CookieName:
			session = c.Value != ""
		case OIDCCookie:
			cleared = c.Value == ""
		}
	}
	if !session || !cleared {
		t.Fatalf("cookies after callback: session %v, flight cleared %v", session, cleared)
	}
	// the same state again is refused, before any token exchange
	tokens := p.Tokens
	if _, _, _, err := callback(a, cb, cookie); !errors.As(err, &oe) || oe.Code != "state_reused" || p.Tokens != tokens {
		t.Fatalf("replayed callback: %v (tokens %d -> %d)", err, tokens, p.Tokens)
	}

	// the session identifies the person on the next request
	req := httptest.NewRequest("GET", "/", nil)
	for _, c := range rec.Result().Cookies() {
		if c.Name == CookieName {
			req.AddCookie(c)
		}
	}
	if got, err := a.Identify(req); err != nil || got == nil || got.User.Subject != "alice" || !got.InstanceAdmin {
		t.Fatalf("identify: %+v %v", got, err)
	}

	// a nonce that does not match the flight is refused
	p.WrongNonce = true
	to, cookie = start(t, a, "/")
	if _, _, _, err := callback(a, p.Visit(t, to), cookie); !errors.As(err, &oe) || oe.Code != "nonce_mismatch" {
		t.Fatalf("wrong nonce: %v", err)
	}
	p.WrongNonce = false

	// fewer groups next time: the admin role and the flag go away
	p.Claims["groups"] = []string{"vink:homelab:viewer"}
	to, cookie = start(t, a, "/")
	principal, _, _, err = callback(a, p.Visit(t, to), cookie)
	if err != nil || principal.InstanceAdmin || len(principal.Memberships) != 1 || principal.Memberships[0].Role != domain.RoleViewer {
		t.Fatalf("after fewer groups: %+v %v", principal, err)
	}
	if u, _ := e.svc.UserByID(ctx, principal.User.ID); u.InstanceAdmin {
		t.Fatal("flag kept in the database")
	}

	// a disabled account, and a local account's name, are not the provider's to use
	admin := domain.Scope{InstanceAdmin: true, UserID: e.user.ID, Role: domain.RoleOwner, Actor: "user:j"}
	if err := e.svc.SetUserDisabled(ctx, admin, principal.User.ID, true); err != nil {
		t.Fatal(err)
	}
	to, cookie = start(t, a, "/")
	if _, _, _, err := callback(a, p.Visit(t, to), cookie); !errors.As(err, &oe) || oe.Code != "disabled" {
		t.Fatalf("disabled: %v", err)
	}
	p.Claims["preferred_username"] = "j"
	to, cookie = start(t, a, "/")
	if _, _, _, err := callback(a, p.Visit(t, to), cookie); !errors.As(err, &oe) || oe.Code != "local_account" {
		t.Fatalf("local collision: %v", err)
	}
	// a proxy account is the same identity: it moves to oidc on first sign-in
	if _, err := e.svc.EnsureProxyUser(ctx, "pete", "pete@example.com", "Pete"); err != nil {
		t.Fatal(err)
	}
	p.Claims["preferred_username"] = "pete"
	to, cookie = start(t, a, "/")
	principal, _, _, err = callback(a, p.Visit(t, to), cookie)
	if err != nil || principal.User.Subject != "pete" || principal.User.Source != "oidc" {
		t.Fatalf("proxy account adopted: %+v %v", principal, err)
	}
	if u, _ := e.svc.UserBySubject(ctx, "pete"); u.Source != "oidc" {
		t.Fatalf("source in the database: %s", u.Source)
	}

	// sign-ins are in the log with their method
	page, err := e.svc.AuditLog(ctx, admin, service.AuditFilter{})
	if err != nil {
		t.Fatal(err)
	}
	var seen int
	for _, en := range page.Entries {
		if en.Action == "user.signin" && en.Detail["method"] == "oidc" && en.Target == "alice" {
			seen++
		}
	}
	if seen != 2 {
		t.Fatalf("oidc sign-ins logged: %d", seen)
	}
}

func TestOIDCWithoutLocalAccounts(t *testing.T) {
	e := newEnv(t)
	p := oidctest.New(t)
	p.Claims = map[string]any{"preferred_username": "bob@EXAMPLE.COM", "groups": "vink:homelab:member"}
	a := oidcAuth(t, e, p, func(c *config.Auth) {
		c.Local.Enabled = false
		c.OIDC.AutoRedirect = true
		c.OIDC.StripRealm = true
		c.OIDC.ClientSecret = ""
	})
	p.Secret = ""
	if !a.OIDCAutoRedirect() || a.LocalEnabled() || a.OIDCDisplayName() != "Keycloak" {
		t.Fatal("config not reflected")
	}
	to, cookie := start(t, a, "/")
	principal, _, _, err := callback(a, p.Visit(t, to), cookie)
	if err != nil || principal.User.Subject != "bob" || len(principal.Memberships) != 1 || principal.Memberships[0].Role != domain.RoleMember {
		t.Fatalf("public client, realm stripped, groups as a string: %+v %v", principal, err)
	}
	if _, err := a.Login(httptest.NewRecorder(), httptest.NewRequest("POST", "/login", nil), "j", "correct horse"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("password sign-in with local off: %v", err)
	}
}
