package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/w4jnl/vink/internal/auth"
	"github.com/w4jnl/vink/internal/checks"
	"github.com/w4jnl/vink/internal/config"
	"github.com/w4jnl/vink/internal/db"
	"github.com/w4jnl/vink/internal/db/dbtest"
	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/http/middleware"
	"github.com/w4jnl/vink/internal/notify"
	"github.com/w4jnl/vink/internal/outbound"
	"github.com/w4jnl/vink/internal/service"
)

type env struct {
	t       *testing.T
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
	e := &env{t: t, svc: svc, now: time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)}
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
	authn, err := auth.New(svc, config.Default().Auth, "http://localhost:8080", quiet)
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
	p.has(t, `<details class="vk-popover"><summary class="vk-top__crumb" title="Switch project">homelab / <b>prod</b>`, `class="vk-menu"`, `<div class="vk-menu__label"><span>homelab</span><span class="vk-tag">admin</span></div>`,
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
	_, _, _ = e.svc.RecordPing(ctx, tgt, service.PingObservation{Signal: domain.SignalOK, RunID: "r1", Body: []byte("x")})
	full := e.get(projPath+"/m/nightly", false)
	if full.code != 200 {
		t.Fatalf("drawer page: %d", full.code)
	}
	full.has(t, `<title>Nightly · vink</title>`, `class="vk-page" id="page"`, `<aside class="vk-drawer" id="drawer">`, `aria-current="true"`, `vk-drawer__title">Nightly`, e.project.PingKey+"/<b>nightly</b>", "every 1h · grace 5m · due in", "Last 24 hours", `vk-obs`, "4m0s", "new → up · first ok",
		`data-drawer-close`, `>Edit<`, `>Pause<`, "As YAML", "slug", `vk-codebox`)
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
		`vk-details__title">Advanced</span>`, "max runtime none · down after 1 · methods any · body 64 KB", `for="body_limit"`, "Ping URL", "As YAML", "kind: heartbeat", `Create monitor`, `hx-post="/o/homelab/p/prod/m/preview"`)
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
	pv.has(t, `<hx-partial hx-target="#schedule-msg"`, "Next runs:", `hx-target="#grace-msg"`, "Late at 03:00, down at 03:05.", `hx-target="#monitor-form .vk-codebox"`, "nightly-backup", `cron: &quot;0 3 * * *&quot;`)
	ok := e.post(projPath+"/m/new", url.Values{"name": {"Nightly backup"}, "schedule_type": {"cron"}, "schedule": {"0 3 * * *"}, "grace": {"30m"}, "tags": {"Backup, prod"}, "max_runtime": {"2h"}}, true)
	if ok.code != 204 || ok.hdr.Get("HX-Redirect") != projPath+"/m/nightly-backup" {
		t.Fatalf("create: %d %v %s", ok.code, ok.hdr, ok.body)
	}
	m, err := e.svc.MonitorBySlug(context.Background(), e.scope, "nightly-backup")
	if err != nil || m.Heartbeat.Schedule.Cron != "0 3 * * *" || m.Heartbeat.Grace.String() != "30m" || len(m.Tags) != 2 || m.Heartbeat.MaxRuntime.String() != "2h" {
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
	p.has(t, "<h1>Homelab status</h1>", "vk-banner--down", "1 service down", "since 14:00", "<h2>prod</h2>", "<h2>backup</h2>", "Api", "Nightly", "vk-uptime", "up over 90 days", "Open incidents", "powered by", `http-equiv="refresh"`, "updated 14:00:00 CEST")
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
