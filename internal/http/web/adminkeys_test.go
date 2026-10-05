package web

import (
	"context"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/w4jnl/vink/internal/config"
	"github.com/w4jnl/vink/internal/domain"
)

// TestInstanceAdminKeys: the API keys tab makes a key, shows it once and
// never in a URL, lists keys with their creator, expiry and use, mutes
// expired ones, and revokes.
func TestInstanceAdminKeys(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	admin := domain.Scope{InstanceAdmin: true, Role: domain.RoleOwner, Actor: "test"}
	if err := e.svc.SetInstanceAdmin(ctx, admin, "j", true); err != nil {
		t.Fatal(err)
	}
	empty := e.get("/admin/keys", false)
	empty.has(t, `<a class="vk-tab" href="/admin/keys" aria-current="page">API keys<span class="vk-tab__n">0</span></a>`,
		`for <span class="vk-mono">vink admin</span> from your own machine`, `They are accepted from any address.`,
		`<form class="vk-inlineform" action="/admin/keys" method="post">`, `name="expires" value="90" checked`, `name="access" value="ro" checked`,
		`<h3>No API keys</h3>`)

	made := e.post("/admin/keys", url.Values{"name": {"laptop"}, "access": {"rw"}, "expires": {"30"}}, false)
	if made.code != 200 || made.hdr.Get("Location") != "" {
		t.Fatalf("create: %d %s", made.code, made.hdr.Get("Location"))
	}
	token := regexp.MustCompile(`vka_[a-z0-9]{8}_[a-z0-9]{32}`).FindString(made.body)
	if token == "" {
		t.Fatalf("no key in the response:\n%s", made.body)
	}
	made.has(t, `<b class="vk-notice__title">Key created.</b>`, `<code class="vk-ping__url">vink ctx add admin --server http://localhost:8080 --key `+token+`</code>`,
		`data-copy="vink ctx add admin --server http://localhost:8080 --key `+token+`"`)
	if strings.Contains(made.body, "?"+token) || strings.Contains(made.body, "/"+token) || strings.Count(made.body, token) != 4 {
		t.Errorf("the key appears %d times, or in a URL", strings.Count(made.body, token))
	}
	k, err := e.svc.VerifyAdminKey(ctx, token, "192.0.2.1")
	if err != nil || k.Access != domain.AccessRW || !k.ExpiresAt.Equal(e.now.Add(30*24*time.Hour)) {
		t.Fatalf("the key: %+v %v", k, err)
	}

	// an old one from the server host, expired since
	e.now = e.now.Add(-48 * time.Hour)
	_, _, err = e.svc.CreateAdminKey(ctx, domain.Scope{InstanceAdmin: true, Actor: "cli:admin"}, "old box", domain.AccessRO, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(48 * time.Hour)
	list := e.get("/admin/keys", false)
	list.has(t, `API keys<span class="vk-tab__n">2</span>`, `vka_`+k.Prefix+`…`, `<span class="vk-tag">rw</span>`, `j · today`, `expires 27 Oct 2026`, `used just now`,
		`server host · 25 Sep`, `expired 26 Sep`, `never used`, `Really remove?`, `Really revoke?`)
	if strings.Contains(list.body, token) {
		t.Error("the key is shown again")
	}
	if !regexp.MustCompile(`<div class="vk-srow vk-srow--muted">.{0,200}old box`).MatchString(list.body) {
		t.Errorf("the expired row is not muted:\n%s", list.body)
	}

	if r := e.post("/admin/keys", url.Values{"name": {"x"}, "access": {"ro"}, "expires": {"7"}}, false); r.code != 422 || !strings.Contains(r.body, "Pick 30, 90 or 365 days.") {
		t.Fatalf("7 days: %d", r.code)
	}
	if r := e.post("/admin/keys", url.Values{"name": {strings.Repeat("x", 200)}, "access": {"ro"}, "expires": {"90"}}, false); r.code != 422 || !strings.Contains(r.body, "At most 120 characters.") {
		t.Fatalf("long name: %d", r.code)
	}
	r := e.post("/admin/keys/"+k.ID+"/revoke", nil, false)
	if r.code != 303 || !strings.HasPrefix(r.hdr.Get("Location"), "/admin/keys?flash=") {
		t.Fatalf("revoke: %d %s", r.code, r.hdr.Get("Location"))
	}
	if _, err := e.svc.VerifyAdminKey(ctx, token, ""); err == nil {
		t.Fatal("the revoked key still works")
	}
	e.get(r.hdr.Get("Location"), false).has(t, `Key revoked. It stopped working at once.`, `API keys<span class="vk-tab__n">1</span>`)
}

// TestInstanceAdminKeysNetworks: the lede names where keys are accepted.
func TestInstanceAdminKeysNetworks(t *testing.T) {
	cfg := config.Default().Auth
	cfg.AdminKeys.AllowedCIDRs = []string{"10.0.0.0/8", "192.168.1.0/24", "fd00::/8"}
	e := newEnvAuth(t, cfg)
	if err := e.svc.SetInstanceAdmin(context.Background(), domain.Scope{InstanceAdmin: true, Actor: "test"}, "j", true); err != nil {
		t.Fatal(err)
	}
	e.get("/admin/keys", false).has(t, `They are accepted only from 10.0.0.0/8, 192.168.1.0/24 and fd00::/8.`)
}
