package web

import (
	"context"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/w4jnl/vink/internal/audit"
	"github.com/w4jnl/vink/internal/auth"
	"github.com/w4jnl/vink/internal/auth/oidctest"
	"github.com/w4jnl/vink/internal/checks"
	"github.com/w4jnl/vink/internal/config"
	"github.com/w4jnl/vink/internal/db"
	"github.com/w4jnl/vink/internal/db/dbtest"
	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/http/middleware"
	"github.com/w4jnl/vink/internal/notify"
	"github.com/w4jnl/vink/internal/outbound"
	"github.com/w4jnl/vink/internal/service"
	"github.com/w4jnl/vink/internal/totp"
)

type env struct {
	t       *testing.T
	db      *db.DB
	svc     *service.Service
	authn   *auth.Authenticator
	web     *Web
	srv     http.Handler
	project *domain.Project
	org     *domain.Org
	scope   domain.Scope
	cookie  *http.Cookie
	csrf    string
	now     time.Time
}

func newEnv(t *testing.T) *env {
	t.Helper()
	return newEnvAuth(t, config.Default().Auth)
}

// newEnvAuth is newEnv with its own auth config.
func newEnvAuth(t *testing.T, authCfg config.Auth) *env {
	t.Helper()
	d := dbtest.Open(t)
	logOut := io.Discard
	if os.Getenv("VINK_TEST_LOG") != "" {
		logOut = os.Stderr
	}
	quiet := slog.New(slog.NewTextHandler(logOut, nil))
	svc := service.New(d, nil, quiet, service.DefaultConfig())
	reg, err := notify.NewRegistry(notify.Options{AllowPrivateTargets: true})
	if err != nil {
		t.Fatal(err)
	}
	svc.SetNotifier(reg)
	e := &env{t: t, db: d, svc: svc, now: time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)}
	svc.SetClock(func() time.Time { return e.now })
	ctx := context.Background()
	admin := domain.Scope{InstanceAdmin: true, Role: domain.RoleOwner}
	e.org, _ = svc.CreateOrg(ctx, admin, "homelab", "Homelab")
	e.project, _ = svc.CreateProject(ctx, admin, e.org.ID, "prod", "Production", "Europe/Amsterdam")
	other, _ := svc.CreateOrg(ctx, admin, "acme", "Acme")
	_, _ = svc.CreateProject(ctx, admin, other.ID, "prod", "Acme", "UTC")
	user, _ := svc.CreateLocalUser(ctx, admin, "j", "j@example.com", "Jaro", "correct horse", false)
	_ = svc.SetMembership(ctx, admin, user.ID, e.org.ID, domain.RoleAdmin)
	e.scope = domain.Scope{OrgID: e.org.ID, ProjectID: e.project.ID, UserID: user.ID, Role: domain.RoleAdmin, Actor: "test"}
	authn, err := auth.New(svc, authCfg, "http://localhost:8080", quiet)
	if err != nil {
		t.Fatal(err)
	}
	e.authn = authn
	e.web, err = New(svc, authn, quiet)
	if err != nil {
		t.Fatal(err)
	}
	e.web.SetClock(func() time.Time { return e.now })
	mux := http.NewServeMux()
	e.web.Mount(mux)
	e.srv = middleware.Chain(e.web.CustomDomains(mux), middleware.RequestID)

	if !authCfg.Local.Enabled {
		return e
	}
	rec := httptest.NewRecorder()
	p, err := authn.Login(rec, httptest.NewRequest("POST", "/login", nil), "j", "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	e.cookie = rec.Result().Cookies()[0]
	e.csrf = p.CSRF()
	return e
}

type page struct {
	code int
	body string
	hdr  http.Header
}

func (p page) has(t *testing.T, parts ...string) {
	t.Helper()
	for _, s := range parts {
		if !strings.Contains(p.body, s) {
			t.Errorf("body lacks %q\n---\n%s", s, p.body)
		}
	}
}

func (e *env) get(path string, htmx bool) page {
	return e.do("GET", path, nil, htmx, true)
}

func (e *env) post(path string, form url.Values, htmx bool) page {
	if form == nil {
		form = url.Values{}
	}
	form.Set("_csrf", e.csrf)
	return e.do("POST", path, form, htmx, true)
}

func (e *env) do(method, path string, form url.Values, htmx, signedIn bool) page {
	e.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, path, body)
	req.RemoteAddr = "203.0.113.9:1"
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if htmx {
		req.Header.Set("HX-Request", "true")
	}
	if signedIn {
		req.AddCookie(e.cookie)
	}
	rec := httptest.NewRecorder()
	e.srv.ServeHTTP(rec, req)
	return page{code: rec.Code, body: rec.Body.String(), hdr: rec.Header()}
}

func (e *env) monitor(slug string, tags ...string) *domain.Monitor {
	e.t.Helper()
	m, err := e.svc.CreateMonitor(context.Background(), e.scope, &domain.Monitor{Slug: slug, Name: strings.ToUpper(slug[:1]) + slug[1:], Kind: domain.KindHeartbeat, Tags: tags,
		Heartbeat: &domain.HeartbeatSpec{Schedule: domain.Schedule{Period: domain.MustDuration("1h")}, Grace: domain.MustDuration("5m")}})
	if err != nil {
		e.t.Fatal(err)
	}
	return m
}

const projPath = "/o/homelab/p/prod"

func TestLoginRedirectsAndSignsIn(t *testing.T) {
	e := newEnv(t)
	p := e.do("GET", projPath, nil, false, false)
	if p.code != 303 || p.hdr.Get("Location") != "/login?next=%2Fo%2Fhomelab%2Fp%2Fprod" {
		t.Fatalf("anonymous: %d %s", p.code, p.hdr.Get("Location"))
	}
	p = e.do("GET", "/login", nil, false, false)
	if p.code != 200 {
		t.Fatalf("login form: %d", p.code)
	}
	p.has(t, `name="username"`, `type="password"`, "Sign in", `<span>vink</span>`, `class="vk-auth__card"`, "w4j.nl")
	if strings.Contains(p.body, "vk-top") {
		t.Error("the auth layout has no top bar")
	}
	if p.hdr.Get("Content-Security-Policy") == "" || p.hdr.Get("X-Frame-Options") != "DENY" {
		t.Errorf("security headers: %v", p.hdr)
	}
	bad := e.do("POST", "/login", url.Values{"username": {"j"}, "password": {"nope"}, "next": {projPath}}, false, false)
	if bad.code != 401 || !strings.Contains(bad.body, "Wrong username or password") {
		t.Fatalf("bad login: %d", bad.code)
	}
	good := e.do("POST", "/login", url.Values{"username": {"j"}, "password": {"correct horse"}, "next": {"//evil.example"}}, false, false)
	if good.code != 303 || good.hdr.Get("Location") != "/" {
		t.Fatalf("good login must redirect to a safe path: %d %s", good.code, good.hdr.Get("Location"))
	}
	// home goes to the only project
	if p := e.get("/", false); p.code != 303 || p.hdr.Get("Location") != projPath {
		t.Fatalf("home: %d %s", p.code, p.hdr.Get("Location"))
	}
	if p := e.get("/projects", false); p.code != 200 || !strings.Contains(p.body, "homelab / prod") {
		t.Fatalf("projects page: %d", p.code)
	}
	out := e.post("/logout", nil, false)
	if out.code != 303 || out.hdr.Get("Location") != "/login" {
		t.Fatalf("logout: %d %s", out.code, out.hdr.Get("Location"))
	}
}

func TestTopBarMenusAndOrgShell(t *testing.T) {
	e := newEnv(t)
	e.monitor("job", "prod")
	p := e.get(projPath, false)
	p.has(t, `<details class="vk-popover vk-top__switch"><summary class="vk-top__crumb" title="Switch project">homelab / <b>prod</b>`, `class="vk-menu"`, `<div class="vk-menu__label"><span>homelab</span><span class="vk-tag">admin</span></div>`,
		`<a class="vk-menu__item" href="/o/homelab/p/prod" aria-current="page"><span>prod</span><span class="vk-menu__meta"><span class="vk-counts"><span class="vk-counts__n vk-counts__n--up">`, "all up",
		`<a class="vk-menu__item vk-menu__item--quiet" href="/o/homelab/admin/projects?add=1"><span>New project</span></a>`, `href="/o/homelab/admin/projects"><span>Org settings</span>`,
		`<details class="vk-popover vk-popover--end"><summary class="vk-top__user" title="Jaro">J</summary>`, `<span>Jaro</span>`, `href="/api/v1/openapi.yaml"><span>API reference</span>`, `href="/logout"><span>Sign out</span>`)
	if strings.Contains(p.body, "Instance admin") || strings.Contains(p.body, `vk-tag">instance admin`) {
		t.Error("an org admin is not an instance admin")
	}
	// the org shell: tabs, no current section, the switcher still on the project
	if r := e.get("/o/homelab/admin", false); r.code != 303 || r.hdr.Get("Location") != "/o/homelab/admin/projects" {
		t.Fatalf("org admin home: %d %s", r.code, r.hdr.Get("Location"))
	}
	shell := e.get("/o/homelab/admin/projects", false)
	if shell.code != 200 {
		t.Fatalf("org shell: %d %s", shell.code, shell.body)
	}
	shell.has(t, `<h1>homelab</h1><span class="vk-muted vk-mono">org settings</span>`, `aria-label="Org settings"`, `aria-current="page">Projects<span class="vk-tab__n">1</span>`, `href="/o/homelab/admin/agents">Agents<span class="vk-tab__n">0</span></a>`, `homelab / <b>prod</b>`, `class="vk-top__link" href="/o/homelab/p/prod">Monitors</a>`)
	if strings.Contains(shell.body, `vk-top__link" href="/o/homelab/p/prod" aria-current`) {
		t.Error("no section is current on an org page")
	}
	if r := e.get("/o/homelab/admin/nope", false); r.code != 404 {
		t.Fatalf("unknown tab: %d", r.code)
	}
	if r := e.get("/o/acme/admin/projects", false); r.code != 404 {
		t.Fatalf("foreign org: %d", r.code)
	}
	// a member of the org without the role gets a 403; sign out is a link
	ctx := context.Background()
	admin := domain.Scope{InstanceAdmin: true, Role: domain.RoleOwner}
	member, _ := e.svc.CreateLocalUser(ctx, admin, "m", "m@example.com", "M", "correct horse", false)
	_ = e.svc.SetMembership(ctx, admin, member.ID, e.org.ID, domain.RoleMember)
	rec := httptest.NewRecorder()
	if _, err := e.authn.Login(rec, httptest.NewRequest("POST", "/login", nil), "m", "correct horse"); err != nil {
		t.Fatal(err)
	}
	e.cookie = rec.Result().Cookies()[0]
	if r := e.get("/o/homelab/admin/projects", false); r.code != 403 {
		t.Fatalf("member on org settings: %d", r.code)
	}
	if r := e.get(projPath, false); strings.Contains(r.body, "New project") {
		t.Error("members get no admin items")
	}
	if r := e.get("/logout", false); r.code != 303 || r.hdr.Get("Location") != "/login" {
		t.Fatalf("sign out link: %d %s", r.code, r.hdr.Get("Location"))
	}
}

func TestNoOrgYetPage(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	admin := domain.Scope{InstanceAdmin: true, Role: domain.RoleOwner}
	if _, err := e.svc.CreateLocalUser(ctx, admin, "nobody", "n@example.com", "", "correct horse", false); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	if _, err := e.authn.Login(rec, httptest.NewRequest("POST", "/login", nil), "nobody", "correct horse"); err != nil {
		t.Fatal(err)
	}
	e.cookie = rec.Result().Cookies()[0]
	p := e.get("/", false)
	if p.code != 403 {
		t.Fatalf("no org: %d", p.code)
	}
	p.has(t, "not in an org yet", "<dt>user</dt><dd>nobody</dd>", "vink admin user create", ">Sign out<", `id="logout-form"`)
	if p := e.get(projPath, false); p.code != 404 {
		t.Fatalf("a project the user cannot see: %d", p.code)
	}
}

func TestProxyDeniedPage(t *testing.T) {
	e := newEnv(t)
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := config.Default().Auth
	cfg.Local.Enabled = false
	cfg.Proxy.Enabled = true
	cfg.Proxy.TrustedCIDRs = []string{"203.0.113.0/24"}
	cfg.Proxy.UserHeader = "X-User"
	cfg.Proxy.GroupsHeader = "X-Groups"
	cfg.Proxy.SecretHeader = "X-Proxy-Secret"
	cfg.Proxy.Secret = "s3cret"
	authn, err := auth.New(e.svc, cfg, "http://localhost:8080", quiet)
	if err != nil {
		t.Fatal(err)
	}
	w, err := New(e.svc, authn, quiet)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	w.Mount(mux)
	srv := middleware.Chain(mux, middleware.RequestID)
	get := func(path string, headers map[string]string) page {
		req := httptest.NewRequest("GET", path, nil)
		req.RemoteAddr = "203.0.113.9:1"
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return page{code: rec.Code, body: rec.Body.String(), hdr: rec.Header()}
	}
	denied := get(projPath, nil)
	if denied.code != 403 {
		t.Fatalf("no identity: %d", denied.code)
	}
	denied.has(t, "No identity from the proxy", "<dt>status</dt><dd>403</dd>", "<dt>request</dt>", "Copy details", ">Try again<")
	if strings.Contains(denied.body, "vk-top") || strings.Contains(denied.body, "Sign in") {
		t.Error("proxy denied must be one card without top bar or login form")
	}
	noOrg := get("/", map[string]string{"X-Proxy-Secret": "s3cret", "X-User": "stranger", "X-Groups": "staff,ops"})
	if noOrg.code != 403 {
		t.Fatalf("no org via proxy: %d", noOrg.code)
	}
	noOrg.has(t, "not in an org yet", "<dt>user</dt><dd>stranger</dd>", "<dt>groups</dt><dd>staff, ops</dd>", "vink:&lt;org&gt;:&lt;role&gt;")
}

func TestMonitorsPageEmptyAndRows(t *testing.T) {
	e := newEnv(t)
	p := e.get(projPath, false)
	if p.code != 200 {
		t.Fatalf("monitors: %d %s", p.code, p.body)
	}
	p.has(t, `<h1>Monitors</h1>`, `class="vk-empty"`, e.project.PingKey, `homelab / <b>prod</b>`, `Create monitor`, `data-down-count="0"`, `/static/`,
		`class="vk-top__link" href="/o/homelab/p/prod" aria-current="page">Monitors</a>`, `href="/o/homelab/p/prod/incidents">Incidents</a>`, `href="/o/homelab/p/prod/settings/channels">Settings</a>`, `vk-top__search"`, `title="Jaro"`)
	if strings.Contains(p.body, "http://") && !strings.Contains(p.body, "http://localhost:8080/ping/") {
		t.Error("unexpected external URL")
	}
	e.monitor("nightly", "backup", "prod")
	e.monitor("hourly", "prod")
	e.now = e.now.Add(70 * time.Minute)
	ids, _ := e.svc.ListDue(context.Background(), e.now, 10)
	for _, id := range ids {
		_ = e.svc.Tick(context.Background(), id, e.now)
	}
	p = e.get(projPath, false)
	p.has(t, `class="vk-row"`, `vk-glyph--down`, "Nightly", "Hourly", `hx-get="/o/homelab/p/prod/m/nightly"`, `vk-chip`, `data-down-count="2"`, `favicon-down.svg`, `title="2 open"`,
		`<span class="vk-row__tags"><span class="vk-tag">backup</span><span class="vk-tag">prod</span></span>`, `class="vk-page vk-page--full" id="page"`)
	if strings.Contains(p.body, `id="drawer"`) || strings.Contains(p.body, "No monitor selected") {
		t.Error("with nothing selected the page has no drawer")
	}
	if empty := e.get(projPath+"?partial=drawer-empty", true); !strings.Contains(empty.body, `data-empty="1"`) {
		t.Errorf("empty drawer partial: %s", empty.body)
	}
	// down sorts first, and chips carry counts
	if !strings.Contains(p.body, `>down<span class="vk-chip__n">2</span>`) || !strings.Contains(p.body, `>prod<span class="vk-chip__n">2</span>`) {
		t.Errorf("chips: %s", p.body)
	}
	// filters
	f := e.get(projPath+"?tag=backup", false)
	if !strings.Contains(f.body, "Nightly") || strings.Contains(f.body, `href="/o/homelab/p/prod/m/hourly"`) {
		t.Errorf("tag filter")
	}
	none := e.get(projPath+"?q=zzz", false)
	none.has(t, "No monitors match", `value="zzz"`)
	// partials and ETag
	list := e.get(projPath+"?partial=list", false)
	if list.code != 200 || !strings.HasPrefix(list.body, `<div class="vk-list"`) || list.hdr.Get("ETag") == "" {
		t.Fatalf("list partial: %d %q", list.code, list.body[:40])
	}
	req := httptest.NewRequest("GET", projPath+"?partial=list", nil)
	req.AddCookie(e.cookie)
	req.Header.Set("If-None-Match", list.hdr.Get("ETag"))
	rec := httptest.NewRecorder()
	e.srv.ServeHTTP(rec, req)
	if rec.Code != 304 {
		t.Fatalf("etag: %d", rec.Code)
	}
	main := e.get(projPath+"?partial=main", true)
	if !strings.HasPrefix(main.body, `<div class="vk-main__head">`) {
		t.Errorf("main partial: %q", main.body[:40])
	}
}

func TestDrawerAndActions(t *testing.T) {
	e := newEnv(t)
	m := e.monitor("nightly", "backup")
	ctx := context.Background()
	tgt, _ := e.svc.ResolvePing(ctx, e.project.PingKey, "nightly", "", false)
	_, _, _ = e.svc.RecordPing(ctx, tgt, service.PingObservation{Signal: domain.SignalStart, RunID: "r1"})
	e.now = e.now.Add(4 * time.Minute)
	_, _, _ = e.svc.RecordPing(ctx, tgt, service.PingObservation{Signal: domain.SignalOK, RunID: "r1", Body: []byte("x"), RemoteAddr: "192.168.30.5", UserAgent: "curl/8.4.0"})
	full := e.get(projPath+"/m/nightly", false)
	if full.code != 200 {
		t.Fatalf("drawer page: %d", full.code)
	}
	full.has(t, `<title>Nightly · vink</title>`, `class="vk-page" id="page"`, `<aside class="vk-drawer" id="drawer">`, `aria-current="true"`, `vk-drawer__title">Nightly`, e.project.PingKey+"/<b>nightly</b>", "every 1h · grace 5m · due in", "Last 24 hours", `vk-obs`, "4m0s", "new → up · first ok",
		`data-drawer-close`, `>Edit<`, `>Pause<`, "As YAML", "slug", `vk-codebox`,
		`<details class="vk-obsrow"><summary class="vk-obs">`, `<span title="curl/8.4.0">ok · from 192.168.30.5</span><span>4m0s · body</span></summary><div class="vk-obsrow__body"><dl class="vk-kv"><dt>from</dt><dd>192.168.30.5</dd><dt>agent</dt><dd>curl/8.4.0</dd><dt>run</dt><dd>r1</dd><dt>took</dt><dd>4m0s</dd></dl>`,
		`?partial=1" hx-trigger="revealed" hx-swap="outerHTML"><pre class="vk-code"><a class="vk-link" href="/o/homelab/p/prod/m/nightly/obs/`, `<span>start</span><span></span>`)
	// the stored body is a link to plain text, never rendered, and scoped to the project
	bodyPath := regexp.MustCompile(`href="(/o/homelab/p/prod/m/nightly/obs/[A-Z0-9]+/body)"`).FindStringSubmatch(full.body)
	if bodyPath == nil {
		t.Fatal("no body link in the drawer")
	}
	if r := e.get(bodyPath[1], false); r.code != 200 || r.body != "x" || r.hdr.Get("Content-Type") != "text/plain; charset=utf-8" || r.hdr.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("body: %d %q %v", r.code, r.body, r.hdr)
	}
	// the panel loads the same body as a code box with Copy
	if r := e.get(bodyPath[1]+"?partial=1", true); r.code != 200 || !strings.Contains(r.body, `<pre class="vk-code">x</pre>`) || !strings.Contains(r.body, `data-copy="x"`) || strings.Contains(r.body, "<html") {
		t.Fatalf("body panel: %d %q", r.code, r.body)
	}
	if r := e.get(strings.Replace(bodyPath[1], "/o/homelab/p/prod/", "/o/acme/p/prod/", 1), false); r.code != 404 {
		t.Fatalf("body across tenants: %d", r.code)
	}
	partial := e.get(projPath+"/m/nightly", true)
	if !strings.HasPrefix(partial.body, `<div id="drawer-body"`) || strings.Contains(partial.body, "<html") {
		t.Errorf("htmx drawer must be the partial only")
	}
	// pause via htmx returns the drawer with Resume
	paused := e.post(projPath+"/m/nightly/pause", nil, true)
	if paused.code != 200 || !strings.Contains(paused.body, ">Resume<") || !strings.Contains(paused.body, "vk-state--paused") {
		t.Fatalf("pause: %d %s", paused.code, paused.body)
	}
	resumed := e.post(projPath+"/m/nightly/resume", nil, false)
	if resumed.code != 303 || resumed.hdr.Get("Location") != projPath+"/m/nightly" {
		t.Fatalf("resume redirect: %d %s", resumed.code, resumed.hdr.Get("Location"))
	}
	// missing CSRF is refused
	req := httptest.NewRequest("POST", projPath+"/m/nightly/pause", nil)
	req.AddCookie(e.cookie)
	rec := httptest.NewRecorder()
	e.srv.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("no csrf: %d", rec.Code)
	}
	// delete via htmx redirects with HX-Redirect
	del := e.post(projPath+"/m/nightly/delete", nil, true)
	if del.code != 204 || del.hdr.Get("HX-Redirect") != projPath {
		t.Fatalf("delete: %d %v", del.code, del.hdr)
	}
	if _, err := e.svc.MonitorBySlug(ctx, e.scope, m.Slug); err == nil {
		t.Fatal("monitor still exists")
	}
	if p := e.get(projPath+"/m/nightly", false); p.code != 404 || !strings.Contains(p.body, "Not found") {
		t.Fatalf("deleted monitor page: %d", p.code)
	}
}

func TestCreateAndEditForm(t *testing.T) {
	e := newEnv(t)
	form := e.get(projPath+"/m/new", false)
	if form.code != 200 {
		t.Fatalf("new form: %d", form.code)
	}
	form.has(t, `id="monitor-form"`, `for="name"`, `for="slug"`, `for="schedule"`, `name="schedule_type"`, `for="timezone"`, `Europe/Amsterdam (project)`, `for="grace"`, `for="tags"`,
		`vk-details__title">Advanced</span>`, "tolerance 30s · max runtime none · down after 1 · methods any · body 64 KB", `for="tolerance"`, `for="body_limit"`, "Ping URL", "As YAML", "kind: heartbeat", `Create monitor`, `hx-post="/o/homelab/p/prod/m/preview"`)
	// the kind switch keeps the name and shows the kind's fields
	sw := e.get(projPath+"/m/new?kind=http&name=Web", true)
	sw.has(t, `value="Web"`, `for="url"`, `value="200-299"`)
	// validation: no schedule, bad grace
	bad := e.post(projPath+"/m/new", url.Values{"name": {"Nightly backup"}, "grace": {"10s"}}, true)
	if bad.code != 422 {
		t.Fatalf("validation: %d", bad.code)
	}
	bad.has(t, `vk-field--error`, `id="grace-msg"`, "Must be at least 1m.", `id="schedule-msg"`, "Set period or cron.")
	if strings.Contains(bad.body, "<html") {
		t.Error("htmx validation response must be the partial")
	}
	unparsable := e.post(projPath+"/m/new", url.Values{"name": {"X"}, "schedule_type": {"period"}, "schedule": {"soon"}, "failure_threshold": {"many"}}, false)
	if unparsable.code != 422 || !strings.Contains(unparsable.body, "Use a duration such as 1h or 1d.") || !strings.Contains(unparsable.body, `<details class="vk-details" open>`) {
		t.Fatalf("parse errors: %d", unparsable.code)
	}
	// the preview answers with partials for the hints and the YAML
	pv := e.post(projPath+"/m/preview", url.Values{"name": {"Nightly backup"}, "schedule_type": {"cron"}, "schedule": {"0 3 * * *"}, "grace": {"5m"}}, true)
	if pv.code != 200 {
		t.Fatalf("preview: %d %s", pv.code, pv.body)
	}
	pv.has(t, `<hx-partial hx-target="#schedule-msg"`, "Next runs:", `hx-target="#grace-msg"`, "Late at 03:00:30, down at 03:05.", `hx-target="#monitor-form .vk-codebox"`, "nightly-backup", `cron: &quot;0 3 * * *&quot;`)
	// the ping URL follows the slug, derived from the name when none is typed
	named := e.post(projPath+"/m/preview", url.Values{"name": {"Photo sync"}, "schedule_type": {"period"}, "schedule": {"1h"}}, true)
	named.has(t, `<hx-partial hx-target="#monitor-form .vk-ping" hx-swap="outerHTML"><div class="vk-ping"><code class="vk-ping__url">`, "/<b>photo-sync</b></code>")
	typed := e.post(projPath+"/m/preview", url.Values{"name": {"Photo sync"}, "slug": {"photos-nas"}, "schedule_type": {"period"}, "schedule": {"1h"}}, true)
	typed.has(t, "/<b>photos-nas</b></code>")
	// a tolerance over the grace is refused with the field under Advanced open
	tooTolerant := e.post(projPath+"/m/new", url.Values{"name": {"Nightly backup"}, "schedule_type": {"cron"}, "schedule": {"0 3 * * *"}, "grace": {"30m"}, "tolerance": {"1h"}}, true)
	if tooTolerant.code != 422 || !strings.Contains(tooTolerant.body, "Must be at most the grace (30m).") || !strings.Contains(tooTolerant.body, `<details class="vk-details" open>`) {
		t.Fatalf("tolerance over grace: %d %s", tooTolerant.code, tooTolerant.body)
	}
	ok := e.post(projPath+"/m/new", url.Values{"name": {"Nightly backup"}, "schedule_type": {"cron"}, "schedule": {"0 3 * * *"}, "grace": {"30m"}, "tolerance": {"2m"}, "tags": {"Backup, prod"}, "max_runtime": {"2h"}}, true)
	if ok.code != 204 || ok.hdr.Get("HX-Redirect") != projPath+"/m/nightly-backup" {
		t.Fatalf("create: %d %v %s", ok.code, ok.hdr, ok.body)
	}
	m, err := e.svc.MonitorBySlug(context.Background(), e.scope, "nightly-backup")
	if err != nil || m.Heartbeat.Schedule.Cron != "0 3 * * *" || m.Heartbeat.Grace.String() != "30m" || m.Heartbeat.Tolerance.String() != "2m" || len(m.Tags) != 2 || m.Heartbeat.MaxRuntime.String() != "2h" {
		t.Fatalf("created monitor: %+v %v", m, err)
	}
	dup := e.post(projPath+"/m/new", url.Values{"name": {"Nightly backup"}, "schedule_type": {"period"}, "schedule": {"1h"}}, false)
	if dup.code != 422 || !strings.Contains(dup.body, "A monitor with this slug exists.") {
		t.Fatalf("duplicate: %d", dup.code)
	}
	edit := e.get(projPath+"/m/nightly-backup/edit", false)
	edit.has(t, `value="0 3 * * *"`, `value="30m"`, `disabled`, `<details class="vk-details" open>`, `>Save changes<`, `Delete monitor`, `data-confirm="Really delete?"`, "it cannot change", `<h1 class="vk-drawer__title">Nightly backup</h1>`, "nightly-backup · created 27 Sep")
	saved := e.post(projPath+"/m/nightly-backup/edit", url.Values{"name": {"Nightly"}, "schedule_type": {"cron"}, "schedule": {"0 4 * * *"}, "grace": {"1h"}}, false)
	if saved.code != 303 {
		t.Fatalf("edit: %d %s", saved.code, saved.body)
	}
	m, _ = e.svc.MonitorBySlug(context.Background(), e.scope, "nightly-backup")
	if m.Name != "Nightly" || m.Heartbeat.Schedule.Cron != "0 4 * * *" {
		t.Fatalf("edited: %+v", m)
	}
}

func TestPullMonitorDrawer(t *testing.T) {
	e := newEnv(t)
	reg, err := checks.NewRegistry(checks.Options{Outbound: outbound.Options{AllowPrivateTargets: true}})
	if err != nil {
		t.Fatal(err)
	}
	e.svc.SetChecker(reg)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	defer srv.Close()
	ctx := context.Background()
	if _, err := e.svc.CreateMonitor(ctx, e.scope, &domain.Monitor{Slug: "web", Name: "Web", Kind: domain.KindHTTP, Pull: &domain.PullSpec{HTTP: &domain.HTTPCheck{URL: srv.URL}}}); err != nil {
		t.Fatal(err)
	}
	p := e.get(projPath+"/m/web", false)
	p.has(t, "<h2>Target</h2>", "GET "+srv.URL, ">Check now<", "every 1 min · timeout 10 s · down after 3 failures · confirm 2×", "No attempts yet", "kind: http")
	if strings.Contains(p.body, "Ping URL") {
		t.Error("a pull monitor has no ping URL")
	}
	checked := e.post(projPath+"/m/web/check", nil, true)
	if checked.code != 200 {
		t.Fatalf("check now: %d %s", checked.code, checked.body)
	}
	checked.has(t, "vk-state--up", "200 OK", " ms<", "new → up · first ok")
	list := e.get(projPath, false)
	list.has(t, `class="vk-spark`, `class="vk-row"`, " ms<")
}

func TestPullMonitorForms(t *testing.T) {
	e := newEnv(t)
	form := e.get(projPath+"/m/new?kind=http", false)
	form.has(t, `name="kind" value="http" checked`, `for="url"`, `for="expect_status"`, "Codes or ranges, comma-separated.", `for="interval"`, `value="60s"`, `for="timeout"`, "10s or more.",
		"<h3>Request</h3>", "<h3>Failures</h3>", `id="failures-msg"`, "A failed check is retried 2×, 5s apart, before it counts. Down after 3 counted failures, up after 1 success.",
		"<h3>Response body</h3>", `name="body_match" value="none" checked`, "<h3>TLS and redirects</h3>", `name="verify_tls" value="1" checked`, `for="ca_pem"`, "GET · retry 2× after 5s · down after 3", `name="pull_form"`, "kind: http")
	if strings.Contains(form.body, "Ping URL") || strings.Contains(form.body, `for="grace"`) {
		t.Error("a pull form has no ping URL or grace")
	}
	// the body match segmented re-renders the form with its fields
	jp := e.get(projPath+"/m/new?kind=http&name=API&url=https://x&body_match=jsonpath", true)
	jp.has(t, `for="jsonpath"`, `for="equals"`, `value="API"`, `name="body_match" value="jsonpath" checked`)
	tcp := e.get(projPath+"/m/new?kind=tcp&name=DB", true)
	tcp.has(t, `for="host"`, `for="port"`, "<h3>Banner</h3>", `value="DB"`, `value="3"`)
	tls := e.get(projPath+"/m/new?kind=tls", true)
	tls.has(t, `for="servername"`, `value="443"`, "<h3>Expiry</h3>", `value="14"`)
	dns := e.get(projPath+"/m/new?kind=dns", true)
	dns.has(t, `for="dns_name"`, `<option value="A" selected>`, `for="resolver"`)
	icmp := e.get(projPath+"/m/new?kind=icmp", true)
	icmp.has(t, `for="count"`, `for="loss_threshold"`, `value="0.67"`)

	// validation from the domain lands on the form's fields
	bad := e.post(projPath+"/m/new", url.Values{"kind": {"http"}, "name": {"API"}, "url": {"ftp://x"}, "interval": {"30s"}, "timeout": {"45s"}, "expect_status": {"2xx"}, "pull_form": {"1"}}, true)
	if bad.code != 422 {
		t.Fatalf("validation: %d", bad.code)
	}
	bad.has(t, `id="url-msg"`, "Must be an absolute http(s) URL.", `id="timeout-msg"`, "Must be shorter than the interval.", "Use codes or ranges such as 200, 300-399.")
	// preview for a pull form answers the failures sentence, the timeout hint, the summary and the YAML
	pv := e.post(projPath+"/m/preview", url.Values{"kind": {"http"}, "name": {"API"}, "url": {"https://api.example.com/healthz"}, "interval": {"30s"}, "timeout": {"45s"}, "retries": {"1"}, "retry_delay": {"2s"}, "body_match": {"jsonpath"}, "jsonpath": {"$.status"}, "equals": {"ok"}, "pull_form": {"1"}}, true)
	pv.has(t, `hx-target="#failures-msg"`, "retried 1×, 2s apart", `hx-target="#timeout-msg"`, "Must be shorter than the interval.", "GET · retry 1× after 2s · down after 3 · $.status = ok", "expect_body: {jsonpath: {path: $.status, equals: ok}}")
	// create keeps the checkbox state and the body match
	ok := e.post(projPath+"/m/new", url.Values{"kind": {"http"}, "name": {"Public API"}, "url": {"https://api.example.com/healthz"}, "expect_status": {"200, 300-399"}, "tags": {"api, prod"}, "interval": {"30s"}, "timeout": {"5s"},
		"headers": {"Accept: application/json"}, "body_match": {"jsonpath"}, "jsonpath": {"$.status"}, "equals": {"ok"}, "follow_redirects": {"1"}, "pull_form": {"1"}}, true)
	if ok.code != 204 || ok.hdr.Get("HX-Redirect") != projPath+"/m/public-api" {
		t.Fatalf("create: %d %v %s", ok.code, ok.hdr, ok.body)
	}
	m, err := e.svc.MonitorBySlug(context.Background(), e.scope, "public-api")
	if err != nil || m.Pull == nil || m.Pull.HTTP == nil {
		t.Fatalf("created: %+v %v", m, err)
	}
	h := m.Pull.HTTP
	if h.URL != "https://api.example.com/healthz" || len(h.ExpectStatus) != 2 || h.Headers["Accept"] != "application/json" || h.ExpectBody == nil || h.ExpectBody.JSONPath == nil || h.ExpectBody.JSONPath.Equals != "ok" || !h.Redirects() || h.Verify() || m.Pull.Interval.String() != "30s" || len(m.Tags) != 2 {
		t.Fatalf("spec: %+v %+v", m.Pull, h)
	}
	edit := e.get(projPath+"/m/public-api/edit", false)
	edit.has(t, `value="$.status"`, `name="body_match" value="jsonpath" checked`, `>Accept: application/json</textarea>`, `value="200, 300-399"`, `<details class="vk-details" open>`, "TLS unverified", `>Save changes<`)
	if strings.Contains(edit.body, `name="verify_tls" value="1" checked`) {
		t.Error("verify_tls must show as off")
	}
	saved := e.post(projPath+"/m/public-api/edit", url.Values{"kind": {"http"}, "name": {"Public API"}, "url": {"https://api.example.com/v2"}, "interval": {"30s"}, "timeout": {"5s"}, "body_match": {"none"}, "verify_tls": {"1"}, "follow_redirects": {"1"}, "pull_form": {"1"}}, false)
	if saved.code != 303 {
		t.Fatalf("edit: %d %s", saved.code, saved.body)
	}
	m, _ = e.svc.MonitorBySlug(context.Background(), e.scope, "public-api")
	if m.Pull.HTTP.URL != "https://api.example.com/v2" || !m.Pull.HTTP.ExpectBody.IsZero() || !m.Pull.HTTP.Verify() {
		t.Fatalf("edited: %+v", m.Pull.HTTP)
	}
	// a tcp monitor from the form
	tcpOK := e.post(projPath+"/m/new", url.Values{"kind": {"tcp"}, "name": {"DB"}, "host": {"db.lan"}, "port": {"5432"}, "interval": {"60s"}, "timeout": {"10s"}, "expect": {"220"}, "pull_form": {"1"}}, true)
	if tcpOK.code != 204 {
		t.Fatalf("tcp create: %d %s", tcpOK.code, tcpOK.body)
	}
	m, _ = e.svc.MonitorBySlug(context.Background(), e.scope, "db")
	if m.Pull == nil || m.Pull.TCP == nil || m.Pull.TCP.Port != 5432 || m.Pull.TCP.Expect != "220" {
		t.Fatalf("tcp spec: %+v", m.Pull)
	}
}

func TestPullRowsShowSparklines(t *testing.T) {
	e := newEnv(t)
	reg, err := checks.NewRegistry(checks.Options{Outbound: outbound.Options{AllowPrivateTargets: true}})
	if err != nil {
		t.Fatal(err)
	}
	e.svc.SetChecker(reg)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"status":"ok"}`)) }))
	defer srv.Close()
	ctx := context.Background()
	m, err := e.svc.CreateMonitor(ctx, e.scope, &domain.Monitor{Slug: "web", Name: "Web", Kind: domain.KindHTTP, Pull: &domain.PullSpec{HTTP: &domain.HTTPCheck{URL: srv.URL, ExpectBody: &domain.ExpectBody{JSONPath: &domain.JSONPathExpect{Path: "$.status", Equals: "ok"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := e.svc.RunCheck(ctx, m.ID, e.now); err != nil {
			t.Fatal(err)
		}
		e.now = e.now.Add(time.Hour)
	}
	list := e.get(projPath, false)
	list.has(t, `class="vk-spark`, "<polyline points=", " ms<")
	if strings.Contains(list.body, "next in") {
		t.Error("pull rows carry a sparkline, not a next due")
	}
	drawer := e.get(projPath+"/m/web", true)
	drawer.has(t, "200 OK · $.status = ok", " ms<")
}

func TestIncidentsPage(t *testing.T) {
	e := newEnv(t)
	e.monitor("job", "prod")
	ctx := context.Background()
	tgt, _ := e.svc.ResolvePing(ctx, e.project.PingKey, "job", "", false)
	_, _, _ = e.svc.RecordPing(ctx, tgt, service.PingObservation{Signal: domain.SignalFail})
	p := e.get(projPath+"/incidents", false)
	if p.code != 200 {
		t.Fatalf("incidents: %d", p.code)
	}
	p.has(t, `1 open`, `>Ack<`, "Job", `vk-irow--open`, "open under a minute", `>prod<span class="vk-chip__n">1</span>`, `name="period" value="30d" checked`, `class="vk-top__count"`)
	incs, _ := e.svc.ListIncidents(ctx, e.scope, true, 0, time.Time{})
	acked := e.post(projPath+"/incidents/"+incs[0].ID+"/ack", nil, true)
	if acked.code != 200 || !strings.Contains(acked.body, "acked by j · 14:00") || strings.Contains(acked.body, ">Ack<") || !strings.Contains(acked.body, "vk-irow--acked") {
		t.Fatalf("ack: %d %s", acked.code, acked.body)
	}
	e.now = e.now.Add(time.Hour)
	_, _, _ = e.svc.RecordPing(ctx, tgt, service.PingObservation{Signal: domain.SignalOK})
	p = e.get(projPath+"/incidents?partial=main", false)
	p.has(t, "vk-irow--resolved", "lasted 1 h", "1 in the last 30 days", "0 open", "Nothing open")
	if strings.Contains(p.body, "<html") {
		t.Error("main partial must not be a page")
	}
	p = e.get(projPath+"/incidents?period=7d&tag=other", false)
	p.has(t, "No resolved incidents", `name="period" value="7d" checked`)
}

func TestSettingsTabs(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if p := e.get(projPath+"/settings", false); p.code != 303 || p.hdr.Get("Location") != projPath+"/settings/channels" {
		t.Fatalf("settings redirect: %d", p.code)
	}
	ch := e.get(projPath+"/settings/channels", false)
	ch.has(t, `aria-current="page">Channels<span class="vk-tab__n">0</span>`, `href="/o/homelab/p/prod/settings/keys">Keys</a>`, "No alert channels yet", `href="/o/homelab/p/prod/settings/channels?add=1"`)
	add := e.get(projPath+"/settings/channels?add=1", false)
	add.has(t, `id="channel-panel"`, `<option value="webhook" selected>`, `for="url"`, `form="channel-form"`, `>Send test<`, "Secrets are stored encrypted")
	ntfy := e.get(projPath+"/settings/channels?add=1&kind=ntfy&name=phone", true)
	ntfy.has(t, `for="topic"`, `value="phone"`, `<option value="ntfy" selected>`)
	if strings.Contains(ntfy.body, "<html") {
		t.Error("htmx tab must be the partial")
	}
	bad := e.post(projPath+"/settings/channels", url.Values{"name": {"hook"}, "kind": {"webhook"}, "url": {"ftp://x"}}, false)
	if bad.code != 422 || !strings.Contains(bad.body, "vk-field--error") || !strings.Contains(bad.body, `value="hook"`) {
		t.Fatalf("bad channel: %d %s", bad.code, bad.body)
	}
	var hits int
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++; w.WriteHeader(200) }))
	defer hook.Close()
	ok := e.post(projPath+"/settings/channels", url.Values{"name": {"hook"}, "kind": {"webhook"}, "url": {hook.URL + "/x"}}, false)
	if ok.code != 303 || ok.hdr.Get("Location") != projPath+"/settings/channels" {
		t.Fatalf("create channel: %d %s", ok.code, ok.body)
	}
	ch = e.get(projPath+"/settings/channels", false)
	ch.has(t, "hook", hook.URL+"/x", `>Test<`, "1 route", "never sent", `aria-checked="true"`, `?edit=`)
	channels, _ := e.svc.ListChannels(ctx, e.scope)
	id := channels[0].ID
	// the switch flips enabled and answers with the tab
	off := e.post(projPath+"/settings/channels/"+id+"/toggle", nil, true)
	if off.code != 200 || !strings.Contains(off.body, `aria-checked="false"`) || !strings.Contains(off.body, "vk-srow--muted") {
		t.Fatalf("toggle: %d %s", off.code, off.body)
	}
	if on := e.post(projPath+"/settings/channels/"+id+"/toggle", nil, false); on.code != 303 {
		t.Fatalf("toggle back: %d", on.code)
	}
	// a test from the row shows its result under the row
	sent := e.post(projPath+"/settings/channels/"+id+"/test", nil, true)
	if sent.code != 200 || !strings.Contains(sent.body, "Test sent.") || hits != 1 {
		t.Fatalf("test: %d hits=%d %s", sent.code, hits, sent.body)
	}
	// a test from the panel uses the unsaved form
	dead := e.post(projPath+"/settings/channels", url.Values{"name": {"dead"}, "kind": {"webhook"}, "url": {"http://127.0.0.1:1/x"}, "action": {"test"}}, true)
	if dead.code != 200 || !strings.Contains(dead.body, "Test failed.") || !strings.Contains(dead.body, "<code>") || !strings.Contains(dead.body, `value="dead"`) {
		t.Fatalf("panel test: %d %s", dead.code, dead.body)
	}
	if list, _ := e.svc.ListChannels(ctx, e.scope); len(list) != 1 {
		t.Fatal("a test must not save the channel")
	}
	// edit in place keeps secrets
	edit := e.get(projPath+"/settings/channels?edit="+id, false)
	edit.has(t, `id="channel-panel"`, `value="hook"`, `>Save<`, `Delete channel`, `<input type="hidden" name="kind" value="webhook"`)
	saved := e.post(projPath+"/settings/channels/"+id, url.Values{"name": {"hook2"}, "url": {hook.URL + "/y"}, "headers": {"X-Token: abc"}}, false)
	if saved.code != 303 {
		t.Fatalf("edit channel: %d %s", saved.code, saved.body)
	}
	edit = e.get(projPath+"/settings/channels?edit="+id, false)
	edit.has(t, `value="hook2"`, ">***</textarea>")
	if p := e.post(projPath+"/settings/channels/"+id, url.Values{"name": {"hook2"}, "url": {hook.URL + "/y"}, "headers": {"***"}}, false); p.code != 303 {
		t.Fatalf("edit with kept secret: %d %s", p.code, p.body)
	}
	if c, _ := e.svc.Channel(ctx, e.scope, id); !strings.Contains(string(c.Config), "abc") {
		t.Fatalf("secret not kept: %s", c.Config)
	}
	// routes
	rt := e.get(projPath+"/settings/routes", false)
	rt.has(t, "Every monitor", "→ hook2", `vk-state--down`, `vk-state--up`, "no repeat", `vk-srow__lead">1<`, `?edit=`)
	addRoute := e.get(projPath+"/settings/routes?add=1", false)
	addRoute.has(t, `id="route-panel"`, `name="channels" value="`+id+`"`, `name="on" value="late"`, `form="route-form"`)
	badRoute := e.post(projPath+"/settings/routes", url.Values{"repeat_every": {"1m"}}, false)
	if badRoute.code != 422 || !strings.Contains(badRoute.body, "Pick at least one channel.") || !strings.Contains(badRoute.body, "Must be at least 5m.") {
		t.Fatalf("bad route: %d %s", badRoute.code, badRoute.body)
	}
	okRoute := e.post(projPath+"/settings/routes", url.Values{"channels": {id}, "match_tags": {"prod"}, "on": {"down", "late"}, "repeat_every": {"4h"}}, false)
	if okRoute.code != 303 {
		t.Fatalf("create route: %d %s", okRoute.code, okRoute.body)
	}
	rt = e.get(projPath+"/settings/routes", false)
	rt.has(t, `<span class="vk-tag">prod</span>`, "repeat every 4 h", `vk-state--late`, `vk-srow__lead">2<`)
	routes, _ := e.svc.ListRoutes(ctx, e.scope)
	var rid string
	for _, r := range routes {
		if len(r.MatchTags) == 1 {
			rid = r.ID
		}
	}
	editRoute := e.get(projPath+"/settings/routes?edit="+rid, false)
	editRoute.has(t, `value="prod"`, `value="`+id+`" checked`, `value="late" checked`, "Delete route")
	if p := e.post(projPath+"/settings/routes/"+rid, url.Values{"channels": {id}, "match_tags": {"prod, db"}, "on": {"down"}}, false); p.code != 303 {
		t.Fatalf("edit route: %d %s", p.code, p.body)
	}
	if r, _ := e.svc.Route(ctx, e.scope, rid); len(r.MatchTags) != 2 || len(r.On) != 1 {
		t.Fatalf("edited route: %+v", r)
	}
	if p := e.post(projPath+"/settings/routes/"+rid+"/delete", nil, false); p.code != 303 {
		t.Fatalf("delete route: %d", p.code)
	}
	// keys
	keys := e.get(projPath+"/settings/keys", false)
	keys.has(t, e.project.PingKey, "Rotate key", "No API keys", `name="access" value="rw"`, `for="key_name"`)
	created := e.post(projPath+"/settings/keys", url.Values{"name": {"laptop"}, "access": {"ro"}}, false)
	if created.code != 200 {
		t.Fatalf("create key: %d %s", created.code, created.body)
	}
	created.has(t, "Key created.", `data-copy="vk_`, "laptop", "never used", ">Revoke<", `<span class="vk-tag">ro</span>`, "j · today")
	apiKeys, _ := e.svc.ListAPIKeys(ctx, e.scope)
	revoked := e.post(projPath+"/settings/keys/"+apiKeys[0].ID+"/revoke", nil, false)
	if revoked.code != 303 || !strings.Contains(revoked.hdr.Get("Location"), "flash=Key+revoked.") {
		t.Fatalf("revoke: %d %s", revoked.code, revoked.hdr.Get("Location"))
	}
	if p := e.get(revoked.hdr.Get("Location"), false); !strings.Contains(p.body, "Key revoked.") {
		t.Error("flash not shown")
	}
	if p := e.post(projPath+"/settings/ping-key/rotate", nil, false); p.code != 303 {
		t.Fatalf("rotate: %d", p.code)
	}
	if p := e.get(projPath+"/settings/nope", false); p.code != 404 {
		t.Fatalf("unknown tab: %d", p.code)
	}
}

func TestSettingsChannelKinds(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for _, kind := range []string{"gotify", "matrix", "slackhook", "alertmanager"} {
		p := e.get(projPath+"/settings/channels?add=1&kind="+kind, true)
		p.has(t, `<option value="`+kind+`" selected>`)
	}
	e.get(projPath+"/settings/channels?add=1&kind=matrix", true).has(t, `for="homeserver"`, `for="room_id"`, `for="access_token"`)
	e.get(projPath+"/settings/channels?add=1&kind=alertmanager", true).has(t, `for="labels"`, "resolves on up")
	if p := e.post(projPath+"/settings/channels", url.Values{"name": {"push"}, "kind": {"gotify"}, "url": {"https://gotify.example.com"}, "token": {"app-token"}, "priority": {"9"}}, false); p.code != 303 {
		t.Fatalf("gotify: %d %s", p.code, p.body)
	}
	if p := e.post(projPath+"/settings/channels", url.Values{"name": {"am"}, "kind": {"alertmanager"}, "url": {"http://am:9093"}, "labels": {"team: ops\nenv: prod"}}, false); p.code != 303 {
		t.Fatalf("alertmanager: %d %s", p.code, p.body)
	}
	if p := e.post(projPath+"/settings/channels", url.Values{"name": {"chat"}, "kind": {"slackhook"}, "url": {"https://hooks.slack.com/services/T/B/x"}}, false); p.code != 303 {
		t.Fatalf("slackhook: %d %s", p.code, p.body)
	}
	if p := e.post(projPath+"/settings/channels", url.Values{"name": {"room"}, "kind": {"matrix"}, "homeserver": {"https://matrix.example.com"}, "room_id": {"#alias:x"}, "access_token": {"t"}}, false); p.code != 422 || !strings.Contains(p.body, "vk-field--error") {
		t.Fatalf("matrix validation: %d", p.code)
	}
	channels, _ := e.svc.ListChannels(ctx, e.scope)
	byName := map[string]string{}
	for _, c := range channels {
		byName[c.Name] = string(c.Config)
	}
	if !strings.Contains(byName["push"], `"priority":9`) || !strings.Contains(byName["am"], `"labels":{"env":"prod","team":"ops"}`) {
		t.Fatalf("configs: %v", byName)
	}
	tab := e.get(projPath+"/settings/channels", false)
	tab.has(t, "gotify · https://gotify.example.com", "slackhook · incoming webhook", "alertmanager · http://am:9093")
	for _, c := range channels {
		if c.Name == "chat" {
			edit := e.get(projPath+"/settings/channels?edit="+c.ID, false)
			edit.has(t, `value="***"`)
		}
	}
}

func TestSettingsMaintenance(t *testing.T) {
	e := newEnv(t)
	tab := e.get(projPath+"/settings/maintenance", false)
	tab.has(t, `aria-current="page">Maintenance<span class="vk-tab__n">0</span>`, "No maintenance windows", "do not alert or go down")
	add := e.get(projPath+"/settings/maintenance?add=1", false)
	add.has(t, `id="window-panel"`, `name="repeat" value="weekly" checked`, `name="days" value="Sat"`, `for="from"`, `placeholder="02:00"`, "Europe/Amsterdam (project)", `id="window-msg"`, ">Save window<")
	once := e.get(projPath+"/settings/maintenance?add=1&repeat=once&name=NAS", true)
	once.has(t, `name="repeat" value="once" checked`, `value="NAS"`)
	if strings.Contains(once.body, `name="days"`) {
		t.Error("a one-off window has no day toggles")
	}
	// the clock is Sunday 14:00 in Amsterdam: this weekly window is active now
	weekly := e.post(projPath+"/settings/maintenance", url.Values{"name": {"weekly patching"}, "match_tags": {"prod"}, "repeat": {"weekly"}, "days": {"Sun"}, "from": {"13:00"}, "to": {"17:00"}, "timezone": {"Europe/Amsterdam"}}, false)
	if weekly.code != 303 {
		t.Fatalf("create weekly: %d %s", weekly.code, weekly.body)
	}
	tab = e.get(projPath+"/settings/maintenance", false)
	tab.has(t, "weekly patching", "weekly · Sun 13:00–17:00 Europe/Amsterdam", `<span class="vk-tag">prod</span>`, `class="vk-pill"`, "active · 3 h left", ">End now<", `?edit=`)
	bad := e.post(projPath+"/settings/maintenance", url.Values{"name": {"swap"}, "repeat": {"once"}, "from": {"2026-09-29 21:00"}, "to": {"2026-09-29 19:00"}, "timezone": {"Europe/Amsterdam"}}, false)
	if bad.code != 422 || !strings.Contains(bad.body, "Must be after the start.") {
		t.Fatalf("bad once: %d %s", bad.code, bad.body)
	}
	onceOK := e.post(projPath+"/settings/maintenance", url.Values{"name": {"NAS disk swap"}, "match_tags": {"homelab"}, "repeat": {"once"}, "from": {"2026-09-29 19:00"}, "to": {"2026-09-29 21:00"}, "timezone": {"Europe/Amsterdam"}}, false)
	if onceOK.code != 303 {
		t.Fatalf("create once: %d %s", onceOK.code, onceOK.body)
	}
	tab = e.get(projPath+"/settings/maintenance", false)
	tab.has(t, "once · Tue 29 Sep 19:00–21:00 Europe/Amsterdam", "starts Tue 29 Sep 19:00", `Maintenance<span class="vk-tab__n">2</span>`)
	windows, _ := e.svc.ListMaintenance(context.Background(), e.scope)
	edit := e.get(projPath+"/settings/maintenance?edit="+windows[0].ID, false)
	edit.has(t, `value="weekly patching"`, `name="days" value="Sun" checked`, `value="13:00"`, "Delete window", "Active until")
	ended := e.post(projPath+"/settings/maintenance/"+windows[0].ID+"/end", nil, true)
	if ended.code != 200 || strings.Contains(ended.body, `class="vk-pill"`) || !strings.Contains(ended.body, "next Sun 4 Oct 13:00") {
		t.Fatalf("end now: %d %s", ended.code, ended.body)
	}
	if p := e.post(projPath+"/settings/maintenance/"+windows[1].ID+"/delete", nil, false); p.code != 303 {
		t.Fatalf("delete: %d", p.code)
	}
}

func TestPublicStatusPage(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.monitor("api", "prod")
	e.monitor("nightly", "backup")
	e.monitor("lab-thing", "lab")
	tgt, _ := e.svc.ResolvePing(ctx, e.project.PingKey, "api", "", false)
	_, _, _ = e.svc.RecordPing(ctx, tgt, service.PingObservation{Signal: domain.SignalFail})
	if _, err := e.svc.CreateStatusPage(ctx, e.scope, &domain.StatusPage{Slug: "homelab", Title: "Homelab status", MatchTags: []string{"prod", "backup"}, Public: true}, ""); err != nil {
		t.Fatal(err)
	}
	p := e.do("GET", "/s/homelab", nil, false, false)
	if p.code != 200 {
		t.Fatalf("status page: %d %s", p.code, p.body)
	}
	p.has(t, "<h1>Homelab status</h1>", "vk-banner--down", "1 service down", "since 14:00", "<h2>prod</h2>", "<h2>backup</h2>", "Api", "Nightly", "vk-uptime", `aria-label="no data"`, "Open incidents", "powered by", `http-equiv="refresh"`, "updated 14:00:00 CEST")
	if strings.Contains(p.body, "no data up") {
		t.Error("a monitor without data is labelled 'no data', not 'no data up'")
	}
	if strings.Contains(p.body, "<script") || strings.Contains(p.body, "Lab-thing") || strings.Contains(p.body, "vk-top") {
		t.Error("a status page carries no script, no top bar and no monitors outside its tags")
	}
	if p.hdr.Get("Cache-Control") != "public, max-age=30" || p.hdr.Get("X-Frame-Options") != "SAMEORIGIN" || !strings.Contains(p.hdr.Get("Content-Security-Policy"), "default-src 'none'") {
		t.Errorf("headers: %v", p.hdr)
	}
	if p := e.do("GET", "/s/nope", nil, false, false); p.code != 404 {
		t.Fatalf("unknown page: %d", p.code)
	}
	svg := e.do("GET", "/s/homelab/badge/api.svg", nil, false, false)
	if svg.code != 200 || !strings.HasPrefix(svg.hdr.Get("Content-Type"), "image/svg+xml") || !strings.Contains(svg.body, ">down<") || !strings.Contains(svg.body, "#e05d44") {
		t.Fatalf("svg badge: %d %s", svg.code, svg.body)
	}
	js := e.do("GET", "/s/homelab/badge/api.json", nil, false, false)
	js.has(t, `"schemaVersion":1`, `"color":"red"`, `"label":"Api"`, `"message":"down"`)
	if p := e.do("GET", "/s/homelab/badge/lab-thing.svg", nil, false, false); p.code != 404 {
		t.Fatalf("badge outside the page: %d", p.code)
	}
	// a private page asks for its password and remembers the answer in a cookie
	if _, err := e.svc.CreateStatusPage(ctx, e.scope, &domain.StatusPage{Slug: "office", Title: "Office", Public: false}, "s3cret"); err != nil {
		t.Fatal(err)
	}
	locked := e.do("GET", "/s/office", nil, false, false)
	locked.has(t, `type="password"`, "asks for a password", "<h1>Office</h1>")
	if strings.Contains(locked.body, "vk-uptime") || locked.hdr.Get("Cache-Control") != "no-store" {
		t.Error("locked page must not show monitors or be cached")
	}
	if p := e.do("POST", "/s/office", url.Values{"password": {"nope"}}, false, false); p.code != 401 || !strings.Contains(p.body, "Wrong password.") {
		t.Fatalf("wrong password: %d", p.code)
	}
	ok := e.do("POST", "/s/office", url.Values{"password": {"s3cret"}}, false, false)
	if ok.code != 303 || ok.hdr.Get("Location") != "/s/office" || !strings.Contains(ok.hdr.Get("Set-Cookie"), "vk_status_office=") {
		t.Fatalf("unlock: %d %v", ok.code, ok.hdr)
	}
	req := httptest.NewRequest("GET", "/s/office", nil)
	req.Header.Set("Cookie", strings.Split(ok.hdr.Get("Set-Cookie"), ";")[0])
	rec := httptest.NewRecorder()
	e.srv.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "vk-uptime") || rec.Header().Get("Cache-Control") != "private, max-age=30" {
		t.Fatalf("unlocked page: %d %s", rec.Code, rec.Header().Get("Cache-Control"))
	}
	// a custom domain serves the page at its root
	if _, err := e.svc.UpdateStatusPage(ctx, e.scope, "homelab", &domain.StatusPage{Slug: "homelab", Title: "Homelab status", MatchTags: []string{"prod"}, Public: true, CustomDomain: "status.example.test"}, ""); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/", "/badge/api.svg"} {
		req := httptest.NewRequest("GET", "http://status.example.test:8443"+path, nil)
		rec := httptest.NewRecorder()
		e.srv.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("custom domain %s: %d %s", path, rec.Code, rec.Body.String())
		}
	}
}

func TestSettingsPages(t *testing.T) {
	e := newEnv(t)
	tab := e.get(projPath+"/settings/pages", false)
	tab.has(t, `Status pages<span class="vk-tab__n">0</span>`, "No status pages", "cached for 30 s")
	add := e.get(projPath+"/settings/pages?add=1", false)
	add.has(t, `id="page-panel"`, `vk-affix__text">localhost:8080/s/<`, `name="access" value="public" checked`, `placeholder="only with Password"`, `disabled`, `for="custom_domain"`, ">Save page<")
	pw := e.get(projPath+"/settings/pages?add=1&access=password&title=Office", true)
	pw.has(t, `name="access" value="password" checked`, `value="Office"`)
	if strings.Contains(pw.body, `disabled=""`) {
		t.Error("the password field opens with Password access")
	}
	created := e.post(projPath+"/settings/pages", url.Values{"title": {"Homelab status"}, "slug": {"homelab"}, "match_tags": {"prod, backup"}, "access": {"public"}}, false)
	if created.code != 303 {
		t.Fatalf("create: %d %s", created.code, created.body)
	}
	tab = e.get(projPath+"/settings/pages", false)
	tab.has(t, "Homelab status", "http://localhost:8080/s/homelab", ">public<", `<span class="vk-tag">prod</span>`, "no custom domain", `href="/s/homelab"`, ">Open<")
	noPw := e.post(projPath+"/settings/pages", url.Values{"title": {"Office"}, "slug": {"office"}, "access": {"password"}}, false)
	if noPw.code != 422 || !strings.Contains(noPw.body, "Set a password or make the page public.") {
		t.Fatalf("private without password: %d %s", noPw.code, noPw.body)
	}
	if p := e.post(projPath+"/settings/pages", url.Values{"title": {"Office"}, "slug": {"office"}, "access": {"password"}, "password": {"s3cret"}}, false); p.code != 303 {
		t.Fatalf("private page: %d %s", p.code, p.body)
	}
	dup := e.post(projPath+"/settings/pages", url.Values{"title": {"Again"}, "slug": {"office"}, "access": {"public"}}, false)
	if dup.code != 422 || !strings.Contains(dup.body, "This address is taken.") {
		t.Fatalf("duplicate: %d", dup.code)
	}
	edit := e.get(projPath+"/settings/pages?edit=office", false)
	edit.has(t, `value="Office"`, `name="access" value="password" checked`, `placeholder="unchanged"`, "Delete page")
	if p := e.post(projPath+"/settings/pages/office", url.Values{"title": {"Office"}, "slug": {"office"}, "access": {"password"}, "custom_domain": {"status.w4j.nl"}}, false); p.code != 303 {
		t.Fatalf("edit keeps the password: %d %s", p.code, p.body)
	}
	tab = e.get(projPath+"/settings/pages", false)
	tab.has(t, ">password<", "status.w4j.nl")
	page, _ := e.svc.StatusPage(context.Background(), e.scope, "office")
	if !page.HasPassword() || page.CustomDomain != "status.w4j.nl" {
		t.Fatalf("edited page: %+v", page)
	}
	if p := e.post(projPath+"/settings/pages/office/delete", nil, false); p.code != 303 {
		t.Fatalf("delete: %d", p.code)
	}
}

func TestForeignProjectIs404AndStatic(t *testing.T) {
	e := newEnv(t)
	if p := e.get("/o/acme/p/prod", false); p.code != 404 || !strings.Contains(p.body, "Not found") {
		t.Fatalf("foreign project: %d", p.code)
	}
	if p := e.get("/o/homelab/p/nope", false); p.code != 404 {
		t.Fatalf("unknown project: %d", p.code)
	}
	p := e.get("/static/"+e.web.static.Hash()+"/vink.js", false)
	if p.code != 200 || !strings.Contains(p.body, "htmx:config:request") {
		t.Fatalf("static: %d", p.code)
	}
}

func TestAgentsTabDrawerAndRunFrom(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	root := "/o/homelab/admin/agents"

	// empty tab, then the add panel
	tab := e.get(root, false)
	if tab.code != 200 {
		t.Fatalf("agents tab: %d", tab.code)
	}
	tab.has(t, `aria-current="page">Agents<span class="vk-tab__n">0</span>`, `href="/o/homelab/admin/agents?add=1">Add agent</a>`, `<h3>No agents yet</h3>`, `vk-page vk-page--full`)
	if strings.Contains(tab.body, "vk-quota") {
		t.Error("no quota line without a quota")
	}
	panel := e.get(root+"?add=1", false)
	panel.has(t, `<section class="vk-panel" id="agent-panel"><div class="vk-panel__head"><h2>Add agent</h2></div>`, `id="name" name="name"`, `id="labels" name="labels"`, `>Create agent</button>`)
	if strings.Contains(panel.body, "?add=1\">Add agent") {
		t.Error("the Add agent button hides while the panel is open")
	}

	// validation, then a create that shows the token once
	bad := e.post(root, url.Values{"name": {"DC 2"}, "labels": {"site=dc2"}}, false)
	if bad.code != 422 || !strings.Contains(bad.body, `id="name-msg"`) {
		t.Fatalf("bad name: %d", bad.code)
	}
	badLabels := e.post(root, url.Values{"name": {"dc2-probe"}, "labels": {"nope"}}, false)
	if badLabels.code != 422 || !strings.Contains(badLabels.body, "key=value pairs") {
		t.Fatalf("bad labels: %d", badLabels.code)
	}
	created := e.post(root, url.Values{"name": {"dc2-probe"}, "labels": {"site=dc2, zone=dmz"}}, false)
	if created.code != 200 {
		t.Fatalf("create: %d %s", created.code, created.body)
	}
	created.has(t, `<b class="vk-notice__title">Agent dc2-probe created.</b> Run this on its host. The token is shown once; vink keeps only a hash.</p><div class="vk-codebox"><pre class="vk-code">vink agent --server ws://localhost:8080 \`, `--token vat_`, `--labels site=dc2,zone=dmz</pre><button type="button" class="vk-btn vk-copy" data-copy="vink agent`,
		`<a class="vk-srow__link" href="/o/homelab/admin/agents/dc2-probe">dc2-probe</a></span><span class="vk-srow__sub" title="site=dc2 · zone=dmz">site=dc2 · zone=dmz</span>`,
		`<span class="vk-state vk-state--new"><i class="vk-glyph vk-glyph--new" aria-hidden="true"></i>waiting</span>`, `vk-srow__cell--mono">—</span>`, `>0 monitors</span>`, `vk-srow__cell--mono">never</span>`,
		`action="/o/homelab/admin/agents/dc2-probe/revoke"`, `data-confirm="Really revoke?">Revoke</button>`, `vk-srow vk-srow--muted`, `Agents<span class="vk-tab__n">1</span>`)
	_, rest, _ := strings.Cut(created.body, "--token ")
	token, _, _ := strings.Cut(rest, " ")
	if !strings.Contains(created.body, "vk-notice vk-notice--ok") || strings.Contains(e.get(root, false).body, token) {
		t.Error("the token shows once")
	}
	dup := e.post(root, url.Values{"name": {"dc2-probe"}}, false)
	if dup.code != 422 || !strings.Contains(dup.body, "An agent with this name exists.") {
		t.Fatalf("duplicate: %d", dup.code)
	}

	// the quota line and the quota error
	one := int64(1)
	if err := e.svc.DB().Write().SetOrgQuotas(ctx, db.SetOrgQuotasParams{QuotaAgents: &one, ID: e.org.ID}); err != nil {
		t.Fatal(err)
	}
	e.get(root, false).has(t, `<p class="vk-quota"><span>Quota set by the instance admin</span><span class="vk-usage"><meter class="vk-usage__meter" min="0" max="1" value="1"`, `1 / 1 agents`)
	quota := e.post(root, url.Values{"name": {"second"}}, false)
	if quota.code != 422 || !strings.Contains(quota.body, "ask the instance admin for more") {
		t.Fatalf("quota: %d %s", quota.code, quota.body)
	}

	// the drawer: waiting, connection facts, no monitors yet
	drawer := e.get(root+"/dc2-probe", false)
	if drawer.code != 200 {
		t.Fatalf("drawer: %d", drawer.code)
	}
	drawer.has(t, `<div class="vk-page" id="page">`, `<aside class="vk-drawer" id="drawer">`, `<h1 class="vk-drawer__title">dc2-probe</h1>`, `vk-state--pill"><i class="vk-glyph vk-glyph--new" aria-hidden="true"></i>waiting</span><span class="vk-tag">site=dc2</span><span class="vk-tag">zone=dmz</span>`,
		`href="/o/homelab/admin/agents/dc2-probe?labels=1">Edit labels</a>`, `data-confirm="Really revoke?">Revoke token</button>`, `<dl class="vk-kv"><dt>last seen</dt><dd>never</dd><dt>token</dt><dd>vat_`, `Assigned monitors <span class="vk-muted vk-mono">0</span>`, `No monitor runs from this agent yet.`,
		`<div class="vk-srow vk-srow--muted" aria-current="true">`, `names dc2-probe, or labels only this agent has.`)
	if r := e.get(root+"/nope", false); r.code != 404 {
		t.Fatalf("unknown agent: %d", r.code)
	}

	// labels: the form in the drawer, a bad value, a save
	e.get(root+"/dc2-probe?labels=1", false).has(t, `<section class="vk-panel" id="labels-panel"><div class="vk-panel__head"><h2>Labels</h2></div>`, `name="labels"`, `value="site=dc2,zone=dmz"`, `>Save labels</button>`)
	if r := e.post(root+"/dc2-probe/labels", url.Values{"labels": {"bad key=x"}}, false); r.code != 422 || !strings.Contains(r.body, "label name") {
		t.Fatalf("bad labels: %d %s", r.code, r.body)
	}
	if r := e.post(root+"/dc2-probe/labels", url.Values{"labels": {"site=dc3"}}, false); r.code != 303 || r.hdr.Get("Location") != root+"/dc2-probe" {
		t.Fatalf("save labels: %d %s", r.code, r.hdr.Get("Location"))
	}
	e.get(root+"/dc2-probe", false).has(t, `<span class="vk-tag">site=dc3</span>`)

	// Run from in the monitor form: the group, the agent select with its
	// state, the interval floor, a monitor created on the agent
	form := e.get(projPath+"/m/new?kind=http", false)
	form.has(t, `<h3>Run from</h3>`, `aria-label="Run from"`, `name="location" value="local" checked`, `name="location" value="agent"`, `name="location" value="labels"`)
	if strings.Contains(form.body, `id="agent"`) {
		t.Error("no agent select on a local check")
	}
	e.get(projPath+"/m/new?kind=http&location=agent", true).has(t, `<select class="vk-input vk-input--mono" id="agent" name="agent"`, `<option value="dc2-probe">dc2-probe · waiting</option>`, `If the agent goes offline the monitor turns late, not down.`)
	e.get(projPath+"/m/new?kind=http&location=labels", true).has(t, `id="labels" name="labels"`, `The least loaded agent with all of these labels runs it.`)
	short := e.post(projPath+"/m/new", url.Values{"kind": {"http"}, "name": {"Intranet"}, "url": {"https://intranet.internal"}, "interval": {"10s"}, "location": {"agent"}, "agent": {"dc2-probe"}}, true)
	if short.code != 422 || !strings.Contains(short.body, "at least 30s when an agent runs the check") {
		t.Fatalf("interval floor: %d %s", short.code, short.body)
	}
	nobody := e.post(projPath+"/m/new", url.Values{"kind": {"http"}, "name": {"Intranet"}, "url": {"https://intranet.internal"}, "location": {"agent"}}, true)
	if nobody.code != 422 || !strings.Contains(nobody.body, "Pick an agent.") {
		t.Fatalf("no agent picked: %d", nobody.code)
	}
	ok := e.post(projPath+"/m/new", url.Values{"kind": {"http"}, "name": {"Intranet"}, "url": {"https://intranet.internal"}, "interval": {"60s"}, "location": {"agent"}, "agent": {"dc2-probe"}}, true)
	if ok.code != 200 && ok.code != 204 {
		t.Fatalf("create on agent: %d %s", ok.code, ok.body)
	}
	m, err := e.svc.MonitorBySlug(ctx, e.scope, "intranet")
	if err != nil || m.Pull.Location != "agent:dc2-probe" {
		t.Fatalf("created: %+v %v", m, err)
	}
	edit := e.get(projPath+"/m/intranet/edit", false)
	edit.has(t, `name="location" value="agent" checked`, `<option value="dc2-probe" selected>dc2-probe · waiting</option>`, `30s or more when an agent runs it.`, `from agent:dc2-probe · GET`)
	labelsMon := e.post(projPath+"/m/new", url.Values{"kind": {"tcp"}, "name": {"LDAP"}, "host": {"ldap.internal"}, "port": {"389"}, "interval": {"60s"}, "location": {"labels"}, "labels": {"site=dc3"}}, true)
	if labelsMon.code != 200 && labelsMon.code != 204 {
		t.Fatalf("create by labels: %d %s", labelsMon.code, labelsMon.body)
	}
	if m, _ := e.svc.MonitorBySlug(ctx, e.scope, "ldap"); m == nil || m.Pull.Location != "site=dc3" {
		t.Fatalf("by labels: %+v", m)
	}

	// the drawer with monitors waiting for the agent, then late after the sweep
	e.get(root+"/dc2-probe", false).has(t, `>0 monitors</span>`, `Assigned monitors <span class="vk-muted vk-mono">0</span>`)
	if _, err := e.svc.AssignAgents(ctx, e.org.ID); err != nil {
		t.Fatal(err)
	}
	agent, _ := e.svc.Agent(ctx, domain.Scope{OrgID: e.org.ID, Role: domain.RoleAdmin, UserID: e.scope.UserID}, "dc2-probe")
	online := true
	e.svc.SetAgentPresence(func(id string) bool { return online && id == agent.ID })
	e.svc.SetAgentSince(func(string) (time.Time, bool) { return e.now.Add(-3 * time.Hour), true })
	if _, err := e.svc.AssignAgents(ctx, e.org.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.TouchAgent(ctx, agent.ID, "10.20.0.14", "0.4.1"); err != nil {
		t.Fatal(err)
	}
	connected := e.get(root+"/dc2-probe", false)
	connected.has(t, `vk-state--up vk-state--pill"><i class="vk-glyph vk-glyph--up" aria-hidden="true"></i>connected</span>`, `<dt>from</dt><dd>10.20.0.14</dd><dt>version</dt><dd>0.4.1</dd>`, `<dt>connected</dt><dd>3 h, since 11:00</dd>`,
		`Assigned monitors <span class="vk-muted vk-mono">2</span>`, `<span class="vk-row__slug">intranet</span>`, `<span class="vk-row__slug">ldap</span>`, `no checks yet`, `>2 monitors</span>`, `seen just now`)
	online = false
	e.now = e.now.Add(5 * time.Minute)
	if n, err := e.svc.SweepOfflineAgents(ctx, e.now, 2*time.Minute); err != nil || n != 2 {
		t.Fatalf("sweep: %d %v", n, err)
	}
	offline := e.get(root+"/dc2-probe", false)
	offline.has(t, `vk-state--late vk-state--pill"><i class="vk-glyph vk-glyph--late" aria-hidden="true"></i>offline <span class="vk-state__since">for 5 min</span></span>`,
		`<b class="vk-notice__title">2 monitors are late.</b> Their checks stopped with the agent at 14:00. They stay late with reason agent offline; an agent going away never makes a monitor down.`,
		`vk-row__data vk-row__data--late" title="">agent offline 5 min</span>`, `vk-state--late"><i class="vk-glyph vk-glyph--late" aria-hidden="true"></i>offline <span class="vk-state__since">5 min</span>`, `seen 5 min ago`)

	// revoke: a flash, the row gone, the monitors released
	revoked := e.post(root+"/dc2-probe/revoke", nil, false)
	if revoked.code != 303 || !strings.Contains(revoked.hdr.Get("Location"), root+"?flash=") {
		t.Fatalf("revoke: %d %s", revoked.code, revoked.hdr.Get("Location"))
	}
	after := e.get(revoked.hdr.Get("Location"), false)
	after.has(t, `Agent dc2-probe revoked.`, `<h3>No agents yet</h3>`)
	if r := e.get(root+"/dc2-probe", false); r.code != 404 {
		t.Fatalf("after revoke: %d", r.code)
	}
	if m, _ := e.svc.MonitorBySlug(ctx, e.scope, "intranet"); m.AgentID != "" {
		t.Fatalf("released: %+v", m)
	}
}

func TestProjectsTab(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	root := "/o/homelab/admin/projects"
	e.monitor("job", "prod")

	tab := e.get(root, false)
	if tab.code != 200 {
		t.Fatalf("projects tab: %d", tab.code)
	}
	tab.has(t, `href="/o/homelab/admin/projects?add=1">Add project</a>`,
		`<a class="vk-srow__link" href="/o/homelab/p/prod">prod</a></span><span class="vk-srow__sub" title="/o/homelab/p/prod · Europe/Amsterdam">/o/homelab/p/prod · Europe/Amsterdam</span>`,
		`vk-srow__cell--l"><span class="vk-counts"><span class="vk-counts__n vk-counts__n--new" title="1 new">`, `>1 monitor</span>`, `vk-srow__cell--mono">since 27 Sep</span>`,
		`<a class="vk-btn" href="/o/homelab/p/prod">Open</a><a class="vk-btn" href="/o/homelab/admin/projects?edit=prod">Edit</a>`)
	if strings.Contains(tab.body, "vk-quota") || strings.Contains(tab.body, "Not yet") {
		t.Error("no quota line without quotas, and no placeholder")
	}
	hundred, five := int64(100), int64(5)
	if err := e.svc.DB().Write().SetOrgQuotas(ctx, db.SetOrgQuotasParams{QuotaMonitors: &hundred, QuotaAgents: &five, ID: e.org.ID}); err != nil {
		t.Fatal(err)
	}
	e.get(root, false).has(t, `<p class="vk-quota"><span>Quota set by the instance admin</span><span class="vk-usage"><meter class="vk-usage__meter" min="0" max="100" value="1"`, `1 / 100 monitors`, `0 / 5 agents`)

	// the add panel, validation, a create
	panel := e.get(root+"?add=1", false)
	panel.has(t, `<section class="vk-panel" id="project-panel"><div class="vk-panel__head"><h2>Add project</h2></div>`, `id="pr_name" name="pr_name"`, `id="pr_slug" name="pr_slug"`, `In its URLs: /o/homelab/p/&lt;slug&gt;`,
		`<select class="vk-input" id="pr_tz" name="pr_tz"`, `<option value="Europe/Amsterdam" selected>Europe/Amsterdam</option>`, `Default for cron schedules and maintenance windows.`, `vink creates the project with its own ping key and a default route.`, `>Create project</button>`, `href="/o/homelab/admin/projects">Cancel</a>`)
	if strings.Contains(panel.body, "?add=1\">Add project") {
		t.Error("the Add project button hides while the panel is open")
	}
	bad := e.post(root, url.Values{"pr_name": {"Lab"}, "pr_slug": {"Lab!"}, "pr_tz": {"Mars/Olympus"}}, false)
	if bad.code != 422 || !strings.Contains(bad.body, `id="pr_slug-msg"`) || !strings.Contains(bad.body, "Unknown timezone") {
		t.Fatalf("bad project: %d", bad.code)
	}
	dup := e.post(root, url.Values{"pr_name": {"Production"}, "pr_slug": {"prod"}, "pr_tz": {"UTC"}}, false)
	if dup.code != 422 || !strings.Contains(dup.body, "A project with this slug exists in this org.") {
		t.Fatalf("duplicate: %d", dup.code)
	}
	ok := e.post(root, url.Values{"pr_name": {"Lab Network"}, "pr_tz": {"UTC"}}, false)
	if ok.code != 303 || !strings.Contains(ok.hdr.Get("Location"), root+"?flash=") {
		t.Fatalf("create: %d %s", ok.code, ok.hdr.Get("Location"))
	}
	after := e.get(ok.hdr.Get("Location"), false)
	after.has(t, `Project lab-network created with its own ping key and a default route.`, `href="/o/homelab/p/lab-network">lab-network</a>`, `/o/homelab/p/lab-network · UTC`, `<span class="vk-counts__none">no monitors</span>`, `>0 monitors</span>`, `Projects<span class="vk-tab__n">2</span>`)
	if p, err := e.svc.ProjectBySlug(ctx, e.org.ID, "lab-network"); err != nil || p.Name != "Lab Network" || p.PingKey == "" {
		t.Fatalf("created project: %+v %v", p, err)
	}

	// edit: the panel with the slug locked, a save
	edit := e.get(root+"?edit=lab-network", false)
	edit.has(t, `<h2>Edit lab-network</h2>`, `value="Lab Network"`, `id="pr_slug" name="pr_slug"`, `disabled`, `Part of its URLs; it cannot change.`, `<option value="UTC" selected>UTC</option>`, `>Save changes</button>`)
	if r := e.get(root+"?edit=nope", false); r.code != 404 {
		t.Fatalf("edit unknown: %d", r.code)
	}
	if r := e.post(root+"/lab-network", url.Values{"pr_name": {""}, "pr_tz": {"UTC"}}, false); r.code != 422 || !strings.Contains(r.body, "Must not be empty.") {
		t.Fatalf("empty name: %d", r.code)
	}
	saved := e.post(root+"/lab-network", url.Values{"pr_name": {"Lab"}, "pr_tz": {"Europe/London"}}, false)
	if saved.code != 303 {
		t.Fatalf("save: %d %s", saved.code, saved.body)
	}
	if p, _ := e.svc.ProjectBySlug(ctx, e.org.ID, "lab-network"); p.Name != "Lab" || p.Timezone != "Europe/London" {
		t.Fatalf("saved project: %+v", p)
	}
	e.get(saved.hdr.Get("Location"), false).has(t, `Project lab-network saved.`, `/o/homelab/p/lab-network · Europe/London`)
	if r := e.post("/o/acme/admin/projects", url.Values{"pr_name": {"X"}}, false); r.code != 404 {
		t.Fatalf("foreign org create: %d", r.code)
	}
}

func TestSwitcherListsOrgsWithoutProjects(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	admin := domain.Scope{InstanceAdmin: true, Role: domain.RoleOwner}
	empty, err := e.svc.CreateOrg(ctx, admin, "empty", "Empty")
	if err != nil {
		t.Fatal(err)
	}
	j, _ := e.svc.UserBySubject(ctx, "j")
	if err := e.svc.SetMembership(ctx, admin, j.ID, empty.ID, domain.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	// the switcher shows the empty org with its admin items, before homelab's
	p := e.get(projPath, false)
	p.has(t, `<div class="vk-menu__label"><span>empty</span><span class="vk-tag">admin</span></div></div>`, `href="/o/empty/admin/projects?add=1"><span>New project</span>`, `href="/o/empty/admin/projects"><span>Org settings</span>`)
	if strings.Index(p.body, "<span>empty</span>") > strings.Index(p.body, "<span>homelab</span>") {
		t.Error("orgs are listed by slug")
	}
	// the chooser offers to add the first project
	chooser := e.get("/projects", false)
	chooser.has(t, `<div class="vk-srow vk-srow--muted"><div class="vk-srow__main"><span class="vk-srow__title"><a class="vk-srow__link" href="/o/empty/admin/projects?add=1">Add the first project</a></span><span class="vk-srow__sub" title="empty">empty</span></div><span class="vk-srow__cell vk-srow__cell--m">no projects yet</span>`,
		`<span class="vk-srow__title"><a class="vk-srow__link" href="/o/homelab/p/prod">Production</a></span><span class="vk-srow__sub" title="homelab / prod">homelab / prod</span></div><span class="vk-srow__cell vk-srow__cell--s">admin</span>`)

	// a viewer whose only org has no projects lands on the chooser, not the no-access page
	v, _ := e.svc.CreateLocalUser(ctx, admin, "v", "v@example.com", "V", "correct horse", false)
	_ = e.svc.SetMembership(ctx, admin, v.ID, empty.ID, domain.RoleViewer)
	rec := httptest.NewRecorder()
	if _, err := e.authn.Login(rec, httptest.NewRequest("POST", "/login", nil), "v", "correct horse"); err != nil {
		t.Fatal(err)
	}
	e.cookie = rec.Result().Cookies()[0]
	if r := e.get("/", false); r.code != 303 || r.hdr.Get("Location") != "/projects" {
		t.Fatalf("viewer home: %d %s", r.code, r.hdr.Get("Location"))
	}
	r := e.get("/projects", false)
	r.has(t, `<span class="vk-srow__title">empty</span><span class="vk-srow__sub vk-srow__sub--prose" title="No projects yet. Ask an org admin to add one.">No projects yet. Ask an org admin to add one.</span>`)
	if strings.Contains(r.body, "New project") || strings.Contains(r.body, "Add the first project") {
		t.Error("a viewer gets no add links")
	}

	// an instance admin sees every org, as owner
	root, _ := e.svc.CreateLocalUser(ctx, admin, "root", "r@example.com", "Root", "correct horse", true)
	_ = root
	rec = httptest.NewRecorder()
	if _, err := e.authn.Login(rec, httptest.NewRequest("POST", "/login", nil), "root", "correct horse"); err != nil {
		t.Fatal(err)
	}
	e.cookie = rec.Result().Cookies()[0]
	e.get("/projects", false).has(t, `href="/o/empty/admin/projects?add=1"`)
	e.get(projPath, false).has(t, `<span>acme</span><span class="vk-tag">owner</span>`, `<span>empty</span><span class="vk-tag">owner</span>`, `<span>homelab</span><span class="vk-tag">owner</span>`, `href="/o/empty/admin/projects?add=1"><span>New project</span>`)
}

func TestMembersInvitesAndOwnerActions(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	root := "/o/homelab/admin/members"
	admin := domain.Scope{InstanceAdmin: true, Role: domain.RoleOwner}
	j, _ := e.svc.UserBySubject(ctx, "j")
	_ = e.svc.SetMembership(ctx, admin, j.ID, e.org.ID, domain.RoleOwner)
	bob, _ := e.svc.CreateLocalUser(ctx, admin, "bob", "bob@example.com", "Bob Jansen", "correct horse", false)
	_ = e.svc.SetMembership(ctx, admin, bob.ID, e.org.ID, domain.RoleMember)

	// the tab: you as owner, locked; bob with a live select and Remove
	tab := e.get(root, false)
	if tab.code != 200 {
		t.Fatalf("members: %d", tab.code)
	}
	tab.has(t, `aria-current="page">Members<span class="vk-tab__n">2</span>`, `href="/o/homelab/admin/members?invite=1">Invite</a>`,
		`<span class="vk-srow__lead"><span class="vk-avatar" aria-hidden="true">J</span></span><div class="vk-srow__main"><span class="vk-srow__title">Jaro <span class="vk-tag">you</span></span><span class="vk-srow__sub" title="j@example.com · local account">`,
		`aria-label="Role for Jaro" disabled`, `vk-srow__cell--mono">active now</span><span class="vk-srow__actions"></span>`,
		`<span class="vk-srow__title">Bob Jansen</span>`, `aria-label="Role for Bob Jansen" hx-post="/o/homelab/admin/members/`+bob.ID+`/role" hx-trigger="change"`, `<option value="member" selected>member</option>`, `vk-srow__cell--mono">never</span>`,
		`action="/o/homelab/admin/members/`+bob.ID+`/remove"`, `data-confirm="Really remove?">Remove</button>`,
		`<h2 class="vk-listhead">Owner actions</h2>`, `name="new_owner"`, `<option value="`+bob.ID+`">Bob Jansen</option>`, `Possible once homelab has no projects. It has 1 project.`, `>Delete org</button>`)
	if strings.Contains(tab.body, "vk-listhead\">Invites") {
		t.Error("no invites heading without invites")
	}

	// the invite panel, a bad note, a link shown once
	e.get(root+"?invite=1", false).has(t, `<h2>Invite a local account</h2>`, `id="inv_for" name="inv_for"`, `<select class="vk-input" id="inv_role" name="inv_role"`, `<option value="owner">owner</option>`, `<option value="member" selected>member</option>`, `>Create link</button>`)
	if r := e.post(root+"/invites", url.Values{"inv_for": {"  "}, "inv_role": {"member"}}, false); r.code != 422 || !strings.Contains(r.body, `id="inv_for-msg"`) {
		t.Fatalf("empty note: %d", r.code)
	}
	created := e.post(root+"/invites", url.Values{"inv_for": {"Lisa"}, "inv_role": {"member"}}, false)
	if created.code != 200 {
		t.Fatalf("create invite: %d %s", created.code, created.body)
	}
	created.has(t, `<h2 class="vk-listhead">Invites <span>1 open</span></h2>`, `<span class="vk-avatar" aria-hidden="true">L</span>`, `<span class="vk-srow__title">for Lisa</span>`, `created by Jaro · today`, `<span class="vk-tag">member</span>`, `vk-srow__cell--l vk-srow__cell--mono">expires `,
		`<b class="vk-notice__title">Invite link created.</b> Send it to Lisa. It works once; vink keeps only a hash, so this is the only time you see it.`, `<code class="vk-ping__url">http://localhost:8080/invite/iv_`)
	_, rest, _ := strings.Cut(created.body, `<code class="vk-ping__url">http://localhost:8080/invite/`)
	token, _, _ := strings.Cut(rest, "<")
	if !strings.HasPrefix(token, "iv_") {
		t.Fatalf("token: %q", token)
	}
	if again := e.get(root, false); strings.Contains(again.body, token) {
		t.Error("the link shows once")
	}

	// the invite page: the form, a weak password, the account, then the expired card
	page := e.do("GET", "/invite/"+token, nil, false, false)
	if page.code != 200 {
		t.Fatalf("invite page: %d", page.code)
	}
	page.has(t, `<h1>Join homelab</h1>`, `<b>Jaro</b> invited you to homelab as a <b>member</b>. The link works once and expires `, `id="username" name="username"`, `autocomplete="new-password"`, `>Create account</button>`, `href="/login">Sign in</a>, then open the link again.`)
	weak := e.do("POST", "/invite/"+token, url.Values{"username": {"lisa"}, "display_name": {"Lisa de Boer"}, "password": {"short"}}, false, false)
	if weak.code != 422 || !strings.Contains(weak.body, `id="password-msg"`) {
		t.Fatalf("weak password: %d", weak.code)
	}
	taken := e.do("POST", "/invite/"+token, url.Values{"username": {"bob"}, "password": {"a-long-passphrase"}}, false, false)
	if taken.code != 422 || !strings.Contains(taken.body, "That username is taken.") {
		t.Fatalf("taken: %d", taken.code)
	}
	joined := e.do("POST", "/invite/"+token, url.Values{"username": {"lisa"}, "display_name": {"Lisa de Boer"}, "password": {"a-long-passphrase"}}, false, false)
	if joined.code != 303 || joined.hdr.Get("Location") != "/" || len(joined.hdr.Values("Set-Cookie")) == 0 {
		t.Fatalf("join: %d %s", joined.code, joined.hdr.Get("Location"))
	}
	lisa, err := e.svc.UserBySubject(ctx, "lisa")
	if err != nil || lisa.DisplayName != "Lisa de Boer" || lisa.Source != "local" {
		t.Fatalf("lisa: %+v %v", lisa, err)
	}
	if ms, _ := e.svc.MembershipsForUser(ctx, lisa.ID); len(ms) != 1 || ms[0].Role != domain.RoleMember || ms[0].OrgSlug != "homelab" {
		t.Fatalf("lisa's membership: %+v", ms)
	}
	gone := e.do("GET", "/invite/"+token, nil, false, false)
	if gone.code != 410 {
		t.Fatalf("used link: %d", gone.code)
	}
	gone.has(t, `<h1>This invite has expired</h1>`, `<dt>org</dt><dd>homelab</dd><dt>invited by</dt><dd>Jaro</dd><dt>role</dt><dd>member</dd>`, `A link that was already used shows this page too.`, `href="/login">Go to sign in</a>`)
	if r := e.do("POST", "/invite/"+token, url.Values{"username": {"x"}, "password": {"a-long-passphrase"}}, false, false); r.code != 410 {
		t.Fatalf("used link post: %d", r.code)
	}
	if r := e.do("GET", "/invite/iv_nope", nil, false, false); r.code != 404 {
		t.Fatalf("unknown link: %d", r.code)
	}
	e.get(root, false).has(t, `<h2 class="vk-listhead">Invites <span>1 used</span></h2>`, `vk-srow vk-srow--muted"><span class="vk-srow__lead"><span class="vk-avatar" aria-hidden="true">L</span>`, `vk-srow__cell--mono">joined as lisa</span><span class="vk-srow__actions"></span>`, `<span class="vk-srow__title">Lisa de Boer</span>`, `Members<span class="vk-tab__n">3</span>`)

	// revoked and expired links; Remove tidies them
	second := e.post(root+"/invites", url.Values{"inv_for": {"Marloes"}, "inv_role": {"viewer"}}, false)
	_, rest, _ = strings.Cut(second.body, `<code class="vk-ping__url">http://localhost:8080/invite/`)
	token2, _, _ := strings.Cut(rest, "<")
	invites, _ := e.svc.ListInvites(ctx, e.scope)
	var marloes string
	for _, inv := range invites {
		if inv.Note == "Marloes" {
			marloes = inv.ID
		}
	}
	if r := e.post(root+"/invites/"+marloes+"/revoke", nil, false); r.code != 303 {
		t.Fatalf("revoke: %d", r.code)
	}
	if r := e.do("GET", "/invite/"+token2, nil, false, false); r.code != 410 {
		t.Fatalf("revoked link: %d", r.code)
	}
	e.get(root, false).has(t, `Invites <span>1 expired · 1 used</span>`, `vk-srow__cell--mono">revoked `, `action="/o/homelab/admin/members/invites/`+marloes+`/remove"`)
	third := e.post(root+"/invites", url.Values{"inv_for": {"Old"}, "inv_role": {"viewer"}}, false)
	_, rest, _ = strings.Cut(third.body, `<code class="vk-ping__url">http://localhost:8080/invite/`)
	token3, _, _ := strings.Cut(rest, "<")
	e.now = e.now.Add(8 * 24 * time.Hour)
	if r := e.do("GET", "/invite/"+token3, nil, false, false); r.code != 410 || !strings.Contains(r.body, "ran out on") {
		t.Fatalf("expired link: %d", r.code)
	}
	e.get(root, false).has(t, `Invites <span>2 expired · 1 used</span>`, `vk-srow__cell--mono">expired `)
	if r := e.post(root+"/invites/"+marloes+"/remove", nil, false); r.code != 303 {
		t.Fatalf("remove invite: %d", r.code)
	}

	// roles: a change answers with the row; the last owner cannot be demoted or removed
	changed := e.post(root+"/"+bob.ID+"/role", url.Values{"role": {"admin"}}, true)
	if changed.code != 200 || !strings.HasPrefix(changed.body, `<div class="vk-srow">`) || !strings.Contains(changed.body, `<option value="admin" selected>admin</option>`) || strings.Contains(changed.body, "<html") {
		t.Fatalf("role change: %d %s", changed.code, changed.body)
	}
	if ms, _ := e.svc.MembershipsForUser(ctx, bob.ID); ms[0].Role != domain.RoleAdmin {
		t.Fatalf("bob's role: %+v", ms)
	}
	if r := e.post(root+"/"+j.ID+"/role", url.Values{"role": {"viewer"}}, true); r.code != 422 || !strings.Contains(r.body, "last owner") {
		t.Fatalf("demote last owner: %d %s", r.code, r.body)
	}
	if r := e.post(root+"/"+j.ID+"/remove", nil, false); r.code != 422 || !strings.Contains(r.body, "last owner") {
		t.Fatalf("remove last owner: %d", r.code)
	}
	if r := e.post(root+"/"+lisa.ID+"/remove", nil, false); r.code != 303 {
		t.Fatalf("remove lisa: %d", r.code)
	}
	if ms, _ := e.svc.MembershipsForUser(ctx, lisa.ID); len(ms) != 0 {
		t.Fatalf("lisa still a member: %+v", ms)
	}

	// owner actions: delete refused while projects exist; transfer makes bob owner and j admin
	if r := e.post(root+"/delete-org", nil, false); r.code != 422 || !strings.Contains(r.body, "its projects first") {
		t.Fatalf("delete with projects: %d", r.code)
	}
	if r := e.post(root+"/transfer", url.Values{"new_owner": {j.ID}}, false); r.code != 422 || !strings.Contains(r.body, "you are the owner already") {
		t.Fatalf("transfer to self: %d", r.code)
	}
	moved := e.post(root+"/transfer", url.Values{"new_owner": {bob.ID}}, false)
	if moved.code != 303 || !strings.Contains(moved.hdr.Get("Location"), "flash=") {
		t.Fatalf("transfer: %d %s", moved.code, moved.body)
	}
	after := e.get(moved.hdr.Get("Location"), false)
	after.has(t, `Ownership of homelab went to Bob Jansen. You stay on as admin.`, `aria-label="Role for Jaro" disabled`, `<option value="admin" selected>admin</option>`, `aria-label="Role for Bob Jansen" hx-post=`, `<option value="owner" selected>owner</option>`)
	if strings.Contains(after.body, "Owner actions") {
		t.Error("an admin gets no owner actions")
	}

	// another org's members routes are 404 for this session, as are its invites
	for _, p := range []string{"/o/acme/admin/members", "/o/acme/admin/members?invite=1"} {
		if r := e.get(p, false); r.code != 404 {
			t.Fatalf("%s: %d", p, r.code)
		}
	}
	if r := e.post("/o/acme/admin/members/invites", url.Values{"inv_for": {"x"}, "inv_role": {"member"}}, false); r.code != 404 {
		t.Fatalf("acme invite: %d", r.code)
	}
	if r := e.post("/o/acme/admin/members/"+bob.ID+"/role", url.Values{"role": {"owner"}}, true); r.code != 404 {
		t.Fatalf("acme role: %d", r.code)
	}
	if r := e.post("/o/acme/admin/members/transfer", url.Values{"new_owner": {bob.ID}}, false); r.code != 404 {
		t.Fatalf("acme transfer: %d", r.code)
	}
}

func TestInstanceAdminPages(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	admin := domain.Scope{InstanceAdmin: true, Role: domain.RoleOwner}
	bob, _ := e.svc.CreateLocalUser(ctx, admin, "bob", "bob@example.com", "Bob Jansen", "correct horse", false)
	_ = e.svc.SetMembership(ctx, admin, bob.ID, e.org.ID, domain.RoleMember)

	// an org admin who is not an instance admin sees nothing here
	for _, path := range []string{"/admin", "/admin/orgs", "/admin/users", "/admin/server", "/admin/users?edit=" + bob.ID} {
		if r := e.get(path, false); r.code != 404 {
			t.Errorf("%s as org admin: %d", path, r.code)
		}
	}
	for _, path := range []string{"/admin/orgs", "/admin/users/" + bob.ID + "/disable", "/admin/users/" + bob.ID + "/reset-link"} {
		if r := e.post(path, url.Values{}, false); r.code != 404 {
			t.Errorf("POST %s as org admin: %d", path, r.code)
		}
	}
	if r := e.get(projPath, false); strings.Contains(r.body, `href="/admin/orgs"`) {
		t.Error("menu offers Instance admin to an org admin")
	}
	if r := e.do("GET", "/reset/rs_nope", nil, false, false); r.code != 404 {
		t.Errorf("unknown reset link: %d", r.code)
	}

	// as instance admin: the shell, the orgs tab and its rows
	if err := e.svc.SetInstanceAdmin(ctx, admin, "j", true); err != nil {
		t.Fatal(err)
	}
	e.get(projPath, false).has(t, `<a class="vk-menu__item" href="/admin/orgs">`, `Instance admin</span>`)
	if r := e.get("/admin", false); r.code != 303 || r.hdr.Get("Location") != "/admin/orgs" {
		t.Fatalf("/admin: %d %s", r.code, r.hdr.Get("Location"))
	}
	orgs := e.get("/admin/orgs", false)
	orgs.has(t, `<header class="vk-top">`, `<summary class="vk-top__crumb" title="Switch project">homelab / <b>prod</b>`, `<h1>Instance</h1><span class="vk-muted vk-mono">vink `, ` · localhost:8080</span>`,
		`<nav class="vk-tabs" aria-label="Instance"><a class="vk-tab" href="/admin/orgs" aria-current="page">Orgs<span class="vk-tab__n">2</span></a><a class="vk-tab" href="/admin/users">Users<span class="vk-tab__n">2</span></a><a class="vk-tab" href="/admin/server">Server</a><a class="vk-tab" href="/admin/audit">Audit log</a></nav>`,
		`Instance admins create orgs and set their quotas.`, `href="/admin/orgs?add=1">Add org</a>`,
		`<a class="vk-srow__link" href="/o/homelab/admin/members">homelab</a></span><span class="vk-srow__sub" title="1 project · no owner">1 project · no owner</span></div><span class="vk-srow__cell vk-srow__cell--l"><span class="vk-usage"><span class="vk-usage__text">0 monitors · no quota</span></span></span>`,
		`href="/o/acme/admin/members">acme</a>`, `href="/admin/orgs?edit=homelab">Edit</a>`, `<p class="vk-field__hint">Only an org without projects can be deleted.</p>`)
	if strings.Contains(orgs.body, "Delete</button>") {
		t.Error("an org with projects offers Delete")
	}

	// add an org: bad quota, unknown owner, then one with a quota and bob as owner
	e.get("/admin/orgs?add=1", false).has(t, `<h2>Add org</h2>`, `id="org_slug" name="org_slug"`, `id="org_owner" name="org_owner"`, `id="org_q_mon" name="org_q_mon"`, `>Create org</button>`)
	if r := e.post("/admin/orgs", url.Values{"org_slug": {"lab"}, "org_q_mon": {"many"}}, false); r.code != 422 || !strings.Contains(r.body, `id="org_q_mon-msg"`) {
		t.Fatalf("bad quota: %d", r.code)
	}
	if r := e.post("/admin/orgs", url.Values{"org_slug": {"lab"}, "org_owner": {"nobody"}}, false); r.code != 422 || !strings.Contains(r.body, `No user named nobody`) {
		t.Fatalf("unknown owner: %d", r.code)
	}
	if r := e.post("/admin/orgs", url.Values{"org_slug": {"lab"}, "org_name": {"Lab"}, "org_owner": {"bob"}, "org_q_mon": {"5"}}, false); r.code != 303 || !strings.HasPrefix(r.hdr.Get("Location"), "/admin/orgs?flash=") {
		t.Fatalf("create: %d %s", r.code, r.hdr.Get("Location"))
	}
	if r := e.post("/admin/orgs", url.Values{"org_slug": {"lab"}, "org_name": {"Lab"}}, false); r.code != 422 || !strings.Contains(r.body, `An org with this slug exists.`) {
		t.Fatalf("duplicate: %d", r.code)
	}
	e.get("/admin/orgs", false).has(t, `href="/o/lab/admin/members">lab</a></span><span class="vk-srow__sub" title="no projects · owner bob">`, `<span class="vk-usage__text">0 / 5 monitors</span>`, `<span class="vk-usage__text">0 agents · no quota</span>`,
		`action="/admin/orgs/lab/delete"`, `data-confirm="Really delete?">Delete</button>`, `Orgs<span class="vk-tab__n">3</span>`)
	e.get("/admin/orgs?edit=lab", false).has(t, `<h2>Edit lab</h2>`, `name="org_slug" aria-describedby="org_slug-msg" disabled type="text" value="lab"`, `name="org_q_mon" aria-describedby="org_q_mon-msg" type="text" value="5"`, `>Save</button>`)
	if r := e.post("/admin/orgs/lab", url.Values{"org_name": {"Lab 2"}, "org_q_mon": {""}, "org_q_ag": {"3"}}, false); r.code != 303 {
		t.Fatalf("edit: %d", r.code)
	}
	if lab, err := e.svc.OrgBySlug(ctx, "lab"); err != nil || lab.Name != "Lab 2" || lab.QuotaMonitors != nil || lab.QuotaAgents == nil || *lab.QuotaAgents != 3 {
		t.Fatalf("after edit: %+v %v", lab, err)
	}
	if r := e.post("/admin/orgs/lab/delete", url.Values{}, false); r.code != 303 || !strings.Contains(r.hdr.Get("Location"), "deleted") {
		t.Fatalf("delete: %d %s", r.code, r.hdr.Get("Location"))
	}
	if _, err := e.svc.OrgBySlug(ctx, "lab"); err == nil {
		t.Fatal("lab still exists")
	}
	if r := e.post("/admin/orgs/homelab/delete", url.Values{}, false); r.code != 303 || !strings.Contains(r.hdr.Get("Location"), "project") {
		t.Fatalf("delete with projects: %d %s", r.code, r.hdr.Get("Location"))
	}

	// the users tab: chips, rows, the filter
	users := e.get("/admin/users", false)
	users.has(t, `aria-current="page">Users<span class="vk-tab__n">2</span>`, `<form class="vk-chips" method="get" action="/admin/users">`, `name="filter" value="local">local<span class="vk-chip__n">2</span>`, `value="admins">instance admins<span class="vk-chip__n">1</span>`, `value="disabled">disabled<span class="vk-chip__n">0</span></button>`,
		`<span class="vk-avatar" aria-hidden="true">J</span></span><div class="vk-srow__main"><span class="vk-srow__title">Jaro <span class="vk-tag">you</span></span><span class="vk-srow__sub" title="j@example.com · local · two-factor off">`,
		`<span class="vk-srow__cell vk-srow__cell--l">homelab admin</span><span class="vk-srow__cell vk-srow__cell--m"><span class="vk-tag">instance admin</span></span><span class="vk-srow__cell vk-srow__cell--m vk-srow__cell--mono">active now</span>`,
		`<span class="vk-srow__title">Bob Jansen</span>`, `vk-srow__cell--mono">never</span>`, `href="/admin/users?edit=`+bob.ID+`">Edit</a>`)
	if r := e.get("/admin/users?filter=admins", false); strings.Contains(r.body, "Bob Jansen") || !strings.Contains(r.body, `aria-pressed="true"`) {
		t.Error("filter admins still lists bob, or chip not pressed")
	}

	// bob's panel, a reset link shown once, the reset page once
	panel := e.get("/admin/users?edit="+bob.ID, false)
	panel.has(t, `<h2>Edit Bob Jansen</h2><p>bob · local account · homelab member</p>`, `<input type="checkbox" name="is_admin" value="1" form="user-form">`, `<span class="vk-state vk-state--paused">`, `>Make a reset link</button>`, `data-confirm="Really disable?" form="disable-form">Disable account</button>`)
	if strings.Contains(panel.body, "Reset two-factor") {
		t.Error("reset two-factor offered without two-factor")
	}
	made := e.post("/admin/users/"+bob.ID+"/reset-link", url.Values{}, false)
	if made.code != 200 {
		t.Fatalf("reset link: %d", made.code)
	}
	made.has(t, `<b class="vk-notice__title">Reset link created.</b> It works once, until `, `<code class="vk-ping__url">http://localhost:8080/reset/rs_`)
	token := regexp.MustCompile(`/reset/(rs_[A-Za-z0-9_-]+)`).FindStringSubmatch(made.body)[1]
	e.do("GET", "/reset/"+token, nil, false, false).has(t, `<h1>Set a new password</h1>`, `<b>Jaro</b> made this link for <b>Bob Jansen (bob)</b>`, `name="password"`, `>Save password</button>`)
	if r := e.do("POST", "/reset/"+token, url.Values{"password": {"short"}}, false, false); r.code != 422 || !strings.Contains(r.body, "at least") {
		t.Fatalf("short password: %d", r.code)
	}
	done := e.do("POST", "/reset/"+token, url.Values{"password": {"a brand new passphrase"}}, false, false)
	if done.code != 303 || done.hdr.Get("Location") != "/" || len(done.hdr.Values("Set-Cookie")) == 0 {
		t.Fatalf("reset: %d %s", done.code, done.hdr.Get("Location"))
	}
	if r := e.do("GET", "/reset/"+token, nil, false, false); r.code != 410 || !strings.Contains(r.body, "This reset link has expired") {
		t.Fatalf("used link: %d", r.code)
	}
	if _, err := e.svc.VerifyPassword(ctx, "bob", "a brand new passphrase"); err != nil {
		t.Fatalf("new password: %v", err)
	}

	// disable signs bob out everywhere; you cannot disable yourself
	rec := httptest.NewRecorder()
	if _, err := e.authn.Login(rec, httptest.NewRequest("POST", "/login", nil), "bob", "a brand new passphrase"); err != nil {
		t.Fatal(err)
	}
	bobCookie := rec.Result().Cookies()[0]
	asBob := func() int {
		req := httptest.NewRequest("GET", projPath, nil)
		req.AddCookie(bobCookie)
		w := httptest.NewRecorder()
		e.srv.ServeHTTP(w, req)
		return w.Code
	}
	if code := asBob(); code != 200 {
		t.Fatalf("bob before disable: %d", code)
	}
	if r := e.post("/admin/users/"+bob.ID+"/disable", url.Values{}, false); r.code != 303 || !strings.Contains(r.hdr.Get("Location"), "signed+out") {
		t.Fatalf("disable: %d %s", r.code, r.hdr.Get("Location"))
	}
	if code := asBob(); code != 303 {
		t.Fatalf("bob after disable: %d", code)
	}
	e.get("/admin/users", false).has(t, `title="bob@example.com · local · disabled by j on 27 Sep"`, `<span class="vk-tag">disabled</span>`, `value="disabled">disabled<span class="vk-chip__n">1</span>`)
	e.get("/admin/users?edit="+bob.ID, false).has(t, `>Enable account</button>`)
	if r := e.post("/admin/users/"+bob.ID+"/enable", url.Values{}, false); r.code != 303 {
		t.Fatalf("enable: %d", r.code)
	}
	j, _ := e.svc.UserBySubject(ctx, "j")
	if r := e.post("/admin/users/"+j.ID+"/disable", url.Values{}, false); r.code != 422 || !strings.Contains(r.body, "You cannot disable yourself.") {
		t.Fatalf("self disable: %d", r.code)
	}
	self := e.get("/admin/users?edit="+j.ID, false)
	self.has(t, `name="is_admin" value="1" checked disabled form="user-form"`, `You cannot take instance admin from yourself.`)
	if strings.Contains(self.body, "Disable account") {
		t.Error("self panel offers Disable")
	}
	if r := e.post("/admin/users/"+bob.ID, url.Values{"is_admin": {"1"}}, false); r.code != 303 {
		t.Fatalf("save: %d", r.code)
	}
	if u, _ := e.svc.UserByID(ctx, bob.ID); !u.InstanceAdmin {
		t.Fatal("bob not instance admin after save")
	}

	// the server tab: facts and the backup warning
	e.web.SetServerFacts(func(context.Context) ServerFacts {
		return ServerFacts{Build: [][2]string{{"version", "test"}}, Database: [][2]string{{"path", "/tmp/x.db"}}, SignIn: [][2]string{{"local accounts", "on"}}, Network: [][2]string{{"base url", "http://localhost:8080"}}}
	})
	server := e.get("/admin/server", false)
	server.has(t, `aria-current="page">Server</a>`, `<span class="vk-mono">vink serve --print-config</span>`, `<b class="vk-notice__title">No backup yet.</b> Run vink admin backup on the server`,
		`<dl class="vk-kv"><dt>version</dt><dd>test</dd></dl>`, `<dt>path</dt><dd>/tmp/x.db</dd><dt>last backup</dt><dd>never</dd></dl>`, `<dt>local accounts</dt><dd>on</dd>`, `<dt>base url</dt><dd>http://localhost:8080</dd>`)
	if err := db.New(e.db.Writer).SetInstanceMeta(ctx, db.SetInstanceMetaParams{Name: service.MetaLastBackup, Value: e.now.Add(-3 * time.Hour).Format(time.RFC3339), UpdatedAt: e.now.UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	fresh := e.get("/admin/server", false)
	fresh.has(t, `<dt>last backup</dt><dd>Sun 27 Sep 09:00, 3 h ago</dd>`)
	if strings.Contains(fresh.body, "vk-notice--warn") {
		t.Error("warning with a fresh backup")
	}
	e.now = e.now.Add(48 * time.Hour)
	e.get("/admin/server", false).has(t, `<b class="vk-notice__title">No backup for 2 d.</b>`)
}

func TestAuditLogPages(t *testing.T) {
	e := newEnv(t)
	ctx := audit.WithRequest(context.Background(), audit.Request{Via: audit.ViaWeb, RequestID: "01REQ", RemoteAddr: "10.0.4.12"})
	admin := domain.Scope{InstanceAdmin: true, Role: domain.RoleOwner, Actor: "user:root"}
	j, _ := e.svc.UserBySubject(ctx, "j")
	jsc := domain.Scope{OrgID: e.org.ID, ProjectID: e.project.ID, UserID: j.ID, Role: domain.RoleAdmin, Actor: "user:j"}
	bob, _ := e.svc.CreateLocalUser(ctx, admin, "bob", "bob@example.com", "Bob Jansen", "correct horse", false)
	if err := e.svc.SetMembership(ctx, jsc, bob.ID, e.org.ID, domain.RoleMember); err != nil {
		t.Fatal(err)
	}
	// a monitor made, changed and pinged: two change rows and a state flip
	if _, err := e.svc.CreateMonitor(ctx, jsc, &domain.Monitor{Slug: "api", Name: "API", Kind: domain.KindHTTP, Pull: &domain.PullSpec{Interval: domain.MustDuration("120s"), HTTP: &domain.HTTPCheck{URL: "https://api.example.com"}}}); err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(time.Minute)
	if _, err := e.svc.UpdateMonitor(ctx, jsc, "api", &domain.Monitor{Name: "API", Pull: &domain.PullSpec{Interval: domain.MustDuration("30s"), HTTP: &domain.HTTPCheck{URL: "https://api.example.com"}}}); err != nil {
		t.Fatal(err)
	}
	e.monitor("nightly")
	tgt, _ := e.svc.ResolvePing(ctx, e.project.PingKey, "nightly", "", false)
	e.now = e.now.Add(time.Minute)
	_, _, _ = e.svc.RecordPing(ctx, tgt, service.PingObservation{Signal: domain.SignalOK, RemoteAddr: "192.168.30.5"})
	root := "/o/homelab/admin/audit"

	// the org admin sees everything, newest first, in day groups
	all := e.get(root, false)
	if all.code != 200 {
		t.Fatalf("audit tab: %d", all.code)
	}
	all.has(t, `<a class="vk-tab" href="/o/homelab/admin/audit" aria-current="page">Audit log</a></nav>`, `Everything that happened in homelab:`,
		`<form class="vk-filterbar" method="get" action="/o/homelab/admin/audit" hx-get="/o/homelab/admin/audit" hx-target="#tab" hx-trigger="submit, change" hx-push-url="true"><input type="hidden" name="kind" value="">`,
		`name="kind" value="changes">changes<span class="vk-chip__n">5</span>`, `name="kind" value="access">access<span class="vk-chip__n">3</span>`, `name="kind" value="state">state<span class="vk-chip__n">1</span>`,
		`<select class="vk-input" name="project" aria-label="Project"><option value="" selected>All projects</option><option value="prod">prod</option></select>`,
		`<select class="vk-input" name="actor" aria-label="Who"><option value="" selected>Anyone</option><option value="j">j</option><option value="test">test</option><option value="vink">vink</option></select>`,
		`<div class="vk-seg vk-seg--mono" role="radiogroup" aria-label="Period">`, `<input type="radio" name="period" value="7d" checked>`,
		`<h2 class="vk-listhead">Today <span>Sun 27 Sep</span></h2>`,
		`<span class="vk-audit__time" title="Sun 27 Sep 14:02:00 CEST">14:02</span><span class="vk-audit__who"><i class="vk-glyph vk-glyph--up vk-audit__glyph" aria-hidden="true"></i><span>vink</span></span><span class="vk-audit__what"><code>nightly</code> is up</span><span class="vk-audit__scope"><span class="vk-tag">prod</span></span><span class="vk-audit__via">ping</span>`,
		`<span class="vk-audit__what">changed monitor <code>api</code>: interval</span><span class="vk-audit__scope"><span class="vk-tag">prod</span></span><span class="vk-audit__via">web</span></summary><div class="vk-audit__body"><pre class="vk-diff">`,
		`<span class="vk-diff__line vk-diff__line--del"><i aria-hidden="true">-</i>interval: `, `</span><span class="vk-diff__line vk-diff__line--add"><i aria-hidden="true">+</i>interval: 30s</span>`,
		`<dl class="vk-kv"><dt>request</dt><dd>01REQ</dd><dt>from</dt><dd>10.0.4.12</dd></dl>`,
		`<span class="vk-audit__what">added bob as member</span><span class="vk-audit__scope"><span class="vk-tag">homelab</span></span>`, `created project <code>prod</code>`,
		`<span class="vk-avatar" aria-hidden="true">J</span><span>j</span>`)
	if strings.Count(all.body, `<details class="vk-audit" open>`) != 1 {
		t.Error("exactly the newest row with a body opens")
	}

	// filters are links: kind, who, project, period; a strange project is a 404
	access := e.get(root+"?kind=access", false)
	access.has(t, `<input type="hidden" name="kind" value="access">`, `aria-pressed="true" name="kind" value=""`, `added bob as member`)
	if strings.Contains(access.body, "changed monitor") || strings.Contains(access.body, "is up") {
		t.Error("access filter shows other kinds")
	}
	e.get(root+"?kind=changes,state&actor=j", false).has(t, `created monitor <code>api</code>`, `<option value="j" selected>j</option>`)
	if r := e.get(root+"?project=nope", false); r.code != 404 {
		t.Errorf("unknown project: %d", r.code)
	}
	if r := e.get(root+"?period=24h&project=prod", false); r.code != 200 || !strings.Contains(r.body, `value="24h" checked`) || !strings.Contains(r.body, `<option value="prod" selected>prod</option>`) {
		t.Errorf("period and project: %d", r.code)
	}
	e.now = e.now.Add(26 * time.Hour)
	if r := e.get(root+"?period=24h", false); !strings.Contains(r.body, `<h3>Nothing in the last 24 hours</h3>`) {
		t.Error("empty period")
	}
	e.get(root, false).has(t, `<h2 class="vk-listhead">Yesterday <span>Sun 27 Sep</span></h2>`)
	if r := e.get(root+"?kind=state", true); strings.Contains(r.body, "<html") || !strings.HasPrefix(r.body, `<div class="vk-tabhead">`) {
		t.Error("htmx request renders the whole page")
	}

	// a member sees project rows only, and only this tab
	rec := httptest.NewRecorder()
	if _, err := e.authn.Login(rec, httptest.NewRequest("POST", "/login", nil), "bob", "correct horse"); err != nil {
		t.Fatal(err)
	}
	jCookie := e.cookie
	e.cookie = rec.Result().Cookies()[0]
	mine := e.get(root, false)
	if mine.code != 200 {
		t.Fatalf("member audit: %d", mine.code)
	}
	mine.has(t, `<nav class="vk-tabs" aria-label="Org settings"><a class="vk-tab" href="/o/homelab/admin/audit" aria-current="page">Audit log</a></nav>`, `Everything that happened in your projects in homelab:`, `changed monitor <code>api</code>`, `<code>nightly</code> is up`)
	for _, s := range []string{"added bob as member", "created the account", "signed in", `name="kind" value="access">access<span class="vk-chip__n">3</span>`} {
		if strings.Contains(mine.body, s) {
			t.Errorf("member sees %q", s)
		}
	}
	if r := e.get("/o/homelab/admin/members", false); r.code != 403 {
		t.Errorf("member on members: %d", r.code)
	}
	if r := e.get("/o/acme/admin/audit", false); r.code != 404 {
		t.Errorf("another org's log: %d", r.code)
	}
	if r := e.get("/admin/audit", false); r.code != 404 {
		t.Errorf("instance log as member: %d", r.code)
	}
	e.cookie = jCookie
	if r := e.get("/o/acme/admin/audit", false); r.code != 404 {
		t.Errorf("another org's log as org admin: %d", r.code)
	}

	// 50 rows a page, then Older
	for i := 0; i < 60; i++ {
		e.now = e.now.Add(time.Second)
		_ = e.svc.RecordSignIn(ctx, j, "j", "password", true)
	}
	first := e.get(root+"?kind=access", false)
	if strings.Count(first.body, `signed in with a password`) != 50 {
		t.Fatalf("rows on the first page: %d", strings.Count(first.body, `signed in with a password`))
	}
	older := regexp.MustCompile(`href="(/o/homelab/admin/audit\?[^"]*before=[^"]*)">Older</a>`).FindStringSubmatch(first.body)
	if older == nil {
		t.Fatal("no Older link")
	}
	second := e.get(strings.ReplaceAll(older[1], "&amp;", "&"), false)
	// 60 recorded here plus the two browser sign-ins above
	if second.code != 200 || strings.Count(second.body, `signed in with a password`) != 12 || strings.Contains(second.body, ">Older</a>") {
		t.Fatalf("second page: %d, %d rows", second.code, strings.Count(second.body, `signed in with a password`))
	}

	// the instance-wide list, with an org select and org/project tags
	if err := e.svc.SetInstanceAdmin(ctx, admin, "j", true); err != nil {
		t.Fatal(err)
	}
	inst := e.get("/admin/audit?kind=changes,state", false)
	if inst.code != 200 {
		t.Fatalf("instance audit: %d", inst.code)
	}
	inst.has(t, `aria-current="page">Audit log</a>`, `across orgs`, `<select class="vk-input" name="org" aria-label="Org"><option value="" selected>All orgs</option><option value="acme">acme</option><option value="homelab">homelab</option></select>`,
		`<option value="prod">acme/prod</option><option value="prod">homelab/prod</option>`, `<span class="vk-tag">homelab/prod</span>`, `created project <code>prod</code>`)
	if r := e.get("/admin/audit?org=acme&kind=changes", false); r.code != 200 || !strings.Contains(r.body, `<span class="vk-tag">acme/prod</span>`) || strings.Contains(r.body, "homelab/prod") {
		t.Errorf("org filter: %d", r.code)
	}
	if r := e.get("/admin/audit?org=nope", false); r.code != 404 {
		t.Errorf("unknown org: %d", r.code)
	}
}

func cookieNamed(hdr http.Header, name string) *http.Cookie {
	for _, c := range (&http.Response{Header: hdr}).Cookies() {
		if c.Name == name && c.Value != "" {
			return c
		}
	}
	return nil
}

// doWith sends a request with explicit cookies and no CSRF token.
func (e *env) doWith(method, path string, form url.Values, cookies ...*http.Cookie) page {
	e.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, path, body)
	req.RemoteAddr = "203.0.113.9:1"
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	e.srv.ServeHTTP(rec, req)
	return page{code: rec.Code, body: rec.Body.String(), hdr: rec.Header()}
}

func TestAccountAndTwoFactor(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	acc := e.get("/account", false)
	if acc.code != 200 {
		t.Fatalf("account: %d", acc.code)
	}
	acc.has(t, `<main class="vk-main vk-narrow" id="account">`, `<h1>Account</h1><span class="vk-muted vk-mono">j · local account</span>`,
		`<section class="vk-section"><h2>Profile</h2>`, `id="acc_name" name="acc_name"`, `value="Jaro"`, `id="acc_email" name="acc_email"`, `value="j@example.com"`, `Shown to other members. Alert mail goes to channels, not here.`, `>Save profile</button>`,
		`<section class="vk-section"><h2>Password</h2>`, `<form class="vk-inlineform vk-inlineform--even" action="/account/password" method="post">`, `id="pw_old" name="pw_old"`, `autocomplete="current-password"`, `>Change password</button>`,
		`<h2>Two-factor sign-in</h2><p class="vk-lede">Ask for a code from an authenticator app after the password.`,
		`<span class="vk-srow__title">Authenticator app</span><span class="vk-srow__sub" title="off · a code from your phone after the password">`, `<span class="vk-state vk-state--paused">`, `href="/account?setup=1">Turn on</a>`,
		`<h2>Sessions <span>1</span></h2>`, `<span class="vk-tag">this session</span>`, `since Sun 27 Sep 14:00 · 203.0.113.9`, `vk-srow__cell--mono">active now</span><span class="vk-srow__actions"></span>`)
	if strings.Contains(acc.body, "Sign out everywhere else") {
		t.Error("nothing else to sign out")
	}
	e.get(projPath, false).has(t, `<a class="vk-menu__item" href="/account">`)
	// the page has the top bar, so there is a way back and out
	acc.has(t, `<header class="vk-top">`, `<summary class="vk-top__crumb" title="Switch project">homelab / <b>prod</b>`, `href="/logout"><span>Sign out</span>`)

	// profile and password
	if r := e.post("/account/profile", url.Values{"acc_name": {"Jaro Z"}, "acc_email": {"nope"}}, false); r.code != 422 || !strings.Contains(r.body, `id="acc_email-msg"`) {
		t.Fatalf("bad email: %d", r.code)
	}
	if r := e.post("/account/profile", url.Values{"acc_name": {"Jaro Z"}, "acc_email": {"jz@example.com"}}, false); r.code != 303 {
		t.Fatalf("profile: %d", r.code)
	}
	if u, _ := e.svc.UserBySubject(ctx, "j"); u.DisplayName != "Jaro Z" || u.Email != "jz@example.com" {
		t.Fatalf("saved: %+v", u)
	}
	if r := e.post("/account/password", url.Values{"pw_old": {"wrong"}, "pw_new": {"a brand new passphrase"}}, false); r.code != 422 || !strings.Contains(r.body, `id="pw_old-msg"`) {
		t.Fatalf("wrong current: %d", r.code)
	}
	rec := httptest.NewRecorder()
	phone := httptest.NewRequest("POST", "/login", nil)
	phone.Header.Set("User-Agent", "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 Version/17.0 Mobile/15E148 Safari/604.1")
	phone.RemoteAddr = "10.0.4.31:1"
	if _, err := e.authn.Login(rec, phone, "j", "correct horse"); err != nil {
		t.Fatal(err)
	}
	other := rec.Result().Cookies()[0]
	e.get("/account", false).has(t, `<h2>Sessions <span>2</span></h2>`, `<span class="vk-srow__title">Safari on iPhone</span><span class="vk-srow__sub" title="since Sun 27 Sep 14:00 · 10.0.4.31">`, `action="/account/sessions/`+other.Value+`/delete"`, `>Sign out</button>`, `data-confirm="Really sign out 1 session?">Sign out everywhere else</button>`)
	if r := e.post("/account/password", url.Values{"pw_old": {"correct horse"}, "pw_new": {"a brand new passphrase"}}, false); r.code != 303 {
		t.Fatalf("password: %d", r.code)
	}
	if _, err := e.svc.Session(ctx, other.Value); err == nil {
		t.Fatal("the phone stayed signed in")
	}

	// setup: QR, key, a wrong code, then the right one with the codes shown once
	setup := e.get("/account?setup=1", false)
	setup.has(t, `<h2>Set up two-factor sign-in</h2></div><div class="vk-two">`, `<figure class="vk-qr"><div class="vk-qr__code" role="img" aria-label="QR code for localhost:8080, account j"><svg viewBox="0 0 `, `shape-rendering="crispEdges" aria-hidden="true"><path fill="currentColor" d="M`,
		`<figcaption>Scan with an authenticator app.</figcaption></figure>`, `<span class="vk-field__label">Or type this key</span><div class="vk-field__row"><code class="vk-ping__url">`, `Account j at localhost:8080 · 6 digits · a new code every 30 s`,
		`class="vk-input vk-input--otp" id="otp" name="otp"`, `inputmode="numeric" pattern="[0-9]{6}" maxlength="6" autocomplete="one-time-code"`, `placeholder="000000"`, `>Turn on two-factor</button>`, `href="/account">Cancel</a>`)
	secret := regexp.MustCompile(`data-copy="([A-Z2-7]{32})"`).FindStringSubmatch(setup.body)
	if secret == nil {
		t.Fatal("no key on the page")
	}
	if again := e.get("/account?setup=1", false); !strings.Contains(again.body, secret[1]) {
		t.Error("the pending key must stay the same")
	}
	if r := e.post("/account/totp/confirm", url.Values{"otp": {"000000"}}, false); r.code != 422 || !strings.Contains(r.body, `That code didn’t work`) || !strings.Contains(r.body, secret[1]) {
		t.Fatalf("wrong code: %d", r.code)
	}
	code, _ := totp.Code(secret[1], e.now)
	on := e.post("/account/totp/confirm", url.Values{"otp": {code}}, false)
	if on.code != 200 {
		t.Fatalf("confirm: %d", on.code)
	}
	on.has(t, `<div class="vk-srow__note"><div class="vk-notice vk-notice--ok" role="status">`, `<b class="vk-notice__title">Two-factor is on.</b> Keep these recovery codes somewhere safe`, `<div class="vk-codes"><ol class="vk-codes__list"><li>`, `>Copy codes</button>`,
		`title="on since just now · 10 of 10 recovery codes left"`, `<span class="vk-state vk-state--up">`, `data-confirm="Really replace the codes?" form="codes-form">New codes</button>`, `href="/account?off=1">Turn off</a>`)
	codes := regexp.MustCompile(`<li>([A-Z2-9]{4}-[A-Z2-9]{4})</li>`).FindAllStringSubmatch(on.body, -1)
	if len(codes) != 10 {
		t.Fatalf("%d recovery codes", len(codes))
	}
	if r := e.get("/account", false); strings.Contains(r.body, "vk-codes__list") {
		t.Error("codes shown twice")
	}

	// signing in: the password opens the code step
	if r := e.get("/logout", false); r.code != 303 {
		t.Fatal("logout")
	}
	login := e.doWith("POST", "/login", url.Values{"username": {"j"}, "password": {"a brand new passphrase"}, "next": {"/account"}})
	if login.code != 303 || login.hdr.Get("Location") != "/login/code" || cookieNamed(login.hdr, auth.CookieName) != nil {
		t.Fatalf("password step: %d %s", login.code, login.hdr.Get("Location"))
	}
	ch := cookieNamed(login.hdr, auth.ChallengeCookie)
	if ch == nil || !ch.HttpOnly {
		t.Fatal("no challenge cookie")
	}
	step := e.doWith("GET", "/login/code", nil, ch)
	step.has(t, `<h1>Two-factor sign-in</h1>`, `Enter the code from your authenticator app for <b>j</b>.`, `class="vk-input vk-input--otp" id="otp" name="otp"`, `>Verify</button>`, `href="/login/code?recovery=1">Use a recovery code</a>`, `href="/login">Back to sign in</a>`)
	if r := e.doWith("GET", "/login/code", nil); r.code != 303 || r.hdr.Get("Location") != "/login?code=expired" {
		t.Fatalf("code page without a challenge: %d %s", r.code, r.hdr.Get("Location"))
	}
	// the setup code is spent; a wrong code says the same as a reused one
	if r := e.doWith("POST", "/login/code", url.Values{"otp": {code}}, ch); r.code != 401 || !strings.Contains(r.body, `<span class="vk-field__error" id="otp-msg"><i class="vk-glyph vk-glyph--down" aria-hidden="true"></i>That code didn’t work. Use the code on screen now; it changes every 30 seconds.</span>`) {
		t.Fatalf("spent code: %d", r.code)
	}
	e.now = e.now.Add(90 * time.Second)
	fresh, _ := totp.Code(secret[1], e.now)
	ok := e.doWith("POST", "/login/code", url.Values{"otp": {fresh}}, ch)
	sessCookie := cookieNamed(ok.hdr, auth.CookieName)
	if ok.code != 303 || ok.hdr.Get("Location") != "/account" || sessCookie == nil {
		t.Fatalf("code step: %d %s", ok.code, ok.hdr.Get("Location"))
	}
	if r := e.doWith("GET", "/login/code", nil, ch); r.code != 303 || r.hdr.Get("Location") != "/login?code=expired" {
		t.Fatalf("challenge after use: %d", r.code)
	}
	// replay at a new sign-in
	login2 := e.doWith("POST", "/login", url.Values{"username": {"j"}, "password": {"a brand new passphrase"}})
	ch2 := cookieNamed(login2.hdr, auth.ChallengeCookie)
	if r := e.doWith("POST", "/login/code", url.Values{"otp": {fresh}}, ch2); r.code != 401 {
		t.Fatalf("replay: %d", r.code)
	}
	// a recovery code, once
	e.doWith("GET", "/login/code?recovery=1", nil, ch2).has(t, `Enter one of your recovery codes for <b>j</b>.`, `class="vk-input vk-input--mono" id="recovery" name="recovery"`, `placeholder="XXXX-XXXX"`, `href="/login/code">Use the app</a>`)
	if r := e.doWith("POST", "/login/code", url.Values{"recovery": {strings.ToLower(codes[0][1])}}, ch2); r.code != 303 || r.hdr.Get("Location") != "/" {
		t.Fatalf("recovery: %d %s", r.code, r.hdr.Get("Location"))
	}
	login3 := e.doWith("POST", "/login", url.Values{"username": {"j"}, "password": {"a brand new passphrase"}})
	ch3 := cookieNamed(login3.hdr, auth.ChallengeCookie)
	if r := e.doWith("POST", "/login/code", url.Values{"recovery": {codes[0][1]}}, ch3); r.code != 401 || !strings.Contains(r.body, "Each one works once") {
		t.Fatalf("recovery twice: %d", r.code)
	}
	// five wrong codes end the attempt
	for i := 0; i < 3; i++ {
		if r := e.doWith("POST", "/login/code", url.Values{"otp": {"000000"}}, ch3); r.code != 401 {
			t.Fatalf("wrong %d: %d", i, r.code)
		}
	}
	locked := e.doWith("POST", "/login/code", url.Values{"otp": {"000000"}}, ch3)
	if locked.code != 303 || locked.hdr.Get("Location") != "/login?code=locked" {
		t.Fatalf("lock: %d %s", locked.code, locked.hdr.Get("Location"))
	}
	e.doWith("GET", "/login?code=locked", nil).has(t, `Too many wrong codes. Sign in again.`)

	// the log says how each sign-in went
	e.cookie = sessCookie
	sess, err := e.svc.Session(ctx, sessCookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	e.csrf = sess.CSRF
	e.get("/o/homelab/admin/audit?kind=access", false).has(t, `turned on two-factor sign-in`, `signed in with a code`, `signed in with a recovery code; 9 left`, `failed to sign in as j with a code`, `changed the password of j`, `changed name, email of the account j`)

	// new codes replace the set; turning off needs the password
	e.post("/account/totp/codes", url.Values{}, false).has(t, `<b class="vk-notice__title">New recovery codes.</b>`, `title="on since 1 min ago · 10 of 10 recovery codes left"`)
	e.get("/account?off=1", false).has(t, `<h2>Turn off two-factor sign-in</h2>`, `id="password" name="password"`, `>Turn off two-factor</button>`)
	if r := e.post("/account/totp/off", url.Values{"password": {"wrong"}}, false); r.code != 422 || !strings.Contains(r.body, "That is not your password.") {
		t.Fatalf("off with a wrong password: %d", r.code)
	}
	if r := e.post("/account/totp/off", url.Values{"password": {"a brand new passphrase"}}, false); r.code != 303 {
		t.Fatalf("off: %d", r.code)
	}
	if u, _ := e.svc.UserBySubject(ctx, "j"); u.TOTPOn() {
		t.Fatal("still on")
	}

	// sessions: sign out one, then everywhere else
	var extra *service.Session
	for i := 0; i < 2; i++ {
		extra, err = e.svc.CreateSessionWith(ctx, sess.UserID, "10.0.4.31", "curl/8.4.0")
		if err != nil {
			t.Fatal(err)
		}
	}
	// this session, the one the recovery code started, and the two above
	e.get("/account", false).has(t, `<h2>Sessions <span>4</span></h2>`, `<span class="vk-srow__title">curl</span>`, `data-confirm="Really sign out 3 sessions?"`)
	if r := e.post("/account/sessions/"+extra.ID+"/delete", url.Values{}, false); r.code != 303 {
		t.Fatalf("sign out one: %d", r.code)
	}
	if r := e.post("/account/sessions/"+sessCookie.Value+"/delete", url.Values{}, false); r.code != 404 {
		t.Fatalf("sign out this session: %d", r.code)
	}
	if r := e.post("/account/sessions/others", url.Values{}, false); r.code != 303 || !strings.Contains(r.hdr.Get("Location"), "2+other+sessions") {
		t.Fatalf("others: %d %s", r.code, r.hdr.Get("Location"))
	}
	if list, _ := e.svc.Sessions(ctx, sess.UserID); len(list) != 1 {
		t.Fatalf("%d sessions left", len(list))
	}
}

func TestTOTPRequiredGate(t *testing.T) {
	cfg := config.Default().Auth
	cfg.Local.TOTP = "required"
	e := newEnvAuth(t, cfg)
	if r := e.get(projPath, false); r.code != 303 || r.hdr.Get("Location") != "/account?setup=1" {
		t.Fatalf("gate: %d %s", r.code, r.hdr.Get("Location"))
	}
	if r := e.post("/o/homelab/admin/members/invites", url.Values{"inv_for": {"x"}}, false); r.code != 303 {
		t.Fatalf("gate on POST: %d", r.code)
	}
	e.get("/account?setup=1", false).has(t, `<h2>Set up two-factor sign-in</h2>`)
	if r := e.get("/logout", false); r.code != 303 || r.hdr.Get("Location") != "/login" {
		t.Fatalf("logout through the gate: %d", r.code)
	}
}

func TestOIDCSignInPage(t *testing.T) {
	p := oidctest.New(t)
	p.Claims = map[string]any{"preferred_username": "alice", "groups": []string{"vink:homelab:member"}}
	cfg := config.Default().Auth
	p.Configure(&cfg)
	e := newEnvAuth(t, cfg)

	// the page leads with the provider and keeps the local form below the divider
	page := e.doWith("GET", "/login", nil)
	page.has(t, `<h1>Sign in</h1>`, `<a class="vk-btn vk-btn--primary vk-btn--block" href="/auth/oidc/start">Continue with Keycloak</a>`, `<p>You sign in at `+p.Host()+` and come straight back.</p>`,
		`<div class="vk-or" role="separator"><span>or use a local account</span></div>`, `id="username" name="username"`, `<button type="submit" class="vk-btn vk-btn--block">Sign in</button>`, `<p>Local accounts are for break-glass and service users.</p>`)
	e.doWith("GET", "/login?oidc_error=access_denied", nil).has(t, `<b class="vk-notice__title">Sign-in through Keycloak failed.</b> The provider answered</p><span class="vk-mono">access_denied</span>.`)

	// start, the provider, the callback, the session
	st := e.doWith("GET", "/auth/oidc/start?next=/account", nil)
	flight := cookieNamed(st.hdr, auth.OIDCCookie)
	if st.code != 303 || !strings.HasPrefix(st.hdr.Get("Location"), p.Server.URL+"/auth?") || flight == nil {
		t.Fatalf("start: %d %s", st.code, st.hdr.Get("Location"))
	}
	if r := e.doWith("GET", "/auth/oidc/callback?code=x&state=forged", nil, flight); r.code != 303 || r.hdr.Get("Location") != "/login?oidc_error=state_mismatch" {
		t.Fatalf("forged state: %d %s", r.code, r.hdr.Get("Location"))
	}
	done := e.doWith("GET", p.Visit(t, st.hdr.Get("Location")), nil, flight)
	sess := cookieNamed(done.hdr, auth.CookieName)
	if done.code != 303 || done.hdr.Get("Location") != "/account" || sess == nil {
		t.Fatalf("callback: %d %s", done.code, done.hdr.Get("Location"))
	}
	acc := e.doWith("GET", "/account", nil, sess)
	acc.has(t, `<span class="vk-muted vk-mono">alice · oidc account</span>`, `Comes from the identity provider.`, `<h2>Sessions <span>1</span></h2>`)
	for _, s := range []string{"<h2>Password</h2>", "Two-factor sign-in"} {
		if strings.Contains(acc.body, s) {
			t.Errorf("oidc account offers %q", s)
		}
	}
	if r := e.doWith("GET", "/auth/oidc/callback?code=x&state=forged", nil); r.code != 303 || r.hdr.Get("Location") != "/login?oidc_error=expired" {
		t.Fatalf("callback without a flight: %d %s", r.code, r.hdr.Get("Location"))
	}

	// local accounts off: the button is the card, or /login goes straight to the provider
	cfg.Local.Enabled = false
	e2 := newEnvAuth(t, cfg)
	only := e2.doWith("GET", "/login", nil)
	if only.code != 200 || strings.Contains(only.body, `name="username"`) || !strings.Contains(only.body, `Continue with Keycloak`) {
		t.Fatalf("oidc only: %d", only.code)
	}
	if r := e2.doWith("GET", projPath, nil); r.code != 303 || !strings.HasPrefix(r.hdr.Get("Location"), "/login?next=") {
		t.Fatalf("anonymous with oidc only: %d %s", r.code, r.hdr.Get("Location"))
	}
	cfg.OIDC.AutoRedirect = true
	e3 := newEnvAuth(t, cfg)
	if r := e3.doWith("GET", "/login?next=/account", nil); r.code != 303 || r.hdr.Get("Location") != "/auth/oidc/start?next=%2Faccount" {
		t.Fatalf("auto redirect: %d %s", r.code, r.hdr.Get("Location"))
	}
	if r := e3.doWith("GET", "/login?oidc_error=access_denied", nil); r.code != 200 {
		t.Fatalf("error page must not loop: %d", r.code)
	}
}

func TestHistoryPage(t *testing.T) {
	e := newEnv(t)
	e.monitor("nightly", "backup")
	ctx := context.Background()
	tgt, _ := e.svc.ResolvePing(ctx, e.project.PingKey, "nightly", "", false)
	for i := 0; i < 60; i++ {
		e.now = e.now.Add(time.Minute)
		if _, _, err := e.svc.RecordPing(ctx, tgt, service.PingObservation{Signal: domain.SignalOK, RemoteAddr: "192.168.30.5"}); err != nil {
			t.Fatal(err)
		}
	}
	path := projPath + "/m/nightly/history"
	full := e.get(path, false)
	if full.code != 200 {
		t.Fatalf("history page: %d", full.code)
	}
	full.has(t, `<title>Nightly · history · vink</title>`, `class="vk-page vk-page--full"`, `<div id="history-head" class="vk-section" hx-get="/o/homelab/p/prod/m/nightly/history?newest=`, `hx-trigger="every 10s`,
		`<h1>Nightly <span class="vk-row__slug">nightly</span></h1>`, `>Back<`, `>Edit<`, `>Pause<`, "every 1h · grace 5m · due in", `aria-label="last 24 hours"`, `aria-label="last 90 days"`,
		`<h2 class="vk-listhead">Today <span><a class="vk-link" href="/o/homelab/p/prod/m/nightly/history?since=`, `name="kind" value="fail"`, `name="kind" value="change"`, `name="period" value="7d" checked`,
		`class="vk-irows"`, `ok · from 192.168.30.5`, `hx-trigger="revealed"`, `>Older<`)
	if strings.Count(full.body, `class="vk-obs"`) != 50 || strings.Contains(full.body, "new → up") {
		t.Fatalf("first page: %d rows, event on it: %v", strings.Count(full.body, `class="vk-obs"`), strings.Contains(full.body, "new → up"))
	}
	// the sentinel's partial continues the same day without a heading and ends without a sentinel
	rowsURL := regexp.MustCompile(`hx-get="([^"]*partial=rows[^"]*)"`).FindStringSubmatch(full.body)
	if rowsURL == nil {
		t.Fatal("no rows sentinel")
	}
	rows := e.get(html.UnescapeString(rowsURL[1]), true)
	if rows.code != 200 || strings.Contains(rows.body, "<html") || strings.Contains(rows.body, "vk-listhead") || strings.Contains(rows.body, "revealed") {
		t.Fatalf("rows partial: %d %s", rows.code, rows.body)
	}
	if n := strings.Count(rows.body, `class="vk-obs"`); n != 11 || !strings.Contains(rows.body, "new → up · first ok") {
		t.Fatalf("second page: %d rows: %s", n, rows.body)
	}
	// the plain Older link is a full page from that cursor
	older := regexp.MustCompile(`<a class="vk-btn" href="([^"]*before=[^"]*)">Older</a>`).FindStringSubmatch(full.body)
	if older == nil {
		t.Fatal("no plain Older link")
	}
	if p := e.get(html.UnescapeString(older[1]), false); p.code != 200 || !strings.Contains(p.body, "<html") || strings.Count(p.body, `class="vk-obs"`) != 11 {
		t.Fatalf("older page: %d", p.code)
	}
	if p := e.get(path+"?before=nonsense", false); p.code != 404 {
		t.Fatalf("bad cursor: %d", p.code)
	}
	// kinds and periods
	changes := e.get(path+"?kind=change", true)
	if changes.code != 200 || strings.Contains(changes.body, "<html") || strings.Count(changes.body, `class="vk-obs"`) != 1 || !strings.Contains(changes.body, "new → up") {
		t.Fatalf("changes: %d %s", changes.code, changes.body)
	}
	changes.has(t, `aria-pressed="true" name="kind" value="">`, `>changes<`) // the pressed chip clears on click
	if p := e.get(path+"?kind=fail", false); !strings.Contains(p.body, "No failures in the last 7 days") {
		t.Fatalf("empty failures: %s", p.body)
	}
	if p := e.get(path+"?period=24h", false); !strings.Contains(p.body, `name="period" value="24h" checked`) || strings.Count(p.body, `class="vk-obs"`) != 50 {
		t.Fatalf("period 24h: %d rows", strings.Count(p.body, `class="vk-obs"`))
	}
	// an exact window: the segmented control is unchecked and the window stays in the form
	dayLink := regexp.MustCompile(`vk-listhead">Today <span><a class="vk-link" href="([^"]+)"`).FindStringSubmatch(full.body)
	if dayLink == nil {
		t.Fatal("no day link")
	}
	day := e.get(html.UnescapeString(dayLink[1]), false)
	if day.code != 200 || strings.Contains(day.body, `value="7d" checked`) || !strings.Contains(day.body, `name="since" value="2026-09-27T00:00:00&#43;02:00"`) || !strings.Contains(day.body, `name="until" value="2026-09-27T23:59:59&#43;02:00"`) || strings.Count(day.body, `class="vk-obs"`) != 50 {
		t.Fatalf("day window: %d rows=%d checked=%v", day.code, strings.Count(day.body, `class="vk-obs"`), strings.Contains(day.body, `value="7d" checked`))
	}
	if p := e.get(path+"?since=2026-09-28T00:00:00Z", false); !strings.Contains(p.body, "Nothing in this window") {
		t.Fatal("a window after every row must be empty")
	}
	// the head polls with an ETag and notices newer rows
	head := e.get(path+"?partial=head", false)
	if head.code != 200 || !strings.HasPrefix(head.body, `<div id="history-head"`) || head.hdr.Get("ETag") == "" || strings.Contains(head.body, "New rows arrived") {
		t.Fatalf("head partial: %d %q", head.code, head.body[:60])
	}
	req := httptest.NewRequest("GET", path+"?partial=head", nil)
	req.AddCookie(e.cookie)
	req.Header.Set("If-None-Match", head.hdr.Get("ETag"))
	rec := httptest.NewRecorder()
	e.srv.ServeHTTP(rec, req)
	if rec.Code != 304 {
		t.Fatalf("head etag: %d", rec.Code)
	}
	newest := regexp.MustCompile(`newest=([0-9]+\.[0-9A-Z]+)`).FindStringSubmatch(full.body)
	if newest == nil {
		t.Fatal("no newest key on the poll URL")
	}
	if p := e.get(path+"?partial=head&newest="+newest[1], true); strings.Contains(p.body, "New rows arrived") {
		t.Fatal("nothing new yet")
	}
	e.now = e.now.Add(time.Minute)
	_, _, _ = e.svc.RecordPing(ctx, tgt, service.PingObservation{Signal: domain.SignalOK})
	stale := e.get(path+"?partial=head&newest="+newest[1], true)
	stale.has(t, "New rows arrived", `href="/o/homelab/p/prod/m/nightly/history?period=7d">Reload</a>`)
	// actions come back to the page; the drawer links here; tenancy holds
	paused := e.post(projPath+"/m/nightly/pause", url.Values{"next": {path + "?period=24h"}}, false)
	if paused.code != 303 || paused.hdr.Get("Location") != path+"?period=24h" {
		t.Fatalf("pause from history: %d %s", paused.code, paused.hdr.Get("Location"))
	}
	if p := e.get(path, false); !strings.Contains(p.body, ">Resume<") {
		t.Fatal("paused page must offer Resume")
	}
	if elsewhere := e.post(projPath+"/m/nightly/resume", url.Values{"next": {"https://evil.example/"}}, false); elsewhere.hdr.Get("Location") != projPath+"/m/nightly" {
		t.Fatalf("a foreign next must be ignored: %s", elsewhere.hdr.Get("Location"))
	}
	e.get(projPath+"/m/nightly", true).has(t, `href="/o/homelab/p/prod/m/nightly/history">History</a>`)
	if p := e.get("/o/acme/p/prod/m/nightly/history", false); p.code != 404 {
		t.Fatalf("cross-tenant history: %d", p.code)
	}
}
