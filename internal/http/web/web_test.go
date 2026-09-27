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
	"github.com/w4jnl/vink/internal/config"
	"github.com/w4jnl/vink/internal/db/dbtest"
	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/http/middleware"
	"github.com/w4jnl/vink/internal/notify"
	"github.com/w4jnl/vink/internal/service"
)

type env struct {
	t       *testing.T
	svc     *service.Service
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
	e.web, err = New(svc, authn, quiet)
	if err != nil {
		t.Fatal(err)
	}
	e.web.SetClock(func() time.Time { return e.now })
	mux := http.NewServeMux()
	e.web.Mount(mux)
	e.srv = middleware.Chain(mux, middleware.RequestID)

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
	p.has(t, `name="user"`, `type="password"`, "Sign in", `<span>vink</span>`)
	if p.hdr.Get("Content-Security-Policy") == "" || p.hdr.Get("X-Frame-Options") != "DENY" {
		t.Errorf("security headers: %v", p.hdr)
	}
	bad := e.do("POST", "/login", url.Values{"user": {"j"}, "password": {"nope"}, "next": {projPath}}, false, false)
	if bad.code != 401 || !strings.Contains(bad.body, "Wrong user or password") {
		t.Fatalf("bad login: %d", bad.code)
	}
	good := e.do("POST", "/login", url.Values{"user": {"j"}, "password": {"correct horse"}, "next": {"//evil.example"}}, false, false)
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

func TestMonitorsPageEmptyAndRows(t *testing.T) {
	e := newEnv(t)
	p := e.get(projPath, false)
	if p.code != 200 {
		t.Fatalf("monitors: %d %s", p.code, p.body)
	}
	p.has(t, `<h1>Monitors</h1>`, `class="vk-empty"`, e.project.PingKey, `homelab / <b>prod</b>`, `Create monitor`, `data-down-count="0"`, `/static/`)
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
	p.has(t, `class="vk-row"`, `vk-glyph--down`, "Nightly", "Hourly", `hx-get="/o/homelab/p/prod/m/nightly"`, `vk-chip`, `data-down-count="2"`, `favicon-down.svg`)
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
	none.has(t, "No monitors match this filter.")
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
	full.has(t, `<title>Nightly · vink</title>`, `aria-current="true"`, `vk-drawer__title">Nightly`, e.project.PingKey+"/<b>nightly</b>", "every 1h · grace 5m · due in", "Last 24 hours", `vk-obs`, "4m0s", "new → up · first ok", `data-drawer-close`, `Delete monitor`, `data-confirm="Really delete?"`)
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
	form.has(t, `id="monitor-form"`, `for="name"`, `for="period"`, `for="cron"`, `<summary>Advanced</summary>`, `Create monitor`)
	// validation: no schedule, bad grace
	bad := e.post(projPath+"/m/new", url.Values{"name": {"Nightly backup"}, "grace": {"10s"}}, true)
	if bad.code != 422 {
		t.Fatalf("validation: %d", bad.code)
	}
	bad.has(t, `vk-field--error`, `id="grace-msg"`, "Must be at least 1m.", `id="period-msg"`, "Set period or cron.")
	if strings.Contains(bad.body, "<html") {
		t.Error("htmx validation response must be the partial")
	}
	unparsable := e.post(projPath+"/m/new", url.Values{"name": {"X"}, "period": {"soon"}, "failure_threshold": {"many"}}, false)
	if unparsable.code != 422 || !strings.Contains(unparsable.body, "Use a duration such as 1h or 1d.") || !strings.Contains(unparsable.body, "<details open>") {
		t.Fatalf("parse errors: %d", unparsable.code)
	}
	ok := e.post(projPath+"/m/new", url.Values{"name": {"Nightly backup"}, "cron": {"0 3 * * *"}, "grace": {"30m"}, "tags": {"Backup, prod"}, "max_runtime": {"2h"}}, true)
	if ok.code != 204 || ok.hdr.Get("HX-Redirect") != projPath+"/m/nightly-backup" {
		t.Fatalf("create: %d %v %s", ok.code, ok.hdr, ok.body)
	}
	m, err := e.svc.MonitorBySlug(context.Background(), e.scope, "nightly-backup")
	if err != nil || m.Heartbeat.Schedule.Cron != "0 3 * * *" || m.Heartbeat.Grace.String() != "30m" || len(m.Tags) != 2 || m.Heartbeat.MaxRuntime.String() != "2h" {
		t.Fatalf("created monitor: %+v %v", m, err)
	}
	dup := e.post(projPath+"/m/new", url.Values{"name": {"Nightly backup"}, "period": {"1h"}}, false)
	if dup.code != 422 || !strings.Contains(dup.body, "A monitor with this slug exists.") {
		t.Fatalf("duplicate: %d", dup.code)
	}
	edit := e.get(projPath+"/m/nightly-backup/edit", false)
	edit.has(t, `value="0 3 * * *"`, `value="30m"`, `readonly`, `<details open>`, `>Save<`)
	saved := e.post(projPath+"/m/nightly-backup/edit", url.Values{"name": {"Nightly"}, "cron": {"0 4 * * *"}, "grace": {"1h"}}, false)
	if saved.code != 303 {
		t.Fatalf("edit: %d %s", saved.code, saved.body)
	}
	m, _ = e.svc.MonitorBySlug(context.Background(), e.scope, "nightly-backup")
	if m.Name != "Nightly" || m.Heartbeat.Schedule.Cron != "0 4 * * *" {
		t.Fatalf("edited: %+v", m)
	}
}

func TestIncidentsPage(t *testing.T) {
	e := newEnv(t)
	e.monitor("job")
	ctx := context.Background()
	tgt, _ := e.svc.ResolvePing(ctx, e.project.PingKey, "job", "", false)
	_, _, _ = e.svc.RecordPing(ctx, tgt, service.PingObservation{Signal: domain.SignalFail})
	p := e.get(projPath+"/incidents", false)
	if p.code != 200 {
		t.Fatalf("incidents: %d", p.code)
	}
	p.has(t, `1 open`, `>Acknowledge<`, "Job", "down for")
	incs, _ := e.svc.ListIncidents(ctx, e.scope, true, 0)
	acked := e.post(projPath+"/incidents/"+incs[0].ID+"/ack", nil, true)
	if acked.code != 200 || !strings.Contains(acked.body, "acked by user:j") || strings.Contains(acked.body, ">Acknowledge<") {
		t.Fatalf("ack: %d %s", acked.code, acked.body)
	}
	e.now = e.now.Add(time.Hour)
	_, _, _ = e.svc.RecordPing(ctx, tgt, service.PingObservation{Signal: domain.SignalOK})
	p = e.get(projPath+"/incidents?partial=list", false)
	p.has(t, "resolved just now")
	p = e.get(projPath+"/incidents", false)
	p.has(t, "0 open")
}

func TestSettingsTabs(t *testing.T) {
	e := newEnv(t)
	if p := e.get(projPath+"/settings", false); p.code != 303 || p.hdr.Get("Location") != projPath+"/settings/channels" {
		t.Fatalf("settings redirect: %d", p.code)
	}
	ch := e.get(projPath+"/settings/channels", false)
	ch.has(t, `aria-pressed="true" href="/o/homelab/p/prod/settings/channels"`, "No alert channels yet", `for="config"`, `<option value="webhook"`)
	bad := e.post(projPath+"/settings/channels", url.Values{"name": {"hook"}, "kind": {"webhook"}, "config": {`{"url":"ftp://x"}`}}, false)
	if bad.code != 422 || !strings.Contains(bad.body, "vk-field--error") {
		t.Fatalf("bad channel: %d", bad.code)
	}
	ok := e.post(projPath+"/settings/channels", url.Values{"name": {"hook"}, "kind": {"webhook"}, "config": {`{"url":"https://hooks.example.com/x"}`}}, false)
	if ok.code != 303 {
		t.Fatalf("create channel: %d %s", ok.code, ok.body)
	}
	ch = e.get(projPath+"/settings/channels?flash=Channel+added.", false)
	ch.has(t, "Channel added.", "hooks.example.com/x", `>Test<`, `data-confirm="Really delete?"`)
	rt := e.get(projPath+"/settings/routes", false)
	rt.has(t, "on down, up · all monitors", `<option value="`, `name="on" value="late"`)
	badRoute := e.post(projPath+"/settings/routes", url.Values{"channel_id": {"nope"}, "repeat_every": {"1m"}}, false)
	if badRoute.code != 422 {
		t.Fatalf("bad route: %d", badRoute.code)
	}
	channels, _ := e.svc.ListChannels(context.Background(), e.scope)
	okRoute := e.post(projPath+"/settings/routes", url.Values{"channel_id": {channels[0].ID}, "match_tags": {"prod"}, "on": {"down", "late"}, "repeat_every": {"4h"}}, false)
	if okRoute.code != 303 {
		t.Fatalf("create route: %d %s", okRoute.code, okRoute.body)
	}
	rt = e.get(projPath+"/settings/routes", false)
	rt.has(t, "on down, late · repeat every 4h", `<span class="vk-tag">prod</span>`)
	keys := e.get(projPath+"/settings/keys", false)
	keys.has(t, e.project.PingKey, "Rotate ping key", "No API keys", `<option value="rw">`)
	created := e.post(projPath+"/settings/keys", url.Values{"name": {"laptop"}, "access": {"ro"}}, false)
	if created.code != 303 || !strings.Contains(created.hdr.Get("Location"), "?key=vk_") {
		t.Fatalf("create key: %d %s", created.code, created.hdr.Get("Location"))
	}
	shown := e.get(created.hdr.Get("Location"), false)
	shown.has(t, "Shown once", "laptop", "never used", ">Revoke<")
	if p := e.get(projPath+"/settings/nope", false); p.code != 404 {
		t.Fatalf("unknown tab: %d", p.code)
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
