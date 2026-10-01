//go:build e2e

package e2e

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/w4jnl/vink/internal/totp"
)

// browser is one person's cookie jar; it never follows redirects, so the
// test sees every 303.
type browser struct {
	t      *testing.T
	base   string
	client *http.Client
}

func newBrowser(t *testing.T, in *instance) *browser {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &browser{t: t, base: in.base, client: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (b *browser) do(method, path string, form url.Values) (int, string, http.Header) {
	b.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, b.base+path, body)
	if err != nil {
		b.t.Fatal(err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 14.0; rv:130.0) Gecko/20100101 Firefox/130.0")
	resp, err := b.client.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw), resp.Header
}

// page fetches a signed-in page and returns its body and CSRF token.
func (b *browser) page(path string) (string, string) {
	b.t.Helper()
	code, body, _ := b.do("GET", path, nil)
	if code != 200 {
		b.t.Fatalf("GET %s: %d\n%s", path, code, body)
	}
	// pages without a form carry no token; submit refuses an empty one
	if m := regexp.MustCompile(`name="_csrf" value="([^"]+)"`).FindStringSubmatch(body); m != nil {
		return body, m[1]
	}
	return body, ""
}

// submit posts a form with the token and expects a redirect to want
// (a prefix) or, with want "", a 200.
func (b *browser) submit(path, csrf string, form url.Values, want string) string {
	b.t.Helper()
	if csrf == "" {
		b.t.Fatalf("POST %s: no CSRF token in hand", path)
	}
	form.Set("_csrf", csrf)
	code, body, hdr := b.do("POST", path, form)
	if want == "" {
		if code != 200 {
			b.t.Fatalf("POST %s: %d\n%s", path, code, body)
		}
		return body
	}
	if code != 303 || !strings.HasPrefix(hdr.Get("Location"), want) {
		b.t.Fatalf("POST %s: %d %s (want %s)\n%s", path, code, hdr.Get("Location"), want, body)
	}
	return hdr.Get("Location")
}

func has(t *testing.T, what, body string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(body, p) {
			t.Errorf("%s lacks %q", what, p)
		}
	}
}

// TestSecondOrgOnboarding is the phase 3 gate: a second org onboarded
// without hand-holding, through the browser. The instance admin creates
// the org with a quota and invites its first owner; she accepts the
// invite, turns on two-factor, signs in again with a code, adds a project
// and a monitor; and all of it is in the audit log.
func TestSecondOrgOnboarding(t *testing.T) {
	in := startInstance(t)

	// the instance admin creates the org with a quota
	admin := newBrowser(t, in)
	if code, _, hdr := admin.do("POST", "/login", url.Values{"username": {"j"}, "password": {"e2e-password"}, "next": {"/"}}); code != 303 || hdr.Get("Location") != "/" {
		t.Fatalf("admin sign-in: %d %s", code, hdr.Get("Location"))
	}
	body, csrf := admin.page("/admin/orgs?add=1")
	has(t, "add org", body, `<h2>Add org</h2>`, `name="org_q_mon"`)
	admin.submit("/admin/orgs", csrf, url.Values{"org_slug": {"acme"}, "org_name": {"Acme Labs"}, "org_q_mon": {"5"}, "org_q_ag": {"1"}}, "/admin/orgs?flash=")
	body, _ = admin.page("/admin/orgs")
	has(t, "orgs tab", body, `href="/o/acme/admin/members">acme</a>`, `no projects · no owner`, `0 / 5 monitors`, `0 / 1 agents`)

	// and invites its first owner
	body, csrf = admin.page("/o/acme/admin/members?invite=1")
	has(t, "invite panel", body, `<h2>Invite a local account</h2>`, `<option value="owner">owner</option>`)
	body = admin.submit("/o/acme/admin/members/invites", csrf, url.Values{"inv_for": {"Marit"}, "inv_role": {"owner"}}, "")
	m := regexp.MustCompile(`/invite/(iv_[A-Za-z0-9]+)`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no invite link on the page\n%s", body)
	}
	token := m[1]

	// she accepts it with a new local account
	marit := newBrowser(t, in)
	code, body, _ := marit.do("GET", "/invite/"+token, nil)
	if code != 200 {
		t.Fatalf("invite page: %d", code)
	}
	has(t, "invite page", body, `<h1>Join `, `as a <b>owner</b>`, `name="username"`)
	if code, _, hdr := marit.do("POST", "/invite/"+token, url.Values{"username": {"marit"}, "display_name": {"Marit de Vries"}, "password": {"a-long-marit-passphrase"}}); code != 303 || hdr.Get("Location") != "/" {
		t.Fatalf("accept: %d %s", code, hdr.Get("Location"))
	}
	if code, _, hdr := marit.do("GET", "/invite/"+token, nil); code != 410 || hdr.Get("Location") != "" {
		t.Fatalf("used invite: %d", code)
	}
	body, _ = marit.page("/o/acme/admin/members")
	has(t, "members as the new owner", body, `<span class="vk-srow__title">Marit de Vries <span class="vk-tag">you</span></span>`, `joined as marit`, `<h2 class="vk-listhead">Owner actions</h2>`)

	// she turns on two-factor
	body, csrf = marit.page("/account?setup=1")
	has(t, "setup", body, `<h2>Set up two-factor sign-in</h2>`, `<figure class="vk-qr"><div class="vk-qr__code" role="img"`, `<svg viewBox="0 0 `, `6 digits · a new code every 30 s`)
	sm := regexp.MustCompile(`data-copy="([A-Z2-7]{32})"`).FindStringSubmatch(body)
	if sm == nil {
		t.Fatal("no key on the setup panel")
	}
	secret := sm[1]
	setupAt := time.Now()
	otp, _ := totp.Code(secret, setupAt)
	body = marit.submit("/account/totp/confirm", csrf, url.Values{"otp": {otp}}, "")
	has(t, "two-factor on", body, `<b class="vk-notice__title">Two-factor is on.</b>`, `<ol class="vk-codes__list">`, `10 of 10 recovery codes left`)
	if n := strings.Count(body, "<li>"); n != 10 {
		t.Fatalf("%d recovery codes shown", n)
	}

	// and signs in again: the password, then a fresh code
	if code, _, _ := marit.do("GET", "/logout", nil); code != 303 {
		t.Fatalf("logout: %d", code)
	}
	if code, _, hdr := marit.do("POST", "/login", url.Values{"username": {"marit"}, "password": {"a-long-marit-passphrase"}, "next": {"/o/acme/admin/projects"}}); code != 303 || hdr.Get("Location") != "/login/code" {
		t.Fatalf("password step: %d %s", code, hdr.Get("Location"))
	}
	code, body, _ = marit.do("GET", "/login/code", nil)
	if code != 200 {
		t.Fatalf("code page: %d", code)
	}
	has(t, "code page", body, `<h1>Two-factor sign-in</h1>`, `for <b>marit</b>`)
	// the setup code is spent; wait for the next step when still in it
	for totp.Step(time.Now()) == totp.Step(setupAt) {
		time.Sleep(time.Second)
	}
	otp, _ = totp.Code(secret, time.Now())
	if code, body, hdr := marit.do("POST", "/login/code", url.Values{"otp": {otp}}); code != 303 || hdr.Get("Location") != "/o/acme/admin/projects" {
		t.Fatalf("code step: %d %s\n%s", code, hdr.Get("Location"), body)
	}

	// a project and a monitor
	body, csrf = marit.page("/o/acme/admin/projects?add=1")
	has(t, "add project", body, `name="pr_slug"`)
	marit.submit("/o/acme/admin/projects", csrf, url.Values{"pr_name": {"Production"}, "pr_slug": {"prod"}, "pr_tz": {"Europe/Amsterdam"}}, "/o/acme/")
	body, csrf = marit.page("/o/acme/p/prod/m/new?kind=heartbeat")
	has(t, "monitor form", body, `name="schedule_type"`)
	marit.submit("/o/acme/p/prod/m/new", csrf, url.Values{"kind": {"heartbeat"}, "slug": {"nightly-backup"}, "name": {"Nightly backup"}, "schedule_type": {"period"}, "schedule": {"24h"}, "grace": {"1h"}, "methods": {"any"}, "failure_threshold": {"1"}, "recovery_threshold": {"1"}}, "/o/acme/p/prod")
	if code, body, _ := marit.do("GET", "/o/acme/p/prod", nil); code != 200 || !strings.Contains(body, "Nightly backup") {
		t.Fatalf("monitors page: %d", code)
	}

	// all of it is in the org's audit log, and in the instance's
	body, _ = marit.page("/o/acme/admin/audit")
	has(t, "acme audit log", body, `aria-current="page">Audit log</a>`,
		`created org <code>acme</code>`, `invited Marit as owner`, `joined as marit (owner) through the invite for Marit`,
		`turned on two-factor sign-in`, `signed in with a code`, `created project <code>prod</code>`, `created monitor <code>nightly-backup</code>`,
		`<span class="vk-tag">prod</span>`, `<span class="vk-audit__via">web</span>`, `<dt>request</dt>`)
	if strings.Contains(body, "homelab") {
		t.Error("acme's log shows homelab")
	}
	body, _ = admin.page("/admin/audit?org=acme")
	has(t, "instance audit log", body, `<option value="acme" selected>acme</option>`, `<span class="vk-tag">acme/prod</span>`, `created monitor <code>nightly-backup</code>`, `signed in with a code`)
	body, _ = admin.page("/admin/orgs")
	has(t, "orgs after onboarding", body, `1 project · owner marit`, `1 / 5 monitors`)
	body, _ = admin.page("/admin/users")
	has(t, "users after onboarding", body, `<span class="vk-srow__title">Marit de Vries</span>`, `acme owner`, `two-factor on`)
}
