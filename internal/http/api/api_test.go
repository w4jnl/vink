package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
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
	"github.com/w4jnl/vink/internal/outbound"
	"github.com/w4jnl/vink/internal/service"
)

type env struct {
	t       *testing.T
	svc     *service.Service
	authn   *auth.Authenticator
	srv     http.Handler
	org     *domain.Org
	project *domain.Project
	other   *domain.Project // same org, second project
	foreign *domain.Project // another org
	rw, ro  string
	cookie  *http.Cookie
	csrf    string
	now     time.Time
}

func newEnv(t *testing.T) *env {
	t.Helper()
	d := dbtest.Open(t)
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := service.New(d, nil, quiet, service.DefaultConfig())
	e := &env{t: t, svc: svc, now: time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)}
	svc.SetClock(func() time.Time { return e.now })
	ctx := context.Background()
	admin := domain.Scope{InstanceAdmin: true, Role: domain.RoleOwner}
	var err error
	e.org, err = svc.CreateOrg(ctx, admin, "homelab", "Homelab")
	if err != nil {
		t.Fatal(err)
	}
	e.project, _ = svc.CreateProject(ctx, admin, e.org.ID, "prod", "Production", "Europe/Amsterdam")
	e.other, _ = svc.CreateProject(ctx, admin, e.org.ID, "lab", "Lab", "UTC")
	forg, _ := svc.CreateOrg(ctx, admin, "acme", "Acme")
	e.foreign, _ = svc.CreateProject(ctx, admin, forg.ID, "prod", "Acme prod", "UTC")
	psc := domain.Scope{OrgID: e.org.ID, ProjectID: e.project.ID, Role: domain.RoleAdmin, Actor: "test"}
	_, e.rw, _ = svc.CreateAPIKey(ctx, psc, "rw", domain.AccessRW)
	_, e.ro, _ = svc.CreateAPIKey(ctx, psc, "ro", domain.AccessRO)
	user, _ := svc.CreateLocalUser(ctx, admin, "j", "j@example.com", "J", "correct horse", false)
	_ = svc.SetMembership(ctx, admin, user.ID, e.org.ID, domain.RoleMember)

	e.authn, err = auth.New(svc, config.Default().Auth, "http://localhost:8080", quiet)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(svc, e.authn, quiet).Mount(mux)
	e.srv = middleware.Chain(mux, middleware.RequestID)

	rec := httptest.NewRecorder()
	p, err := e.authn.Login(rec, httptest.NewRequest("POST", "/login", nil), "j", "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	e.cookie = rec.Result().Cookies()[0]
	e.csrf = p.CSRF()
	return e
}

type resp struct {
	code int
	body []byte
	hdr  http.Header
}

func (r resp) json(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal(r.body, v); err != nil {
		t.Fatalf("not JSON (%d): %s", r.code, r.body)
	}
}

func (e *env) key(token, method, path string, body any) resp {
	return e.do(method, Prefix+path, body, func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) })
}

func (e *env) session(method, path string, body any, withCSRF bool) resp {
	return e.do(method, Prefix+"/orgs/homelab/projects/prod"+path, body, func(r *http.Request) {
		r.AddCookie(e.cookie)
		if withCSRF {
			r.Header.Set(auth.CSRFHeader, e.csrf)
		}
	})
}

func (e *env) do(method, path string, body any, mutate func(*http.Request)) resp {
	e.t.Helper()
	var rdr io.Reader
	if body != nil {
		switch b := body.(type) {
		case string:
			rdr = strings.NewReader(b)
		default:
			raw, _ := json.Marshal(b)
			rdr = bytes.NewReader(raw)
		}
	}
	req := httptest.NewRequest(method, path, rdr)
	req.RemoteAddr = "203.0.113.9:1"
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if mutate != nil {
		mutate(req)
	}
	rec := httptest.NewRecorder()
	e.srv.ServeHTTP(rec, req)
	return resp{code: rec.Code, body: rec.Body.Bytes(), hdr: rec.Header()}
}

func (e *env) createMonitor(slug string) MonitorOut {
	e.t.Helper()
	r := e.key(e.rw, "POST", "/monitors", map[string]any{"slug": slug, "name": "Job " + slug, "schedule": map[string]string{"period": "1h"}, "grace": "5m", "tags": []string{"prod"}})
	if r.code != 201 {
		e.t.Fatalf("create monitor: %d %s", r.code, r.body)
	}
	var out MonitorOut
	r.json(e.t, &out)
	return out
}

func TestAuthAndProblems(t *testing.T) {
	e := newEnv(t)
	// no credentials
	r := e.do("GET", Prefix+"/monitors", nil, nil)
	if r.code != 401 || r.hdr.Get("Content-Type") != "application/problem+json" || r.hdr.Get("WWW-Authenticate") == "" {
		t.Fatalf("anonymous: %d %s %v", r.code, r.body, r.hdr)
	}
	var p Problem
	r.json(t, &p)
	if p.Status != 401 || !strings.HasSuffix(p.Type, "#unauthorized") || p.Title == "" {
		t.Errorf("problem: %+v", p)
	}
	// bad key
	if r := e.key("vk_nope", "GET", "/monitors", nil); r.code != 401 {
		t.Errorf("bad key: %d", r.code)
	}
	// ro key cannot write
	r = e.key(e.ro, "POST", "/monitors", map[string]any{"slug": "x"})
	if r.code != 403 {
		t.Errorf("ro write: %d %s", r.code, r.body)
	}
	// session on the bare path is refused
	if r := e.do("GET", Prefix+"/monitors", nil, func(r *http.Request) { r.AddCookie(e.cookie) }); r.code != 401 {
		t.Errorf("session on bare path: %d", r.code)
	}
	// session write without CSRF
	if r := e.session("POST", "/monitors", map[string]any{"slug": "x"}, false); r.code != 403 {
		t.Errorf("session without csrf: %d %s", r.code, r.body)
	}
	// unknown route is a JSON 404
	r = e.key(e.rw, "GET", "/nope", nil)
	if r.code != 404 || r.hdr.Get("Content-Type") != "application/problem+json" {
		t.Errorf("unknown route: %d %s", r.code, r.hdr.Get("Content-Type"))
	}
	// malformed JSON and unknown fields
	if r := e.key(e.rw, "POST", "/monitors", "{not json"); r.code != 400 {
		t.Errorf("malformed: %d", r.code)
	}
	if r := e.key(e.rw, "POST", "/monitors", map[string]any{"slug": "x", "schedule": map[string]string{"period": "1h"}, "colour": "red"}); r.code != 400 {
		t.Errorf("unknown field: %d %s", r.code, r.body)
	}
	// validation
	r = e.key(e.rw, "POST", "/monitors", map[string]any{"slug": "Bad Slug", "schedule": map[string]string{"period": "10s"}, "grace": "1s"})
	if r.code != 422 {
		t.Fatalf("validation: %d %s", r.code, r.body)
	}
	r.json(t, &p)
	fields := map[string]bool{}
	for _, fe := range p.Errors {
		fields[fe.Field] = true
	}
	if !fields["slug"] || !fields["schedule"] || !fields["grace"] {
		t.Errorf("validation fields: %+v", p.Errors)
	}
	// openapi is served
	if r := e.do("GET", Prefix+"/openapi.yaml", nil, nil); r.code != 200 || !bytes.Contains(r.body, []byte("openapi: 3.1.0")) {
		t.Errorf("openapi: %d", r.code)
	}
	if r := e.key(e.rw, "GET", "/monitors", nil); r.hdr.Get("X-Content-Type-Options") != "nosniff" || r.hdr.Get("Cache-Control") != "no-store" {
		t.Errorf("security headers: %v", r.hdr)
	}
}

func TestMe(t *testing.T) {
	e := newEnv(t)
	var me meOut
	r := e.key(e.rw, "GET", "/me", nil)
	r.json(t, &me)
	if me.Kind != "key" || me.Key.Access != domain.AccessRW || me.Project.Slug != "prod" || me.Org.Slug != "homelab" || me.Role != domain.RoleAdmin || me.Project.PingKey == "" {
		t.Errorf("key me: %+v", me)
	}
	r = e.key(e.ro, "GET", "/me", nil)
	me = meOut{}
	r.json(t, &me)
	if me.Project.PingKey != "" || me.Role != domain.RoleViewer {
		t.Errorf("ro me must not see the ping key: %+v", me)
	}
	// session me on the bare path: identity without project
	r = e.do("GET", Prefix+"/me", nil, func(r *http.Request) { r.AddCookie(e.cookie) })
	if r.code != 200 {
		t.Fatalf("session me: %d %s", r.code, r.body)
	}
	me = meOut{}
	r.json(t, &me)
	if me.Kind != "user" || me.User.Subject != "j" || len(me.Memberships) != 1 || me.Project != nil {
		t.Errorf("session me: %+v", me)
	}
	// session me on the project path
	r = e.session("GET", "/me", nil, false)
	me = meOut{}
	r.json(t, &me)
	if me.Project == nil || me.Project.Slug != "prod" || me.Role != domain.RoleMember {
		t.Errorf("session project me: %+v", me)
	}
}

func TestMonitorsCRUD(t *testing.T) {
	e := newEnv(t)
	m := e.createMonitor("nightly")
	if m.State != domain.StateNew || m.Grace.String() != "5m" || m.PingURL == "" || m.ExpectedAt == nil || m.Schedule.Period.String() != "1h" {
		t.Fatalf("created: %+v", m)
	}
	// duplicate
	if r := e.key(e.rw, "POST", "/monitors", map[string]any{"slug": "nightly", "schedule": map[string]string{"period": "1h"}}); r.code != 409 {
		t.Errorf("duplicate: %d", r.code)
	}
	// name-only create derives the slug
	r := e.key(e.rw, "POST", "/monitors", map[string]any{"name": "Weekly Restic Check", "schedule": map[string]string{"cron": "0 4 * * sun"}, "timezone": "Europe/Amsterdam"})
	if r.code != 201 || r.hdr.Get("Location") != Prefix+"/monitors/weekly-restic-check" {
		t.Fatalf("name-only create: %d %s %s", r.code, r.body, r.hdr.Get("Location"))
	}
	// get, ro key sees no ping url
	r = e.key(e.ro, "GET", "/monitors/nightly", nil)
	var got MonitorOut
	r.json(t, &got)
	if got.PingURL != "" || got.Slug != "nightly" {
		t.Errorf("ro get: %+v", got)
	}
	// session get sees it (member)
	r = e.session("GET", "/monitors/nightly", nil, false)
	r.json(t, &got)
	if r.code != 200 || got.PingURL == "" {
		t.Errorf("session get: %d %+v", r.code, got)
	}
	// list with filters and pagination
	r = e.key(e.rw, "GET", "/monitors?limit=1", nil)
	var pg page[MonitorOut]
	r.json(t, &pg)
	if len(pg.Items) != 1 || pg.NextCursor == nil || pg.Items[0].Slug != "nightly" {
		t.Fatalf("page 1: %+v", pg)
	}
	r = e.key(e.rw, "GET", "/monitors?limit=1&cursor="+*pg.NextCursor, nil)
	r.json(t, &pg)
	if len(pg.Items) != 1 || pg.Items[0].Slug != "weekly-restic-check" || pg.NextCursor != nil {
		t.Fatalf("page 2: %+v", pg)
	}
	r = e.key(e.rw, "GET", "/monitors?tag=prod", nil)
	r.json(t, &pg)
	if len(pg.Items) != 1 {
		t.Errorf("tag filter: %d", len(pg.Items))
	}
	if r := e.key(e.rw, "GET", "/monitors?state=bogus", nil); r.code != 400 {
		t.Errorf("bad state filter: %d", r.code)
	}
	if r := e.key(e.rw, "GET", "/monitors?cursor=!!!", nil); r.code != 400 {
		t.Errorf("bad cursor: %d", r.code)
	}
	// put replaces, patch merges
	r = e.key(e.rw, "PUT", "/monitors/nightly", map[string]any{"slug": "nightly", "name": "Nightly backup", "schedule": map[string]string{"cron": "0 3 * * *"}, "grace": "30m", "tags": []string{"backup"}})
	r.json(t, &got)
	if r.code != 200 || got.Name != "Nightly backup" || got.Schedule.Cron != "0 3 * * *" || got.Grace.String() != "30m" {
		t.Fatalf("put: %d %+v", r.code, got)
	}
	r = e.key(e.rw, "PATCH", "/monitors/nightly", map[string]any{"grace": "45m"})
	r.json(t, &got)
	if r.code != 200 || got.Grace.String() != "45m" || got.Schedule.Cron != "0 3 * * *" || got.Name != "Nightly backup" {
		t.Fatalf("patch: %d %+v", r.code, got)
	}
	if r := e.key(e.rw, "PUT", "/monitors/nightly", map[string]any{"slug": "renamed", "schedule": map[string]string{"period": "1h"}}); r.code != 422 {
		t.Errorf("slug change: %d", r.code)
	}
	// pause / resume via session with CSRF
	r = e.session("POST", "/monitors/nightly/pause", nil, true)
	r.json(t, &got)
	if r.code != 200 || got.State != domain.StatePaused || !got.Paused {
		t.Fatalf("pause: %d %+v", r.code, got)
	}
	r = e.session("POST", "/monitors/nightly/resume", nil, true)
	r.json(t, &got)
	if got.State != domain.StateNew || got.Paused {
		t.Fatalf("resume: %+v", got)
	}
	// delete
	if r := e.key(e.rw, "DELETE", "/monitors/nightly", nil); r.code != 204 {
		t.Errorf("delete: %d", r.code)
	}
	if r := e.key(e.rw, "GET", "/monitors/nightly", nil); r.code != 404 {
		t.Errorf("after delete: %d", r.code)
	}
}

func TestCrossTenantIs404(t *testing.T) {
	e := newEnv(t)
	e.createMonitor("nightly")
	// a key of another project of the same org
	ctx := context.Background()
	osc := domain.Scope{OrgID: e.org.ID, ProjectID: e.other.ID, Role: domain.RoleAdmin}
	_, otherKey, _ := e.svc.CreateAPIKey(ctx, osc, "other", domain.AccessRW)
	if r := e.key(otherKey, "GET", "/monitors/nightly", nil); r.code != 404 {
		t.Errorf("other project key: %d", r.code)
	}
	if r := e.key(otherKey, "DELETE", "/monitors/nightly", nil); r.code != 404 {
		t.Errorf("other project delete: %d", r.code)
	}
	// a session user with no role in the foreign org
	r := e.do("GET", Prefix+"/orgs/acme/projects/prod/monitors", nil, func(r *http.Request) { r.AddCookie(e.cookie) })
	if r.code != 404 {
		t.Errorf("foreign org via session: %d %s", r.code, r.body)
	}
	r = e.do("GET", Prefix+"/orgs/nope/projects/prod/monitors", nil, func(r *http.Request) { r.AddCookie(e.cookie) })
	if r.code != 404 {
		t.Errorf("unknown org: %d", r.code)
	}
}

func TestObservationsEventsIncidentsStatus(t *testing.T) {
	e := newEnv(t)
	m := e.createMonitor("job")
	ctx := context.Background()
	tgt, _ := e.svc.ResolvePing(ctx, e.project.PingKey, "job", "", false)
	if _, _, err := e.svc.RecordPing(ctx, tgt, service.PingObservation{Signal: domain.SignalOK, Body: []byte("done\n"), ContentType: "text/plain", Msg: "hi"}); err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(66 * time.Minute)
	if err := e.svc.Tick(ctx, m.ID, e.now); err != nil {
		t.Fatal(err)
	}
	r := e.key(e.rw, "GET", "/monitors/job/observations", nil)
	var obs page[ObservationOut]
	r.json(t, &obs)
	if len(obs.Items) != 1 || !obs.Items[0].HasBody || obs.Items[0].Detail["msg"] != "hi" {
		t.Fatalf("observations: %+v", obs)
	}
	r = e.key(e.rw, "GET", "/monitors/job/observations/"+obs.Items[0].ID+"?body=1", nil)
	if r.code != 200 || string(r.body) != "done\n" || !strings.HasPrefix(r.hdr.Get("Content-Type"), "text/plain") || r.hdr.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("body: %d %q %v", r.code, r.body, r.hdr)
	}
	if r := e.key(e.rw, "GET", "/monitors/job/observations?since=not-a-time", nil); r.code != 400 {
		t.Errorf("bad since: %d", r.code)
	}
	r = e.key(e.rw, "GET", "/monitors/job/events", nil)
	var ev page[EventOut]
	r.json(t, &ev)
	if len(ev.Items) != 2 || ev.Items[0].To != domain.StateDown || ev.Items[1].To != domain.StateUp || ev.NextCursor != nil {
		t.Fatalf("events: %+v", ev)
	}
	// events page like observations: a cursor, a window, both tenancy-scoped
	r = e.key(e.rw, "GET", "/monitors/job/events?limit=1", nil)
	var first page[EventOut]
	r.json(t, &first)
	if len(first.Items) != 1 || first.Items[0].To != domain.StateDown || first.NextCursor == nil {
		t.Fatalf("events page 1: %+v", first)
	}
	r = e.key(e.rw, "GET", "/monitors/job/events?limit=1&cursor="+*first.NextCursor, nil)
	var second page[EventOut]
	r.json(t, &second)
	if len(second.Items) != 1 || second.Items[0].To != domain.StateUp {
		t.Fatalf("events page 2: %+v", second)
	}
	r = e.key(e.rw, "GET", "/monitors/job/events?until="+strconv.FormatInt(e.now.Add(-time.Hour).Unix(), 10), nil)
	r.json(t, &ev)
	if len(ev.Items) != 1 || ev.Items[0].To != domain.StateUp {
		t.Fatalf("events until: %+v", ev.Items)
	}
	if r := e.key(e.rw, "GET", "/monitors/job/events?since=soon", nil); r.code != 400 {
		t.Errorf("bad since on events: %d", r.code)
	}
	// kind narrows observations; a fail ping shows under fail and not under ok
	if _, _, err := e.svc.RecordPing(ctx, tgt, service.PingObservation{Signal: domain.SignalFail, Msg: "disk full"}); err != nil {
		t.Fatal(err)
	}
	r = e.key(e.rw, "GET", "/monitors/job/observations?kind=fail", nil)
	r.json(t, &obs)
	if len(obs.Items) != 1 || obs.Items[0].Signal != "fail" {
		t.Fatalf("kind=fail: %+v", obs.Items)
	}
	r = e.key(e.rw, "GET", "/monitors/job/observations?kind=ok", nil)
	r.json(t, &obs)
	if len(obs.Items) != 1 || obs.Items[0].Signal != "ok" {
		t.Fatalf("kind=ok: %+v", obs.Items)
	}
	for _, bad := range []string{"x", "change"} {
		if r := e.key(e.rw, "GET", "/monitors/job/observations?kind="+bad, nil); r.code != 400 {
			t.Errorf("kind=%s: %d", bad, r.code)
		}
	}
	r = e.key(e.rw, "GET", "/incidents?open=1", nil)
	var inc page[IncidentOut]
	r.json(t, &inc)
	if len(inc.Items) != 1 || !inc.Items[0].Open || inc.Items[0].Monitor != "job" {
		t.Fatalf("incidents: %+v", inc.Items)
	}
	if r := e.key(e.ro, "POST", "/incidents/"+inc.Items[0].ID+"/ack", nil); r.code != 403 {
		t.Errorf("ro ack: %d", r.code)
	}
	r = e.session("POST", "/incidents/"+inc.Items[0].ID+"/ack", nil, true)
	var one IncidentOut
	r.json(t, &one)
	if r.code != 200 || one.AckedAt == nil || one.AckedBy != "user:j" {
		t.Fatalf("ack: %d %+v", r.code, one)
	}
	r = e.key(e.rw, "GET", "/status", nil)
	var st StatusOut
	r.json(t, &st)
	if st.Counts[domain.StateDown] != 1 || st.Total != 1 || len(st.OpenIncidents) != 1 || len(st.Monitors) != 1 {
		t.Fatalf("status: %+v", st)
	}
}

func TestPullMonitorAPI(t *testing.T) {
	e := newEnv(t)
	reg, err := checks.NewRegistry(checks.Options{Outbound: outbound.Options{AllowPrivateTargets: true}})
	if err != nil {
		t.Fatal(err)
	}
	e.svc.SetChecker(reg)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"status":"ok"}`)) }))
	defer target.Close()
	created := e.key(e.rw, "POST", "/monitors", map[string]any{
		"slug": "api", "name": "API", "kind": "http", "interval": "30s", "timeout": "5s",
		"http": map[string]any{"url": target.URL, "expect_status": []any{200, "300-399"}, "expect_body": map[string]any{"jsonpath": map[string]any{"path": "$.status", "equals": "ok"}}},
	})
	if created.code != 201 {
		t.Fatalf("create: %d %s", created.code, created.body)
	}
	var m MonitorOut
	created.json(t, &m)
	if m.Kind != domain.KindHTTP || m.Target != target.URL || m.Interval.String() != "30s" || m.Timeout.String() != "5s" || m.FailureThreshold != 3 || m.Confirm == nil || m.Confirm.Retries != 2 || m.HTTP == nil || len(m.HTTP.ExpectStatus) != 2 || m.NextDueAt == nil || m.PingURL != "" {
		t.Fatalf("created monitor: %s", created.body)
	}
	bad := e.key(e.rw, "POST", "/monitors", map[string]any{"slug": "bad", "kind": "tcp", "tcp": map[string]any{"host": "db", "port": 99999}})
	if bad.code != 422 || !strings.Contains(string(bad.body), "tcp.port") {
		t.Fatalf("validation: %d %s", bad.code, bad.body)
	}
	checked := e.key(e.rw, "POST", "/monitors/api/check", nil)
	if checked.code != 200 {
		t.Fatalf("check: %d %s", checked.code, checked.body)
	}
	checked.json(t, &m)
	if m.State != domain.StateUp || m.LastOkAt == nil {
		t.Fatalf("after check: %s", checked.body)
	}
	if r := e.key(e.ro, "POST", "/monitors/api/check", nil); r.code != 403 {
		t.Fatalf("ro key: %d", r.code)
	}
	e.createMonitor("job")
	if r := e.key(e.rw, "POST", "/monitors/job/check", nil); r.code != 422 {
		t.Fatalf("heartbeat check: %d %s", r.code, r.body)
	}
	obs := e.session("GET", "/monitors/api/observations", nil, false)
	if obs.code != 200 || !strings.Contains(string(obs.body), `"latency_ms":`) || !strings.Contains(string(obs.body), `"matched":true`) {
		t.Fatalf("observations: %d %s", obs.code, obs.body)
	}
	// the edit keeps the kind and re-validates the block
	upd := e.key(e.rw, "PUT", "/monitors/api", map[string]any{"slug": "api", "kind": "http", "interval": "45s", "http": map[string]any{"url": target.URL + "/v2"}})
	if upd.code != 200 {
		t.Fatalf("update: %d %s", upd.code, upd.body)
	}
	upd.json(t, &m)
	if m.Target != target.URL+"/v2" || m.Interval.String() != "45s" {
		t.Fatalf("updated: %s", upd.body)
	}
}

func TestMaintenanceAPI(t *testing.T) {
	e := newEnv(t)
	// Sunday 14:00 in Amsterdam: the weekly window is active
	weekly := e.key(e.rw, "POST", "/maintenance", map[string]any{"name": "weekly patching", "match_tags": []string{"prod"}, "rrule": "FREQ=WEEKLY;BYDAY=SU", "from": "13:00", "to": "17:00"})
	if weekly.code != 201 {
		t.Fatalf("create weekly: %d %s", weekly.code, weekly.body)
	}
	var w MaintenanceOut
	weekly.json(t, &w)
	if !w.Active || w.ActiveUntil == nil || w.Timezone != "Europe/Amsterdam" || w.RRule != "FREQ=WEEKLY;BYDAY=SU" || w.NextStart == nil {
		t.Fatalf("weekly: %s", weekly.body)
	}
	once := e.key(e.rw, "POST", "/maintenance", map[string]any{"name": "disk swap", "starts_at": e.now.Add(2 * time.Hour), "ends_at": e.now.Add(3 * time.Hour)})
	if once.code != 201 {
		t.Fatalf("create once: %d %s", once.code, once.body)
	}
	if r := e.key(e.rw, "POST", "/maintenance", map[string]any{"name": "bad", "rrule": "FREQ=DAILY"}); r.code != 422 || !strings.Contains(string(r.body), "rrule") {
		t.Fatalf("bad rrule: %d %s", r.code, r.body)
	}
	if r := e.key(e.ro, "POST", "/maintenance", map[string]any{"name": "x"}); r.code != 403 {
		t.Fatalf("ro key: %d", r.code)
	}
	list := e.session("GET", "/maintenance", nil, false)
	var page struct{ Items []MaintenanceOut }
	list.json(t, &page)
	if len(page.Items) != 2 || page.Items[0].Name != "weekly patching" || page.Items[1].NextStart == nil {
		t.Fatalf("list: %s", list.body)
	}
	upd := e.key(e.rw, "PUT", "/maintenance/"+w.ID, map[string]any{"name": "patching", "match_tags": []string{"prod", "db"}, "rrule": "FREQ=WEEKLY;BYDAY=SA,SU", "from": "13:00", "to": "17:00"})
	if upd.code != 200 || !strings.Contains(string(upd.body), `"rrule":"FREQ=WEEKLY;BYDAY=SA,SU"`) {
		t.Fatalf("update: %d %s", upd.code, upd.body)
	}
	ended := e.key(e.rw, "POST", "/maintenance/"+w.ID+"/end", nil)
	if ended.code != 200 || !strings.Contains(string(ended.body), `"ended_until"`) || strings.Contains(string(ended.body), `"active":true`) {
		t.Fatalf("end: %d %s", ended.code, ended.body)
	}
	if r := e.key(e.rw, "POST", "/maintenance/"+w.ID+"/end", nil); r.code != 422 {
		t.Fatalf("end again: %d", r.code)
	}
	if r := e.key(e.rw, "DELETE", "/maintenance/"+w.ID, nil); r.code != 204 {
		t.Fatalf("delete: %d", r.code)
	}
	if r := e.session("GET", "/maintenance/"+w.ID, nil, false); r.code != 404 {
		t.Fatalf("after delete: %d", r.code)
	}
}

func TestStatusPagesAPI(t *testing.T) {
	e := newEnv(t)
	created := e.key(e.rw, "POST", "/status-pages", map[string]any{"slug": "homelab", "title": "Homelab status", "match_tags": []string{"prod"}})
	if created.code != 201 || !strings.Contains(string(created.body), `"url":"http://localhost:8080/s/homelab"`) || !strings.Contains(string(created.body), `"public":true`) {
		t.Fatalf("create: %d %s", created.code, created.body)
	}
	private := e.key(e.rw, "POST", "/status-pages", map[string]any{"slug": "office", "title": "Office", "password": "s3cret"})
	if private.code != 201 || !strings.Contains(string(private.body), `"has_password":true`) || strings.Contains(string(private.body), "s3cret") || !strings.Contains(string(private.body), `"public":false`) {
		t.Fatalf("private: %d %s", private.code, private.body)
	}
	if r := e.key(e.rw, "POST", "/status-pages", map[string]any{"slug": "Bad Slug", "title": "x"}); r.code != 422 {
		t.Fatalf("bad slug: %d", r.code)
	}
	if r := e.key(e.rw, "POST", "/status-pages", map[string]any{"slug": "homelab", "title": "again"}); r.code != 409 {
		t.Fatalf("duplicate: %d", r.code)
	}
	if r := e.key(e.ro, "POST", "/status-pages", map[string]any{"slug": "x", "title": "x"}); r.code != 403 {
		t.Fatalf("ro key: %d", r.code)
	}
	list := e.session("GET", "/status-pages", nil, false)
	if list.code != 200 || strings.Count(string(list.body), `"slug":`) != 2 {
		t.Fatalf("list: %d %s", list.code, list.body)
	}
	upd := e.key(e.rw, "PUT", "/status-pages/homelab", map[string]any{"slug": "home", "title": "Home", "match_tags": []string{"prod", "backup"}, "custom_domain": "status.example.test"})
	if upd.code != 200 || !strings.Contains(string(upd.body), `"url":"http://localhost:8080/s/home"`) || !strings.Contains(string(upd.body), `"custom_domain":"status.example.test"`) {
		t.Fatalf("update: %d %s", upd.code, upd.body)
	}
	keep := e.key(e.rw, "PUT", "/status-pages/office", map[string]any{"slug": "office", "title": "Office", "public": false})
	if keep.code != 200 || !strings.Contains(string(keep.body), `"has_password":true`) {
		t.Fatalf("empty password keeps it: %d %s", keep.code, keep.body)
	}
	if r := e.key(e.rw, "DELETE", "/status-pages/home", nil); r.code != 204 {
		t.Fatalf("delete: %d", r.code)
	}
	if r := e.session("GET", "/status-pages/home", nil, false); r.code != 404 {
		t.Fatalf("after delete: %d", r.code)
	}
}

func TestApplyAndExportAPI(t *testing.T) {
	e := newEnv(t)
	file := map[string]any{"version": 1, "channels": []any{map[string]any{"name": "hook", "kind": "webhook", "url": "https://hooks.example.com/x"}}, "monitors": []any{map[string]any{"slug": "web", "kind": "http", "http": map[string]any{"url": "https://example.com/healthz"}}}}
	dry := e.key(e.rw, "PUT", "/apply?dry_run=1", file)
	if dry.code != 200 || !strings.Contains(string(dry.body), `"dry_run":true`) || !strings.Contains(string(dry.body), `"created":["channel hook","monitor web"]`) {
		t.Fatalf("dry run: %d %s", dry.code, dry.body)
	}
	if r := e.session("GET", "/monitors/web", nil, false); r.code != 404 {
		t.Fatal("dry run must not apply")
	}
	if r := e.key(e.rw, "PUT", "/apply", file); r.code != 200 || !strings.Contains(string(r.body), `"unchanged":[]`) {
		t.Fatalf("apply: %d %s", r.code, r.body)
	}
	if r := e.key(e.ro, "PUT", "/apply", file); r.code != 403 {
		t.Fatalf("ro apply: %d", r.code)
	}
	bad := e.key(e.rw, "PUT", "/apply", map[string]any{"version": 1, "monitors": []any{map[string]any{"slug": "x", "grace_period": "5m"}}})
	if bad.code != 422 || !strings.Contains(string(bad.body), "grace_period") {
		t.Fatalf("schema: %d %s", bad.code, bad.body)
	}
	exp := e.session("GET", "/export", nil, false)
	if exp.code != 200 || !strings.HasPrefix(exp.hdr.Get("Content-Type"), "application/yaml") || !strings.Contains(string(exp.body), "slug: web") || !strings.Contains(string(exp.body), "version: 1") {
		t.Fatalf("export: %d %s", exp.code, exp.body)
	}
	if r := e.key(e.ro, "GET", "/export?secrets=1", nil); r.code != 403 {
		t.Fatalf("secrets with ro key: %d", r.code)
	}
	if r := e.key(e.rw, "GET", "/export?secrets=1", nil); r.code != 200 {
		t.Fatalf("secrets with rw key: %d", r.code)
	}
}

// adminSession signs in an org admin and returns the cookie and CSRF token.
func (e *env) adminSession(t *testing.T) (*http.Cookie, string) {
	t.Helper()
	ctx := context.Background()
	admin := domain.Scope{InstanceAdmin: true, Role: domain.RoleOwner}
	user, err := e.svc.CreateLocalUser(ctx, admin, "adm", "adm@example.com", "Adm", "correct horse", false)
	if err != nil {
		t.Fatal(err)
	}
	_ = e.svc.SetMembership(ctx, admin, user.ID, e.org.ID, domain.RoleAdmin)
	rec := httptest.NewRecorder()
	p, err := e.authn.Login(rec, httptest.NewRequest("POST", "/login", nil), "adm", "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	return rec.Result().Cookies()[0], p.CSRF()
}

// TestOrgStatusPagesAPI: org admins manage the org's own pages under
// /orgs/{org}/status-pages with projects by slug; project pages take
// incidents and refuse the org-only fields.
func TestOrgStatusPagesAPI(t *testing.T) {
	e := newEnv(t)
	cookie, csrf := e.adminSession(t)
	as := func(r *http.Request) {
		r.AddCookie(cookie)
		r.Header.Set(auth.CSRFHeader, csrf)
	}
	member := func(r *http.Request) {
		r.AddCookie(e.cookie)
		r.Header.Set(auth.CSRFHeader, e.csrf)
	}
	root := "/api/v1/orgs/homelab/status-pages"
	if r := e.do("GET", root, nil, member); r.code != 403 {
		t.Fatalf("member: %d", r.code)
	}
	if r := e.key(e.rw, "GET", "/orgs/homelab/status-pages", nil); r.code != 403 {
		t.Fatalf("project key: %d", r.code)
	}
	created := e.do("POST", root, map[string]any{"slug": "all", "title": "Everything", "projects": []string{"prod"}, "incidents": "30d"}, as)
	if created.code != 201 || created.hdr.Get("Location") != "/api/v1/orgs/homelab/status-pages/all" {
		t.Fatalf("create: %d %s %v", created.code, created.body, created.hdr)
	}
	var out OrgStatusPageOut
	created.json(t, &out)
	if out.GroupBy != "project" || out.Incidents != "30d" || len(out.Projects) != 1 || out.Projects[0] != "prod" || out.URL != "http://localhost:8080/s/all" {
		t.Fatalf("created: %s", created.body)
	}
	if r := e.do("POST", root, map[string]any{"slug": "x", "title": "X", "projects": []string{"nope"}}, as); r.code != 422 || !strings.Contains(string(r.body), "no project nope in this org") {
		t.Fatalf("unknown project: %d %s", r.code, r.body)
	}
	if r := e.do("POST", root, map[string]any{"slug": "all", "title": "Again"}, as); r.code != 409 {
		t.Fatalf("taken: %d", r.code)
	}
	upd := e.do("PUT", root+"/all", map[string]any{"slug": "all", "title": "Everything", "group_by": "tag", "match_tags": []string{"prod"}}, as)
	if upd.code != 200 || !strings.Contains(string(upd.body), `"group_by":"tag"`) || !strings.Contains(string(upd.body), `"projects":[]`) || !strings.Contains(string(upd.body), `"incidents":"open"`) {
		t.Fatalf("put: %d %s", upd.code, upd.body)
	}
	list := e.do("GET", root, nil, as)
	if list.code != 200 || !strings.Contains(string(list.body), `"slug":"all"`) {
		t.Fatalf("list: %d %s", list.code, list.body)
	}
	if r := e.do("GET", "/api/v1/orgs/acme/status-pages", nil, as); r.code != 404 {
		t.Fatalf("another org: %d", r.code)
	}
	// the project's routes do not see the org's page
	if r := e.key(e.rw, "GET", "/status-pages/all", nil); r.code != 404 {
		t.Fatalf("org page through a project: %d", r.code)
	}
	// a project page takes incidents and refuses the org-only fields
	pp := e.key(e.rw, "POST", "/status-pages", map[string]any{"slug": "lab", "title": "Lab", "incidents": "7d"})
	if pp.code != 201 || !strings.Contains(string(pp.body), `"incidents":"7d"`) {
		t.Fatalf("project page: %d %s", pp.code, pp.body)
	}
	if r := e.key(e.rw, "POST", "/status-pages", map[string]any{"slug": "lab2", "title": "Lab", "group_by": "project"}); r.code != 422 || !strings.Contains(string(r.body), "only an org's page groups by project") {
		t.Fatalf("project page grouped by project: %d %s", r.code, r.body)
	}
	if r := e.do("DELETE", root+"/all", nil, as); r.code != 204 {
		t.Fatalf("delete: %d", r.code)
	}
	if r := e.do("GET", root+"/all", nil, as); r.code != 404 {
		t.Fatalf("after delete: %d", r.code)
	}
}

func TestAgentsAPI(t *testing.T) {
	e := newEnv(t)
	cookie, csrf := e.adminSession(t)
	as := func(c *http.Cookie, token string) func(*http.Request) {
		return func(r *http.Request) {
			r.AddCookie(c)
			r.Header.Set(auth.CSRFHeader, token)
		}
	}
	// a member session has no business here; a key neither
	if r := e.do("GET", "/api/v1/orgs/homelab/agents", nil, as(e.cookie, e.csrf)); r.code != 403 {
		t.Fatalf("member: %d", r.code)
	}
	if r := e.key(e.rw, "GET", "/orgs/homelab/agents", nil); r.code != 403 {
		t.Fatalf("key: %d", r.code)
	}
	created := e.do("POST", "/api/v1/orgs/homelab/agents", map[string]any{"name": "DC2-probe", "labels": map[string]string{"site": "dc2", "zone": "dmz"}}, as(cookie, csrf))
	if created.code != 201 {
		t.Fatalf("create: %d %s", created.code, created.body)
	}
	if loc := created.hdr.Get("Location"); loc != "/api/v1/orgs/homelab/agents/dc2-probe" {
		t.Errorf("Location of an org route: %q", loc)
	}
	var out AgentCreated
	created.json(t, &out)
	if out.Name != "dc2-probe" || out.State != domain.AgentWaiting || !strings.HasPrefix(out.Token, "vat_") || !strings.Contains(out.Command, "vink agent --server ws://localhost:8080") || !strings.Contains(out.Command, "--labels site=dc2,zone=dmz") || out.TokenPrefix == "" {
		t.Fatalf("created: %s", created.body)
	}
	if r := e.do("POST", "/api/v1/orgs/homelab/agents", map[string]any{"name": "dc2-probe"}, as(cookie, csrf)); r.code != 409 {
		t.Fatalf("duplicate: %d %s", r.code, r.body)
	}
	if r := e.do("POST", "/api/v1/orgs/homelab/agents", map[string]any{"name": "bad name"}, as(cookie, csrf)); r.code != 422 {
		t.Fatalf("bad name: %d", r.code)
	}
	list := e.do("GET", "/api/v1/orgs/homelab/agents", nil, as(cookie, csrf))
	if list.code != 200 || !strings.Contains(string(list.body), `"name":"dc2-probe"`) || strings.Contains(string(list.body), out.Token) {
		t.Fatalf("list: %d %s", list.code, list.body)
	}
	one := e.do("GET", "/api/v1/orgs/homelab/agents/dc2-probe", nil, as(cookie, csrf))
	if one.code != 200 || !strings.Contains(string(one.body), `"state":"waiting"`) {
		t.Fatalf("get: %d %s", one.code, one.body)
	}
	upd := e.do("PUT", "/api/v1/orgs/homelab/agents/dc2-probe/labels", map[string]any{"labels": map[string]string{"site": "dc3"}}, as(cookie, csrf))
	if upd.code != 200 || !strings.Contains(string(upd.body), `"labels":{"site":"dc3"}`) {
		t.Fatalf("labels: %d %s", upd.code, upd.body)
	}
	// the token verifies, until revoked
	if ag, err := e.svc.VerifyAgentToken(context.Background(), out.Token); err != nil || ag.Name != "dc2-probe" {
		t.Fatalf("verify: %v %+v", err, ag)
	}
	if _, err := e.svc.VerifyAgentToken(context.Background(), "vat_nope"); err == nil {
		t.Fatal("bad token must fail")
	}
	// the quota
	one64 := int64(1)
	if err := e.svc.DB().Write().SetOrgQuotas(context.Background(), db.SetOrgQuotasParams{QuotaAgents: &one64, ID: e.org.ID}); err != nil {
		t.Fatal(err)
	}
	if r := e.do("POST", "/api/v1/orgs/homelab/agents", map[string]any{"name": "second"}, as(cookie, csrf)); r.code != 422 || !strings.Contains(string(r.body), "ask the instance admin") {
		t.Fatalf("quota: %d %s", r.code, r.body)
	}
	if r := e.do("DELETE", "/api/v1/orgs/homelab/agents/dc2-probe", nil, as(cookie, csrf)); r.code != 204 {
		t.Fatalf("revoke: %d %s", r.code, r.body)
	}
	if r := e.do("GET", "/api/v1/orgs/homelab/agents/dc2-probe", nil, as(cookie, csrf)); r.code != 404 {
		t.Fatalf("after revoke: %d", r.code)
	}
	if _, err := e.svc.VerifyAgentToken(context.Background(), out.Token); err == nil {
		t.Fatal("revoked token must fail")
	}
	if r := e.do("GET", "/api/v1/orgs/acme/agents", nil, as(cookie, csrf)); r.code != 404 {
		t.Fatalf("foreign org: %d", r.code)
	}
}

func TestOrgKeys(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	orgAdmin := domain.Scope{OrgID: e.org.ID, Role: domain.RoleAdmin, Actor: "seed"}
	_, token, err := e.svc.CreateOrgAPIKey(ctx, orgAdmin, "gitops", domain.AccessRW)
	if err != nil {
		t.Fatal(err)
	}
	me := e.key(token, "GET", "/me", nil)
	if me.code != 200 || !strings.Contains(string(me.body), `"kind":"key"`) || !strings.Contains(string(me.body), `"slug":"homelab"`) || strings.Contains(string(me.body), `"project"`) {
		t.Fatalf("org key me: %d %s", me.code, me.body)
	}
	for _, p := range []string{"/monitors", "/channels", "/status", "/export"} {
		if r := e.key(token, "GET", p, nil); r.code != 403 || !strings.Contains(string(r.body), "org key may only export and apply") {
			t.Fatalf("org key on %s: %d %s", p, r.code, r.body)
		}
	}
	if r := e.key(token, "GET", "/orgs/homelab/agents", nil); r.code != 403 {
		t.Fatalf("org key on agents: %d %s", r.code, r.body)
	}
	if r := e.key(token, "GET", "/orgs/acme/agents", nil); r.code != 404 {
		t.Fatalf("org key on another org: %d %s", r.code, r.body)
	}
	if r := e.key(e.rw, "GET", "/orgs/homelab/agents", nil); r.code != 403 || !strings.Contains(string(r.body), "project key acts as its project") {
		t.Fatalf("project key on an org route: %d %s", r.code, r.body)
	}
}

func TestOrgApplyAndExport(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	orgAdmin := domain.Scope{OrgID: e.org.ID, Role: domain.RoleAdmin, Actor: "seed"}
	_, rw, err := e.svc.CreateOrgAPIKey(ctx, orgAdmin, "gitops", domain.AccessRW)
	if err != nil {
		t.Fatal(err)
	}
	_, ro, err := e.svc.CreateOrgAPIKey(ctx, orgAdmin, "reader", domain.AccessRO)
	if err != nil {
		t.Fatal(err)
	}
	file := map[string]any{"version": 1, "org": "homelab", "projects": []any{
		map[string]any{"slug": "prod", "monitors": []any{map[string]any{"slug": "web", "kind": "http", "http": map[string]any{"url": "https://example.com"}}}},
		map[string]any{"slug": "gitops", "name": "GitOps", "timezone": "UTC", "monitors": []any{map[string]any{"slug": "nightly", "schedule": map[string]any{"period": "1d"}, "grace": "1h"}}},
	}}
	dry := e.key(rw, "PUT", "/orgs/homelab/apply?dry_run=1", file)
	if dry.code != 200 || !strings.Contains(string(dry.body), `"dry_run":true`) || !strings.Contains(string(dry.body), `"slug":"gitops","project_created":true`) {
		t.Fatalf("dry run: %d %s", dry.code, dry.body)
	}
	if _, err := e.svc.ProjectBySlug(ctx, e.org.ID, "gitops"); err == nil {
		t.Fatal("dry run must not create")
	}
	applied := e.key(rw, "PUT", "/orgs/homelab/apply", file)
	if applied.code != 200 || !strings.Contains(string(applied.body), `"created":["monitor nightly"]`) {
		t.Fatalf("apply: %d %s", applied.code, applied.body)
	}
	if p, err := e.svc.ProjectBySlug(ctx, e.org.ID, "gitops"); err != nil || p.Name != "GitOps" {
		t.Fatalf("gitops: %+v %v", p, err)
	}
	export := e.key(ro, "GET", "/orgs/homelab/export", nil)
	if export.code != 200 || !strings.Contains(string(export.body), "org: homelab") || !strings.Contains(string(export.body), "slug: gitops") || !strings.Contains(string(export.body), "slug: lab") || !strings.Contains(string(export.body), "slug: nightly") {
		t.Fatalf("export: %d %s", export.code, export.body)
	}
	// who may not
	if r := e.key(ro, "PUT", "/orgs/homelab/apply", file); r.code != 403 {
		t.Fatalf("ro apply: %d %s", r.code, r.body)
	}
	if r := e.key(ro, "GET", "/orgs/homelab/export?secrets=1", nil); r.code != 403 {
		t.Fatalf("ro secrets: %d", r.code)
	}
	if r := e.key(e.rw, "GET", "/orgs/homelab/export", nil); r.code != 403 {
		t.Fatalf("project key: %d %s", r.code, r.body)
	}
	if r := e.key(rw, "GET", "/orgs/acme/export", nil); r.code != 404 {
		t.Fatalf("other org: %d", r.code)
	}
	wrong := map[string]any{"version": 1, "org": "acme", "projects": []any{}}
	if r := e.key(rw, "PUT", "/orgs/homelab/apply", wrong); r.code != 422 || !strings.Contains(string(r.body), "the file is for acme") {
		t.Fatalf("wrong org: %d %s", r.code, r.body)
	}
	if r := e.key(rw, "PUT", "/orgs/homelab/apply", map[string]any{"version": 1, "monitors": []any{}}); r.code != 422 || !strings.Contains(string(r.body), "project file") {
		t.Fatalf("project file on the org route: %d %s", r.code, r.body)
	}
	// an org admin's session may export and apply too
	cookie, csrf := e.adminSession(t)
	as := func(r *http.Request) { r.AddCookie(cookie); r.Header.Set(auth.CSRFHeader, csrf) }
	if r := e.do("GET", "/api/v1/orgs/homelab/export", nil, as); r.code != 200 || !strings.Contains(string(r.body), "org: homelab") {
		t.Fatalf("session export: %d %s", r.code, r.body)
	}
	if r := e.do("PUT", "/api/v1/orgs/homelab/apply", file, as); r.code != 200 {
		t.Fatalf("session apply: %d %s", r.code, r.body)
	}
	if r := e.do("GET", "/api/v1/orgs/homelab/export", nil, func(r *http.Request) { r.AddCookie(e.cookie) }); r.code != 403 {
		t.Fatalf("member session export: %d", r.code)
	}
}

func TestChannelsRoutesKeysPingKey(t *testing.T) {
	e := newEnv(t)
	r := e.key(e.rw, "POST", "/channels", map[string]any{"name": "ntfy", "kind": "ntfy", "config": map[string]any{"url": "https://ntfy.example.com", "topic": "vink", "token": "tk_secret"}})
	if r.code != 201 {
		t.Fatalf("create channel: %d %s", r.code, r.body)
	}
	var ch ChannelOut
	r.json(t, &ch)
	var cfg map[string]any
	_ = json.Unmarshal(ch.Config, &cfg)
	if cfg["token"] != "***" || cfg["topic"] != "vink" || !ch.Enabled {
		t.Fatalf("redaction: %+v", cfg)
	}
	// first channel got a default route
	r = e.key(e.rw, "GET", "/routes", nil)
	var routes page[RouteOut]
	r.json(t, &routes)
	if len(routes.Items) != 1 || len(routes.Items[0].Channels) != 1 || routes.Items[0].Channels[0].ID != ch.ID || len(routes.Items[0].On) != 2 || len(routes.Items[0].MatchTags) != 0 {
		t.Fatalf("default route: %+v", routes.Items)
	}
	// update with *** keeps the token
	r = e.key(e.rw, "PUT", "/channels/"+ch.ID, map[string]any{"name": "ntfy", "kind": "ntfy", "config": map[string]any{"url": "https://ntfy.example.com", "topic": "alerts", "token": "***"}, "enabled": false})
	if r.code != 200 {
		t.Fatalf("update channel: %d %s", r.code, r.body)
	}
	stored, err := e.svc.Channel(context.Background(), domain.Scope{OrgID: e.org.ID, ProjectID: e.project.ID, Role: domain.RoleAdmin}, ch.ID)
	if err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(stored.Config, &cfg)
	if cfg["token"] != "tk_secret" || cfg["topic"] != "alerts" || stored.Enabled {
		t.Fatalf("stored after update: %+v enabled=%v", cfg, stored.Enabled)
	}
	if r := e.key(e.rw, "POST", "/channels", map[string]any{"name": "bad", "kind": "carrier-pigeon", "config": map[string]any{}}); r.code != 422 {
		t.Errorf("bad kind: %d", r.code)
	}
	// routes
	r = e.key(e.rw, "POST", "/routes", map[string]any{"match_tags": []string{"prod"}, "channels": []string{ch.ID}, "on": []string{"down", "late"}, "repeat_every": "4h", "priority": 5})
	if r.code != 201 {
		t.Fatalf("create route: %d %s", r.code, r.body)
	}
	var rt RouteOut
	r.json(t, &rt)
	if len(rt.Channels) != 1 || rt.Channels[0].Name != "ntfy" || rt.RepeatEvery.String() != "4h" || rt.Priority != 5 {
		t.Fatalf("route: %+v", rt)
	}
	if r := e.key(e.rw, "POST", "/routes", map[string]any{"channels": []string{"nope"}}); r.code != 422 {
		t.Errorf("route with unknown channel: %d", r.code)
	}
	if r := e.key(e.rw, "POST", "/routes", map[string]any{"channels": []string{ch.ID}, "repeat_every": "1m"}); r.code != 422 {
		t.Errorf("route repeat too short: %d", r.code)
	}
	r = e.key(e.rw, "PUT", "/routes/"+rt.ID, map[string]any{"channels": []string{ch.ID}, "on": []string{"up"}})
	r.json(t, &rt)
	if r.code != 200 || len(rt.On) != 1 || rt.On[0] != domain.StateUp {
		t.Fatalf("update route: %d %+v", r.code, rt)
	}
	if r := e.key(e.rw, "DELETE", "/routes/"+rt.ID, nil); r.code != 204 {
		t.Errorf("delete route: %d", r.code)
	}
	if r := e.key(e.rw, "DELETE", "/channels/"+ch.ID, nil); r.code != 204 {
		t.Errorf("delete channel: %d", r.code)
	}
	r = e.key(e.rw, "GET", "/routes", nil)
	r.json(t, &routes)
	if len(routes.Items) != 0 {
		t.Errorf("routes must cascade with the channel: %+v", routes.Items)
	}

	// keys: ro cannot list; rw creates; plaintext once
	if r := e.key(e.ro, "GET", "/keys", nil); r.code != 403 {
		t.Errorf("ro listing keys: %d", r.code)
	}
	r = e.key(e.rw, "POST", "/keys", map[string]any{"name": "ci", "access": "ro"})
	var k KeyOut
	r.json(t, &k)
	if r.code != 201 || !strings.HasPrefix(k.Key, "vk_") || k.Access != domain.AccessRO {
		t.Fatalf("create key: %d %+v", r.code, k)
	}
	r = e.key(e.rw, "GET", "/keys", nil)
	var keys page[KeyOut]
	r.json(t, &keys)
	if len(keys.Items) != 3 || keys.Items[0].Key != "" {
		t.Errorf("list keys: %+v", keys.Items)
	}
	if r := e.key(e.rw, "DELETE", "/keys/"+k.ID, nil); r.code != 204 {
		t.Errorf("revoke: %d", r.code)
	}
	if r := e.key(k.Key, "GET", "/monitors", nil); r.code != 401 {
		t.Errorf("revoked key: %d", r.code)
	}
	// session member cannot create rw keys
	if r := e.session("POST", "/keys", map[string]any{"name": "x", "access": "rw"}, true); r.code != 403 {
		t.Errorf("member creating rw key: %d", r.code)
	}
	// ping key rotate: member forbidden, rw key allowed
	if r := e.session("POST", "/ping-key/rotate", nil, true); r.code != 403 {
		t.Errorf("member rotate: %d", r.code)
	}
	r = e.key(e.rw, "POST", "/ping-key/rotate", nil)
	var rot map[string]any
	r.json(t, &rot)
	if r.code != 200 || rot["ping_key"] == e.project.PingKey || rot["previous_key_valid_until"] == nil {
		t.Fatalf("rotate: %d %v", r.code, rot)
	}
}

func TestAPIUnderAPath(t *testing.T) {
	e := newEnv(t)
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	mux := http.NewServeMux()
	New(e.svc, e.authn, quiet).Mount(mux)
	srv := middleware.Chain(middleware.MountUnder("/vink", mux), middleware.RequestID)
	do := func(method, path string, body string) *httptest.ResponseRecorder {
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		req := httptest.NewRequest(method, path, rd)
		req.Header.Set("Authorization", "Bearer "+e.rw)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec
	}
	if rec := do("POST", "/vink/api/v1/monitors", `{"slug":"nightly","kind":"heartbeat","schedule":{"period":"1h"}}`); rec.Code != 201 || rec.Header().Get("Location") != "/vink/api/v1/monitors/nightly" {
		t.Fatalf("create under a path: %d Location %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec := do("POST", "/vink/api/v1/orgs/homelab/projects/prod/monitors", `{"slug":"weekly","kind":"heartbeat","schedule":{"period":"1h"}}`); rec.Code != 403 && rec.Header().Get("Location") != "" && !strings.HasPrefix(rec.Header().Get("Location"), "/vink/api/v1/orgs/homelab/projects/prod/monitors/") {
		t.Fatalf("org form Location: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec := do("GET", "/vink/api/v1/openapi.yaml", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), "  - url: /vink/api/v1\n") || strings.Contains(rec.Body.String(), "  - url: /api/v1\n") {
		t.Fatalf("openapi servers under a path: %d", rec.Code)
	}
	if rec := do("GET", "/vink/api/v1/nope", ""); rec.Code != 404 || !strings.Contains(rec.Body.String(), `"instance":"/vink/api/v1/nope"`) {
		t.Fatalf("problem instance under a path: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do("GET", "/api/v1/monitors", ""); rec.Code != 404 {
		t.Fatalf("the root must not answer: %d", rec.Code)
	}
	// with no prefix the document is served untouched
	if r := e.key(e.rw, "GET", "/openapi.yaml", nil); !strings.Contains(string(r.body), "  - url: /api/v1\n") {
		t.Fatal("openapi servers at the root")
	}
}

// TestProxyRefusalIsUnauthorized: a trusted proxy identity vink refuses
// (here a disabled account) is a 401 on the API, saying why.
func TestProxyRefusalIsUnauthorized(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := config.Default().Auth
	cfg.Proxy.Enabled = true
	cfg.Proxy.TrustedCIDRs = []string{"203.0.113.0/24"}
	cfg.Proxy.Secret = "s3cret"
	authn, err := auth.New(e.svc, cfg, "http://localhost:8080", quiet)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(e.svc, authn, quiet).Mount(mux)
	srv := middleware.Chain(mux, middleware.RequestID)
	bob, _ := e.svc.EnsureProxyUser(ctx, "bob", "", "")
	if err := e.svc.SetUserDisabled(ctx, domain.Scope{InstanceAdmin: true, Role: domain.RoleOwner, Actor: "test"}, bob.ID, true); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/api/v1/me", nil)
	req.RemoteAddr = "203.0.113.9:1"
	req.Header.Set("X-Auth-Proxy-Secret", "s3cret")
	req.Header.Set("Remote-User", "bob")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != 401 || !strings.Contains(rec.Body.String(), "the account bob is disabled") {
		t.Fatalf("disabled through the proxy: %d %s", rec.Code, rec.Body.String())
	}
}
