package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/http/middleware"
	"github.com/w4jnl/vink/internal/outbound"
)

// Native OIDC: the authorization-code flow with PKCE against one issuer.
// The ID token's username and groups claims become the same subject and
// roles proxy mode derives from headers, so nothing downstream changes.

// OIDCCookie carries state, nonce and the PKCE verifier between start
// and callback, signed by the instance key.
const OIDCCookie = "vink_oidc"

// oidcTTL is how long a sign-in may take at the provider.
const oidcTTL = 10 * time.Minute

// OIDCError is why a sign-in through the provider did not happen: the
// provider's own error code, or one of vink's (state_mismatch,
// state_reused, nonce_mismatch, token_exchange, no_id_token, id_token,
// no_subject, disabled, local_account). It unwraps to ErrUnauthorized.
type OIDCError struct{ Code string }

func (e OIDCError) Error() string { return "oidc: " + e.Code }
func (e OIDCError) Unwrap() error { return domain.ErrUnauthorized }

type oidcState struct {
	provider *oidc.Provider
	verifier *oidc.IDTokenVerifier
	oauth    oauth2.Config
}

type oidcFlight struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	Next     string `json:"next"`
}

// SetOutbound gives the authenticator the environment its provider calls
// go through: proxy, CA bundle and the egress log apply. The issuer is
// operator-configured, so the private-target guard does not.
func (a *Authenticator) SetOutbound(env *outbound.Env) { a.outbound = env }

// OIDCEnabled reports whether the provider sign-in is on.
func (a *Authenticator) OIDCEnabled() bool { return a.cfg.OIDC.Enabled }

// OIDCDisplayName names the provider on the sign-in page.
func (a *Authenticator) OIDCDisplayName() string {
	if a.cfg.OIDC.DisplayName == "" {
		return "single sign-on"
	}
	return a.cfg.OIDC.DisplayName
}

// OIDCAutoRedirect reports whether /login should go straight to the
// provider: on, and local accounts off.
func (a *Authenticator) OIDCAutoRedirect() bool {
	return a.cfg.OIDC.Enabled && a.cfg.OIDC.AutoRedirect && !a.cfg.Local.Enabled
}

// OIDCIssuer is the provider, for the Server tab.
func (a *Authenticator) OIDCIssuer() string { return a.cfg.OIDC.Issuer }

// OIDCLogoutURL is where an OIDC session signs out at the provider, or "".
func (a *Authenticator) OIDCLogoutURL() string { return a.cfg.OIDC.LogoutURL }

func (a *Authenticator) oidcClient() *http.Client {
	c := &http.Client{Timeout: 15 * time.Second}
	if a.outbound != nil {
		t := a.outbound.Transport.Clone()
		t.DialContext = a.outbound.DialTrusted
		c.Transport = t
	}
	return c
}

// oidcSetup fetches the provider's discovery document once and keeps it.
// A provider that is down at start does not keep vink from starting; the
// first sign-in tries again.
func (a *Authenticator) oidcSetup(ctx context.Context) (*oidcState, error) {
	a.oidcMu.Lock()
	defer a.oidcMu.Unlock()
	if a.oidc != nil {
		return a.oidc, nil
	}
	ctx = oidc.ClientContext(ctx, a.oidcClient())
	provider, err := oidc.NewProvider(ctx, a.cfg.OIDC.Issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery at %s: %w", a.cfg.OIDC.Issuer, err)
	}
	st := &oidcState{
		provider: provider,
		verifier: provider.Verifier(&oidc.Config{ClientID: a.cfg.OIDC.ClientID}),
		oauth: oauth2.Config{
			ClientID: a.cfg.OIDC.ClientID, ClientSecret: a.cfg.OIDC.ClientSecret, Endpoint: provider.Endpoint(),
			RedirectURL: strings.TrimRight(a.baseURL, "/") + "/auth/oidc/callback", Scopes: a.cfg.OIDC.Scopes,
		},
	}
	a.oidc = st
	return st, nil
}

func randomURLSafe(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// OIDCStart sets the flight cookie and returns the provider URL to send
// the browser to.
func (a *Authenticator) OIDCStart(w http.ResponseWriter, r *http.Request, next string) (string, error) {
	if !a.cfg.OIDC.Enabled {
		return "", domain.ErrForbidden
	}
	st, err := a.oidcSetup(r.Context())
	if err != nil {
		return "", err
	}
	state, err := randomURLSafe(24)
	if err != nil {
		return "", err
	}
	nonce, err := randomURLSafe(24)
	if err != nil {
		return "", err
	}
	flight := oidcFlight{State: state, Nonce: nonce, Verifier: oauth2.GenerateVerifier(), Next: next}
	payload, err := json.Marshal(flight)
	if err != nil {
		return "", err
	}
	exp := a.now().Add(oidcTTL)
	a.setNamedCookie(w, OIDCCookie, a.svc.Keyring().Sign("oidc", string(payload), exp), exp)
	return st.oauth.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(flight.Verifier)), nil
}

// oidcClaims is what vink reads from the ID token.
type oidcClaims struct {
	Subject string
	Email   string
	Name    string
	Groups  []string
}

// OIDCCallback finishes the flow: state against the cookie, the code for
// tokens with the PKCE verifier, the ID token's signature and nonce, then
// the user and their memberships. The session starts here.
func (a *Authenticator) OIDCCallback(w http.ResponseWriter, r *http.Request) (*Principal, string, error) {
	if !a.cfg.OIDC.Enabled {
		return nil, "", domain.ErrForbidden
	}
	ctx := r.Context()
	c, err := r.Cookie(OIDCCookie)
	if err != nil || c.Value == "" {
		return nil, "", domain.NotFound("sign-in")
	}
	a.setNamedCookie(w, OIDCCookie, "", time.Unix(0, 0))
	payload, err := a.svc.Keyring().Verify("oidc", c.Value, a.now())
	if err != nil {
		return nil, "", domain.NotFound("sign-in")
	}
	var flight oidcFlight
	if err := json.Unmarshal([]byte(payload), &flight); err != nil {
		return nil, "", domain.NotFound("sign-in")
	}
	q := r.URL.Query()
	if code := q.Get("error"); code != "" {
		a.log.Info("oidc refused", "error", code, "description", q.Get("error_description"))
		return nil, "", OIDCError{Code: code}
	}
	if q.Get("state") == "" || q.Get("state") != flight.State {
		a.log.Info("oidc state mismatch", "ip", middleware.ClientIP(r))
		return nil, "", OIDCError{Code: "state_mismatch"}
	}
	if !a.markState(flight.State) {
		a.log.Info("oidc state reused", "ip", middleware.ClientIP(r))
		return nil, "", OIDCError{Code: "state_reused"}
	}
	st, err := a.oidcSetup(ctx)
	if err != nil {
		return nil, "", err
	}
	ctx = oidc.ClientContext(ctx, a.oidcClient())
	token, err := st.oauth.Exchange(ctx, q.Get("code"), oauth2.VerifierOption(flight.Verifier))
	if err != nil {
		a.log.Info("oidc token exchange failed", "err", err)
		return nil, "", OIDCError{Code: "token_exchange"}
	}
	raw, _ := token.Extra("id_token").(string)
	if raw == "" {
		return nil, "", OIDCError{Code: "no_id_token"}
	}
	idToken, err := st.verifier.Verify(ctx, raw)
	if err != nil {
		a.log.Info("oidc id token refused", "err", err)
		return nil, "", OIDCError{Code: "id_token"}
	}
	if idToken.Nonce != flight.Nonce {
		a.log.Info("oidc nonce mismatch", "ip", middleware.ClientIP(r))
		return nil, "", OIDCError{Code: "nonce_mismatch"}
	}
	claims, err := a.readClaims(idToken)
	if err != nil {
		return nil, "", err
	}
	roles, admin := mapGroupRules(a.oidcRules, claims.Groups)
	user, err := a.svc.EnsureExternalUser(ctx, claims.Subject, claims.Email, claims.Name, "oidc")
	if err != nil {
		return nil, "", err
	}
	if user.Disabled() {
		_ = a.svc.RecordSignIn(ctx, user, user.Subject, "oidc", false)
		return nil, "", OIDCError{Code: "disabled"}
	}
	if user.Source == "local" {
		// a local account with this name is not the provider's to claim
		a.log.Warn("oidc subject collides with a local account", "subject", user.Subject)
		_ = a.svc.RecordSignIn(ctx, nil, claims.Subject, "oidc", false)
		return nil, "", OIDCError{Code: "local_account"}
	}
	if user.Source == "proxy" {
		// the same identity, seen through headers until now
		if err := a.svc.AdoptProviderUser(ctx, user, "oidc"); err != nil {
			return nil, "", err
		}
	}
	if err := a.applyRoles(ctx, user, "oidc", a.cfg.OIDC.Roles, a.oidcAdmins, roles, admin); err != nil {
		return nil, "", err
	}
	p, err := a.startSession(w, r, user, "oidc", nil)
	if err != nil {
		return nil, "", err
	}
	p.Groups = claims.Groups
	return p, flight.Next, nil
}

func (a *Authenticator) readClaims(idToken *oidc.IDToken) (*oidcClaims, error) {
	all := map[string]any{}
	if err := idToken.Claims(&all); err != nil {
		return nil, OIDCError{Code: "id_token"}
	}
	str := func(key string) string {
		s, _ := all[key].(string)
		return strings.TrimSpace(s)
	}
	out := &oidcClaims{Subject: str(a.cfg.OIDC.UsernameClaim), Email: str("email"), Name: str("name")}
	if out.Subject == "" {
		out.Subject = strings.TrimSpace(idToken.Subject)
	}
	if out.Subject == "" {
		return nil, OIDCError{Code: "no_subject"}
	}
	if a.cfg.OIDC.StripRealm {
		if i := strings.IndexByte(out.Subject, '@'); i > 0 {
			out.Subject = out.Subject[:i]
		}
	}
	if a.cfg.OIDC.Lowercase {
		out.Subject = strings.ToLower(out.Subject)
	}
	switch g := all[a.cfg.OIDC.GroupsClaim].(type) {
	case []any:
		for _, v := range g {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				out.Groups = append(out.Groups, strings.TrimSpace(s))
			}
		}
	case string:
		out.Groups = splitGroups(g, ",")
	}
	return out, nil
}

// groupRules is one provider's mapping from groups to roles.
type groupRules struct {
	re         *regexp.Regexp
	adminGroup string
	groupMap   map[string]string
	defaultOrg string
}

// mapGroupRules turns group names into org roles: the explicit group_map
// first, then the group_pattern regex; the highest role per org wins.
func mapGroupRules(rules groupRules, groups []string) (map[string]domain.Role, bool) {
	roles := map[string]domain.Role{}
	admin := false
	grant := func(org string, role domain.Role) {
		if !role.Valid() || org == "" {
			return
		}
		if cur, ok := roles[org]; !ok || role.Level() > cur.Level() {
			roles[org] = role
		}
	}
	for _, g := range groups {
		if rules.adminGroup != "" && g == rules.adminGroup {
			admin = true
			continue
		}
		if mapped, ok := rules.groupMap[g]; ok {
			org, role, found := strings.Cut(mapped, ":")
			if found {
				grant(org, domain.Role(role))
			}
			continue
		}
		if rules.re == nil {
			continue
		}
		m := rules.re.FindStringSubmatch(g)
		if m == nil {
			continue
		}
		grant(m[rules.re.SubexpIndex("org")], domain.Role(m[rules.re.SubexpIndex("role")]))
	}
	if rules.defaultOrg != "" {
		if _, ok := roles[rules.defaultOrg]; !ok {
			roles[rules.defaultOrg] = domain.RoleViewer
		}
	}
	return roles, admin
}

// markState records a state as used; false when it was seen already. The
// set is pruned as it grows, since flights last oidcTTL.
func (a *Authenticator) markState(state string) bool {
	a.oidcMu.Lock()
	defer a.oidcMu.Unlock()
	now := a.now()
	if a.usedStates == nil {
		a.usedStates = map[string]time.Time{}
	}
	for s, at := range a.usedStates {
		if now.Sub(at) > oidcTTL {
			delete(a.usedStates, s)
		}
	}
	if _, seen := a.usedStates[state]; seen {
		return false
	}
	a.usedStates[state] = now
	return true
}

// OIDCStartURL is the path that begins the flow, with where to go after.
func OIDCStartURL(next string) string {
	if next == "" || next == "/" {
		return "/auth/oidc/start"
	}
	return "/auth/oidc/start?next=" + url.QueryEscape(next)
}
