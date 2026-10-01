// Package oidctest is a fake OpenID Connect provider for tests: discovery,
// a JWKS, an authorization endpoint the test "visits" by parsing its URL,
// and a token endpoint that checks the PKCE verifier and signs an ID token.
package oidctest

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/w4jnl/vink/internal/config"
)

// Provider is the fake. Claims go into every ID token it signs; set
// WrongNonce to sign a token whose nonce does not match the flight.
type Provider struct {
	Server   *httptest.Server
	ClientID string
	Secret   string
	Code     string

	mu         sync.Mutex
	key        *rsa.PrivateKey
	challenge  string
	nonce      string
	Claims     map[string]any
	WrongNonce bool
	// Tokens counts token requests.
	Tokens int
}

// New starts the provider; it stops with the test.
func New(t testing.TB) *Provider {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &Provider{ClientID: "vink", Secret: "s3cret", Code: "good-code", key: key, Claims: map[string]any{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": p.Server.URL, "authorization_endpoint": p.Server.URL + "/auth", "token_endpoint": p.Server.URL + "/token",
			"jwks_uri": p.Server.URL + "/keys", "end_session_endpoint": p.Server.URL + "/logout",
			"response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"}, "id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("GET /keys", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		pub := p.key.PublicKey
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "alg": "RS256", "use": "sig", "kid": "k1",
			"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("POST /token", p.token)
	p.Server = httptest.NewServer(mux)
	t.Cleanup(p.Server.Close)
	return p
}

// Configure points an auth config at the provider.
func (p *Provider) Configure(cfg *config.Auth) {
	cfg.OIDC.Enabled = true
	cfg.OIDC.Issuer = p.Server.URL
	cfg.OIDC.ClientID = p.ClientID
	cfg.OIDC.ClientSecret = p.Secret
	cfg.OIDC.DisplayName = "Keycloak"
}

// Visit is the browser at the authorization endpoint: it remembers the
// PKCE challenge and nonce and returns the callback URL the provider
// would redirect to, with the code and the state.
func (p *Provider) Visit(t testing.TB, authURL string) string {
	t.Helper()
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("client_id") != p.ClientID || q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("nonce") == "" || q.Get("state") == "" {
		t.Fatalf("authorization request lacks something: %s", authURL)
	}
	p.mu.Lock()
	p.challenge, p.nonce = q.Get("code_challenge"), q.Get("nonce")
	p.mu.Unlock()
	cb, _ := url.Parse(q.Get("redirect_uri"))
	cq := url.Values{"code": {p.Code}, "state": {q.Get("state")}}
	return cb.Path + "?" + cq.Encode()
}

func (p *Provider) token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Tokens++
	id, secret, ok := r.BasicAuth()
	if !ok {
		id, secret = r.PostFormValue("client_id"), r.PostFormValue("client_secret")
	}
	fail := func(code string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
	}
	if id != p.ClientID || secret != p.Secret {
		fail("invalid_client")
		return
	}
	if r.PostFormValue("grant_type") != "authorization_code" || r.PostFormValue("code") != p.Code {
		fail("invalid_grant")
		return
	}
	sum := sha256.Sum256([]byte(r.PostFormValue("code_verifier")))
	if base64.RawURLEncoding.EncodeToString(sum[:]) != p.challenge {
		fail("invalid_grant")
		return
	}
	nonce := p.nonce
	if p.WrongNonce {
		nonce = "not-the-nonce"
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "token_type": "Bearer", "expires_in": 300, "id_token": p.IDToken(nonce, p.Claims)})
}

// IDToken signs a token for this provider with the claims on top of the
// standard ones.
func (p *Provider) IDToken(nonce string, claims map[string]any) string {
	now := time.Now()
	payload := map[string]any{"iss": p.Server.URL, "aud": p.ClientID, "sub": "sub-1", "iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(), "nonce": nonce}
	for k, v := range claims {
		payload[k] = v
	}
	seg := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	signing := seg(map[string]string{"alg": "RS256", "typ": "JWT", "kid": "k1"}) + "." + seg(payload)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, p.key, crypto.SHA256, sum[:])
	if err != nil {
		panic(err)
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// Host is the provider's host:port, as the sign-in page names it.
func (p *Provider) Host() string { return strings.TrimPrefix(p.Server.URL, "http://") }
