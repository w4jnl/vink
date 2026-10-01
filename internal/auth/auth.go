// Package auth turns requests into principals and scopes. Three inputs
// feed one shape: trusted-proxy identity headers, the local session
// cookie, and project-scoped API keys. Handlers read the Scope from the
// context and never look at credentials themselves.
package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/w4jnl/vink/internal/config"
	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/http/middleware"
	"github.com/w4jnl/vink/internal/outbound"
	"github.com/w4jnl/vink/internal/ratelimit"
	"github.com/w4jnl/vink/internal/service"
)

// CookieName is the local session cookie.
const CookieName = "vink_session"

// CSRFHeader carries the double-submit token on htmx and fetch requests;
// CSRFField carries it in plain forms.
const (
	CSRFHeader = "X-CSRF-Token"
	CSRFField  = "_csrf"
	// ChallengeCookie carries the sign-in challenge between the password
	// and the code step.
	ChallengeCookie = "vink_login"
)

// Principal is an authenticated person.
type Principal struct {
	User          domain.User
	InstanceAdmin bool
	Memberships   []domain.Membership
	// Session is set for local sessions; proxy identities have none.
	Session *service.Session
	// Source is "session" or "proxy".
	Source string
	// Groups are the raw proxy groups, for the no-access page.
	Groups []string
}

// RoleIn returns the principal's role in an org. Instance admins are
// owners everywhere.
func (p *Principal) RoleIn(orgID string) (domain.Role, bool) {
	if p.InstanceAdmin {
		return domain.RoleOwner, true
	}
	for _, m := range p.Memberships {
		if m.OrgID == orgID {
			return m.Role, true
		}
	}
	return "", false
}

// CSRF returns the session's token, or "" for proxy identities.
func (p *Principal) CSRF() string {
	if p.Session == nil {
		return ""
	}
	return p.Session.CSRF
}

// Authenticator resolves identities.
type Authenticator struct {
	svc       *service.Service
	cfg       config.Auth
	log       *slog.Logger
	secure    bool
	trusted   []*net.IPNet
	groupRe   *regexp.Regexp
	loginIP   *ratelimit.Limiter
	loginUser *ratelimit.Limiter
	now       func() time.Time
	baseURL   string
	// proxyRules and oidcRules map each provider's groups to roles.
	proxyRules groupRules
	oidcRules  groupRules
	outbound   *outbound.Env
	oidcMu     sync.Mutex
	oidc       *oidcState
	usedStates map[string]time.Time
}

// New builds an authenticator. baseURL decides whether cookies are Secure.
func New(svc *service.Service, cfg config.Auth, baseURL string, log *slog.Logger) (*Authenticator, error) {
	a := &Authenticator{
		svc: svc, cfg: cfg, log: log, baseURL: baseURL,
		loginIP: ratelimit.New(10, 10), loginUser: ratelimit.New(5, 5),
		now: func() time.Time { return time.Now().UTC() },
	}
	if u, err := url.Parse(baseURL); err == nil && u.Scheme == "https" {
		a.secure = true
	}
	for _, c := range cfg.Proxy.TrustedCIDRs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			return nil, err
		}
		a.trusted = append(a.trusted, n)
	}
	if cfg.Proxy.Enabled {
		re, err := regexp.Compile(cfg.Proxy.GroupPattern)
		if err != nil {
			return nil, err
		}
		a.groupRe = re
		a.proxyRules = groupRules{re: re, adminGroup: cfg.Proxy.InstanceAdminGroup, groupMap: cfg.Proxy.GroupMap, defaultOrg: cfg.Proxy.DefaultOrg}
	}
	// a logout URL without a return address strands people at the provider
	for _, l := range []struct{ key, url string }{{"auth.proxy.logout_url", cfg.Proxy.LogoutURL}, {"auth.oidc.logout_url", cfg.OIDC.LogoutURL}} {
		if l.url != "" && !strings.Contains(l.url, "?") {
			log.Warn("logout url has no return parameter; people stay on the provider's page after signing in again", "key", l.key, "url", l.url, "hint", "Authelia and oauth2-proxy take ?rd="+baseURL+", Keycloak ?post_logout_redirect_uri="+baseURL+"&client_id=...")
		}
	}
	if cfg.OIDC.Enabled {
		re, err := regexp.Compile(cfg.OIDC.GroupPattern)
		if err != nil {
			return nil, fmt.Errorf("auth.oidc.group_pattern: %w", err)
		}
		a.oidcRules = groupRules{re: re, adminGroup: cfg.OIDC.InstanceAdminGroup, groupMap: cfg.OIDC.GroupMap, defaultOrg: cfg.OIDC.DefaultOrg}
	}
	return a, nil
}

// ErrNeedsCode says the password matched and the code step comes next;
// the challenge cookie is set.
var ErrNeedsCode = errors.New("two-factor code needed")

// TOTPRequired reports whether every local account must have two-factor.
func (a *Authenticator) TOTPRequired() bool { return a.cfg.Local.TOTP == "required" }

// LocalEnabled reports whether the password form is available.
func (a *Authenticator) LocalEnabled() bool { return a.cfg.Local.Enabled }

// ProxyEnabled reports whether identity headers are honoured.
func (a *Authenticator) ProxyEnabled() bool { return a.cfg.Proxy.Enabled }

// GroupPattern is the regex that maps groups to roles, for the no-access page.
func (a *Authenticator) GroupPattern() string { return a.cfg.Proxy.GroupPattern }

// LogoutURL is where a proxy identity signs out, or "".
func (a *Authenticator) LogoutURL() string { return a.cfg.Proxy.LogoutURL }

// Identify resolves the principal of a UI request: the proxy identity
// when proxy mode is on and the request is trusted, else the session
// cookie when local mode is on. It returns nil, nil for anonymous.
func (a *Authenticator) Identify(r *http.Request) (*Principal, error) {
	if a.cfg.Proxy.Enabled {
		p, err := a.fromProxy(r)
		if err != nil {
			return nil, err
		}
		if p != nil {
			return p, nil
		}
	}
	if a.cfg.Local.Enabled || a.cfg.OIDC.Enabled {
		return a.fromSession(r)
	}
	return nil, nil
}

// fromProxy validates peer address and secret before reading identity.
func (a *Authenticator) fromProxy(r *http.Request) (*Principal, error) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer := net.ParseIP(host)
	refused := func(reason string) (*Principal, error) {
		if r.Header.Get(a.cfg.Proxy.UserHeader) == "" && r.Header.Get(a.cfg.Proxy.SecretHeader) == "" {
			// No proxy header at all: say which headers did arrive, so a
			// proxy that forwards nothing shows up at once.
			names := make([]string, 0, len(r.Header))
			for name := range r.Header {
				names = append(names, name)
			}
			sort.Strings(names)
			a.log.Debug("proxy mode on, but the request carries no identity", "peer", host, "path", r.URL.Path, "expects", a.cfg.Proxy.UserHeader+" and "+a.cfg.Proxy.SecretHeader, "headers", strings.Join(names, ","))
			return nil, nil
		}
		a.log.Debug("proxy identity refused", "reason", reason, "peer", host, "path", r.URL.Path)
		return nil, nil
	}
	if peer == nil || !a.trustedPeer(peer) {
		return refused("peer not in auth.proxy.trusted_cidrs")
	}
	secret := r.Header.Get(a.cfg.Proxy.SecretHeader)
	if secret == "" {
		return refused("no " + a.cfg.Proxy.SecretHeader + " header")
	}
	if subtle.ConstantTimeCompare([]byte(secret), []byte(a.cfg.Proxy.Secret)) != 1 {
		return refused(a.cfg.Proxy.SecretHeader + " does not match auth.proxy.secret")
	}
	subject := strings.TrimSpace(r.Header.Get(a.cfg.Proxy.UserHeader))
	if subject == "" {
		return refused("no " + a.cfg.Proxy.UserHeader + " header")
	}
	subject = a.normalizeSubject(subject)
	email := strings.TrimSpace(r.Header.Get(a.cfg.Proxy.EmailHeader))
	name := strings.TrimSpace(r.Header.Get(a.cfg.Proxy.NameHeader))
	groups := splitGroups(r.Header.Get(a.cfg.Proxy.GroupsHeader), a.cfg.Proxy.GroupsSeparator)
	roles, instanceAdmin := a.mapGroups(groups)

	ctx := r.Context()
	user, err := a.svc.EnsureProxyUser(ctx, subject, email, name)
	if err != nil {
		return nil, err
	}
	if err := a.svc.SyncHeaderMemberships(ctx, user.ID, roles); err != nil {
		return nil, err
	}
	memberships, err := a.svc.MembershipsForUser(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	return &Principal{User: *user, InstanceAdmin: user.InstanceAdmin || instanceAdmin, Memberships: memberships, Source: "proxy", Groups: groups}, nil
}

func (a *Authenticator) trustedPeer(ip net.IP) bool {
	for _, n := range a.trusted {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func (a *Authenticator) normalizeSubject(s string) string {
	if a.cfg.Proxy.StripRealm {
		if i := strings.IndexByte(s, '@'); i > 0 {
			s = s[:i]
		}
	}
	if a.cfg.Proxy.Lowercase {
		s = strings.ToLower(s)
	}
	return s
}

func splitGroups(raw, sep string) []string {
	if sep == "" {
		sep = ","
	}
	var out []string
	for _, g := range strings.Split(raw, sep) {
		if g = strings.TrimSpace(g); g != "" {
			out = append(out, g)
		}
	}
	return out
}

// mapGroups applies the proxy's group rules.
func (a *Authenticator) mapGroups(groups []string) (map[string]domain.Role, bool) {
	return mapGroupRules(a.proxyRules, groups)
}

func (a *Authenticator) fromSession(r *http.Request) (*Principal, error) {
	c, err := r.Cookie(CookieName)
	if err != nil || c.Value == "" {
		return nil, nil
	}
	ctx := r.Context()
	sess, err := a.svc.Session(ctx, c.Value)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	user, err := a.svc.UserByID(ctx, sess.UserID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if user.Disabled() {
		return nil, nil
	}
	a.svc.SeenSession(ctx, sess, middleware.ClientIP(r))
	memberships, err := a.svc.MembershipsForUser(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	return &Principal{User: *user, InstanceAdmin: user.InstanceAdmin, Memberships: memberships, Session: sess, Source: "session"}, nil
}

// LoginNext verifies a local password, rate-limited per IP and per
// subject, and starts a fresh session (any previous cookie is replaced),
// or opens the code step when the account has two-factor on. next is
// where the person goes afterwards.
func (a *Authenticator) LoginNext(w http.ResponseWriter, r *http.Request, subject, password, next string) (*Principal, error) {
	if !a.cfg.Local.Enabled {
		return nil, domain.ErrForbidden
	}
	subject = strings.TrimSpace(subject)
	if ok, _ := a.loginIP.Allow(middleware.ClientIP(r)); !ok {
		return nil, domain.ErrRateLimited
	}
	if ok, _ := a.loginUser.Allow(strings.ToLower(subject)); !ok {
		return nil, domain.ErrRateLimited
	}
	ctx := r.Context()
	user, err := a.svc.VerifyPassword(ctx, subject, password)
	if err != nil {
		a.log.Info("login failed", "subject", subject, "ip", middleware.ClientIP(r))
		_ = a.svc.RecordSignIn(ctx, nil, subject, "password", false)
		return nil, err
	}
	if user.TOTPOn() {
		id, err := a.svc.StartChallenge(ctx, user.ID, middleware.ClientIP(r), r.UserAgent(), next)
		if err != nil {
			return nil, err
		}
		a.setNamedCookie(w, ChallengeCookie, id, a.now().Add(service.ChallengeTTL))
		return nil, ErrNeedsCode
	}
	return a.startSession(w, r, user, "password", nil)
}

// Login is LoginNext with the home page as the destination.
func (a *Authenticator) Login(w http.ResponseWriter, r *http.Request, subject, password string) (*Principal, error) {
	return a.LoginNext(w, r, subject, password, "/")
}

// startSession replaces any session cookie with a fresh session and
// writes the sign-in to the audit log.
func (a *Authenticator) startSession(w http.ResponseWriter, r *http.Request, user *domain.User, method string, extra map[string]any) (*Principal, error) {
	ctx := r.Context()
	if old, err := r.Cookie(CookieName); err == nil && old.Value != "" {
		_ = a.svc.DeleteSession(ctx, old.Value)
	}
	sess, err := a.svc.CreateSessionWith(ctx, user.ID, middleware.ClientIP(r), r.UserAgent())
	if err != nil {
		return nil, err
	}
	a.setCookie(w, sess.ID, sess.ExpiresAt)
	memberships, err := a.svc.MembershipsForUser(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	a.log.Info("login", "subject", user.Subject, "method", method, "ip", middleware.ClientIP(r))
	if err := a.svc.RecordSignInDetail(ctx, user, user.Subject, method, true, extra); err != nil {
		return nil, err
	}
	return &Principal{User: *user, InstanceAdmin: user.InstanceAdmin, Memberships: memberships, Session: sess, Source: "session"}, nil
}

// Challenge returns the open code step of this browser, if any.
func (a *Authenticator) Challenge(r *http.Request) (*service.LoginChallenge, error) {
	c, err := r.Cookie(ChallengeCookie)
	if err != nil || c.Value == "" {
		return nil, domain.NotFound("sign-in")
	}
	return a.svc.Challenge(r.Context(), c.Value)
}

// CompleteChallenge checks the code (from the app or a recovery code) and
// starts the session. A wrong code is ErrUnauthorized; too many wrong
// codes end the attempt with service.ErrCodeLocked and clear the cookie.
func (a *Authenticator) CompleteChallenge(w http.ResponseWriter, r *http.Request, code string) (*Principal, string, error) {
	c, err := r.Cookie(ChallengeCookie)
	if err != nil || c.Value == "" {
		return nil, "", domain.NotFound("sign-in")
	}
	ctx := r.Context()
	res, err := a.svc.CompleteChallenge(ctx, c.Value, code)
	if err != nil {
		if errors.Is(err, service.ErrCodeLocked) || errors.Is(err, domain.ErrNotFound) {
			a.setNamedCookie(w, ChallengeCookie, "", time.Unix(0, 0))
		}
		if errors.Is(err, domain.ErrUnauthorized) || errors.Is(err, service.ErrCodeLocked) {
			if ch, cerr := a.svc.Challenge(ctx, c.Value); cerr == nil {
				_ = a.svc.RecordSignIn(ctx, nil, ch.Subject, "totp", false)
			}
		}
		return nil, "", err
	}
	a.setNamedCookie(w, ChallengeCookie, "", time.Unix(0, 0))
	var extra map[string]any
	if res.Method == "recovery" {
		extra = map[string]any{"left": res.Left}
	}
	p, err := a.startSession(w, r, res.User, res.Method, extra)
	return p, res.Next, err
}

// Logout ends the session and clears the cookie.
func (a *Authenticator) Logout(w http.ResponseWriter, r *http.Request) error {
	if c, err := r.Cookie(CookieName); err == nil && c.Value != "" {
		if p := PrincipalFrom(r.Context()); p != nil {
			_ = a.svc.RecordSignOut(r.Context(), &p.User)
		}
		if err := a.svc.DeleteSession(r.Context(), c.Value); err != nil {
			return err
		}
	}
	a.setCookie(w, "", time.Unix(0, 0))
	return nil
}

func (a *Authenticator) setCookie(w http.ResponseWriter, value string, exp time.Time) {
	a.setNamedCookie(w, CookieName, value, exp)
}

func (a *Authenticator) setNamedCookie(w http.ResponseWriter, name, value string, exp time.Time) {
	c := &http.Cookie{ //nolint:gosec // G124: Secure follows server.base_url's scheme; plain http is legitimate on a LAN install
		Name: name, Value: value, Path: "/", HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteLaxMode, Expires: exp,
	}
	if value == "" {
		c.MaxAge = -1
	}
	http.SetCookie(w, c)
}

// CheckCSRF validates the double-submit token for a session request. Proxy
// identities carry no session cookie, so there is nothing to forge with.
func (a *Authenticator) CheckCSRF(r *http.Request, p *Principal) bool {
	if p == nil || p.Session == nil {
		return true
	}
	token := r.Header.Get(CSRFHeader)
	if token == "" {
		token = r.PostFormValue(CSRFField)
	}
	if token == "" {
		token = r.URL.Query().Get(CSRFField)
	}
	return token != "" && subtle.ConstantTimeCompare([]byte(token), []byte(p.Session.CSRF)) == 1
}

// KeyScope resolves a bearer token to a project scope: viewer for ro
// keys, admin for rw keys.
func (a *Authenticator) KeyScope(ctx context.Context, token string) (domain.Scope, error) {
	key, err := a.svc.VerifyAPIKey(ctx, token)
	if err != nil {
		return domain.Scope{}, err
	}
	role := domain.RoleViewer
	if key.Access == domain.AccessRW {
		role = domain.RoleAdmin
	}
	if key.IsOrg() {
		// An org key: bound to the org, no project. Routes decide what
		// it may do (export and apply).
		return domain.Scope{OrgID: key.OrgID, Role: role, Actor: "key:" + key.Prefix, KeyID: key.ID, KeyAccess: key.Access}, nil
	}
	project, err := a.svc.ProjectByID(ctx, key.ProjectID)
	if err != nil {
		return domain.Scope{}, domain.ErrUnauthorized
	}
	return domain.Scope{
		OrgID: project.OrgID, ProjectID: project.ID, Role: role, Actor: "key:" + key.Prefix, KeyID: key.ID, KeyAccess: key.Access,
	}, nil
}

// UserScope binds a principal to a project. A principal without a role in
// the project's org gets ErrNotFound, never ErrForbidden, so other
// tenants' projects are indistinguishable from missing ones.
func (a *Authenticator) UserScope(p *Principal, project *domain.Project) (domain.Scope, error) {
	if p == nil {
		return domain.Scope{}, domain.ErrUnauthorized
	}
	role, ok := p.RoleIn(project.OrgID)
	if !ok {
		return domain.Scope{}, domain.NotFound("project")
	}
	return domain.Scope{
		OrgID: project.OrgID, ProjectID: project.ID, UserID: p.User.ID, Role: role, InstanceAdmin: p.InstanceAdmin, Actor: "user:" + p.User.Subject,
	}, nil
}

// OrgScope binds a principal to an org without a project, for org-level
// pages and project creation.
func (a *Authenticator) OrgScope(p *Principal, org *domain.Org) (domain.Scope, error) {
	if p == nil {
		return domain.Scope{}, domain.ErrUnauthorized
	}
	role, ok := p.RoleIn(org.ID)
	if !ok {
		return domain.Scope{}, domain.NotFound("org")
	}
	return domain.Scope{OrgID: org.ID, UserID: p.User.ID, Role: role, InstanceAdmin: p.InstanceAdmin, Actor: "user:" + p.User.Subject}, nil
}

type ctxKey int

const (
	keyPrincipal ctxKey = iota
	keyScope
)

// WithPrincipal stores p in ctx.
func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, keyPrincipal, p)
}

// PrincipalFrom returns the stored principal or nil.
func PrincipalFrom(ctx context.Context) *Principal {
	p, _ := ctx.Value(keyPrincipal).(*Principal)
	return p
}

// WithScope stores sc in ctx and tags the request log line.
func WithScope(ctx context.Context, sc domain.Scope) context.Context {
	middleware.AddLogFields(ctx, slog.String("org_id", sc.OrgID), slog.String("project_id", sc.ProjectID), slog.String("actor", sc.Actor))
	return context.WithValue(ctx, keyScope, sc)
}

// ScopeFrom returns the stored scope.
func ScopeFrom(ctx context.Context) (domain.Scope, bool) {
	sc, ok := ctx.Value(keyScope).(domain.Scope)
	return sc, ok
}

// Identity is middleware that resolves the principal for UI routes and
// stores it (possibly nil) in the context. Deciding what anonymous means
// is the handler's job: redirect to /login in local mode, 403 in proxy
// mode.
func (a *Authenticator) Identity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, err := a.Identify(r)
		if err != nil {
			a.log.Error("identify", "err", err, "req_id", middleware.GetRequestID(r.Context()))
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		if p != nil {
			middleware.AddLogFields(r.Context(), slog.String("user", p.User.Subject))
		}
		next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
	})
}

// BearerToken extracts a bearer token from the Authorization header.
func BearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	tok, ok := strings.CutPrefix(h, "Bearer ")
	if !ok || strings.TrimSpace(tok) == "" {
		return "", false
	}
	return strings.TrimSpace(tok), true
}
