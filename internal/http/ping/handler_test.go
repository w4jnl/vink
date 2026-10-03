package ping

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/w4jnl/vink/internal/db/dbtest"
	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/http/middleware"
	"github.com/w4jnl/vink/internal/ratelimit"
	"github.com/w4jnl/vink/internal/service"
)

type env struct {
	svc     *service.Service
	h       *Handler
	srv     http.Handler
	project *domain.Project
	scope   domain.Scope
	now     time.Time
}

func newEnv(t *testing.T) *env {
	t.Helper()
	d := dbtest.Open(t)
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := service.New(d, nil, quiet, service.DefaultConfig())
	e := &env{svc: svc, now: time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)}
	svc.SetClock(func() time.Time { return e.now })
	ctx := context.Background()
	admin := domain.Scope{InstanceAdmin: true, Role: domain.RoleOwner}
	org, err := svc.CreateOrg(ctx, admin, "o", "O")
	if err != nil {
		t.Fatal(err)
	}
	e.project, err = svc.CreateProject(ctx, admin, org.ID, "p", "P", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	e.scope = domain.Scope{OrgID: org.ID, ProjectID: e.project.ID, Role: domain.RoleMember}
	e.h = New(svc, quiet, Options{BodyLimit: 32, RatePerMonitor: 10, RatePerIP: 300})
	e.h.now = func() time.Time { return e.now }
	mux := http.NewServeMux()
	e.h.Routes(mux)
	e.srv = middleware.Chain(mux, middleware.RequestID, middleware.RealIP([]string{"10.0.0.0/8"}))
	return e
}

func (e *env) monitor(t *testing.T, slug string, spec *domain.HeartbeatSpec) *domain.Monitor {
	t.Helper()
	if spec == nil {
		spec = &domain.HeartbeatSpec{Schedule: domain.Schedule{Period: domain.MustDuration("1h")}}
	}
	m, err := e.svc.CreateMonitor(context.Background(), e.scope, &domain.Monitor{Slug: slug, Kind: domain.KindHeartbeat, Heartbeat: spec})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func (e *env) do(method, path string, body string, hdr map[string]string) *httptest.ResponseRecorder {
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.RemoteAddr = "203.0.113.9:4444"
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.srv.ServeHTTP(rec, req)
	return rec
}

func (e *env) lastObs(t *testing.T, slug string) *domain.Observation {
	t.Helper()
	page, err := e.svc.ListObservations(context.Background(), e.scope, slug, service.HistoryPage{Limit: 1})
	if err != nil || len(page) == 0 {
		t.Fatalf("no observation for %s: %v", slug, err)
	}
	return page[0]
}

func TestPingForms(t *testing.T) {
	e := newEnv(t)
	m := e.monitor(t, "job", nil)
	base := "/ping/" + e.project.PingKey + "/job"
	cases := []struct {
		name, method, path string
		signal             domain.Signal
		ok                 bool
		exit               int64
	}{
		{"plain get", "GET", base, domain.SignalOK, true, 0},
		{"post", "POST", base, domain.SignalOK, true, 0},
		{"put", "PUT", base, domain.SignalOK, true, 0},
		{"start", "GET", base + "/start", domain.SignalStart, false, 0},
		{"fail", "GET", base + "/fail", domain.SignalFail, false, 0},
		{"log", "POST", base + "/log", domain.SignalLog, false, 0},
		{"exit 0", "GET", base + "/0", domain.SignalExit, true, 0},
		{"exit 3", "GET", base + "/3", domain.SignalExit, false, 3},
		{"by id", "GET", "/ping/id/" + m.ID, domain.SignalOK, true, 0},
		{"by id fail", "POST", "/ping/id/" + m.ID + "/fail", domain.SignalFail, false, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := e.do(c.method, c.path, "", nil)
			if rec.Code != 200 || rec.Body.String() != "OK\n" {
				t.Fatalf("status %d body %q", rec.Code, rec.Body.String())
			}
			if rec.Header().Get("Ping-Body-Limit") != "32" {
				t.Errorf("Ping-Body-Limit = %q", rec.Header().Get("Ping-Body-Limit"))
			}
			obs := e.lastObs(t, "job")
			if obs.Signal != c.signal || obs.OK != c.ok {
				t.Fatalf("obs signal=%s ok=%v, want %s %v", obs.Signal, obs.OK, c.signal, c.ok)
			}
			if c.signal == domain.SignalExit && (obs.ExitCode == nil || *obs.ExitCode != c.exit) {
				t.Fatalf("exit code = %v", obs.ExitCode)
			}
			if obs.Detail["method"] != c.method || obs.RemoteAddr != "203.0.113.9" {
				t.Errorf("detail %v addr %s", obs.Detail, obs.RemoteAddr)
			}
		})
	}
	rec := e.do("HEAD", base, "", nil)
	if rec.Code != 200 || rec.Body.Len() != 0 {
		t.Fatalf("HEAD: %d %q", rec.Code, rec.Body.String())
	}
	if rec := e.do("DELETE", base, "", nil); rec.Code != 405 {
		t.Fatalf("DELETE: %d", rec.Code)
	}
}

func TestNotFoundParity(t *testing.T) {
	e := newEnv(t)
	e.monitor(t, "job", nil)
	badKey := e.do("GET", "/ping/0000000000000000000000/job", "", nil)
	badSlug := e.do("GET", "/ping/"+e.project.PingKey+"/nope", "", nil)
	badSignal := e.do("GET", "/ping/"+e.project.PingKey+"/job/explode", "", nil)
	badID := e.do("GET", "/ping/id/01ARZ3NDEKTSV4RRFFQ69G5FAV", "", nil)
	for name, rec := range map[string]*httptest.ResponseRecorder{"key": badKey, "slug": badSlug, "signal": badSignal, "id": badID} {
		if rec.Code != 404 || rec.Body.String() != "not found\n" {
			t.Errorf("%s: %d %q", name, rec.Code, rec.Body.String())
		}
	}
	if badKey.Header().Get("Content-Type") != badSlug.Header().Get("Content-Type") {
		t.Error("headers differ between unknown key and unknown slug")
	}
}

func TestAutoCreate(t *testing.T) {
	e := newEnv(t)
	if rec := e.do("GET", "/ping/"+e.project.PingKey+"/newjob", "", nil); rec.Code != 404 {
		t.Fatalf("without create: %d", rec.Code)
	}
	rec := e.do("GET", "/ping/"+e.project.PingKey+"/newjob?create=1", "", nil)
	if rec.Code != 200 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	m, err := e.svc.MonitorBySlug(context.Background(), e.scope, "newjob")
	if err != nil || m.State != domain.StateUp {
		t.Fatalf("auto-created: %v %v", m, err)
	}
	if rec := e.do("GET", "/ping/"+e.project.PingKey+"/Bad_Slug?create=1", "", nil); rec.Code != 404 {
		t.Fatalf("invalid slug create: %d", rec.Code)
	}
	if rec := e.do("GET", "/ping/wrongkey/x?create=1", "", nil); rec.Code != 404 {
		t.Fatalf("create with bad key: %d", rec.Code)
	}
}

func TestBodyCaptureTruncationAndMsg(t *testing.T) {
	e := newEnv(t)
	e.monitor(t, "job", nil)
	base := "/ping/" + e.project.PingKey + "/job"
	rec := e.do("POST", base+"?msg=hello+world&rid=01ARZ3NDEKTSV4RRFFQ69G5FAV", "short body", map[string]string{"Content-Type": "text/plain", "User-Agent": "curl/8"})
	if rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	obs := e.lastObs(t, "job")
	if !obs.HasBody || obs.Detail["msg"] != "hello world" || obs.RunID != "01ARZ3NDEKTSV4RRFFQ69G5FAV" || obs.UserAgent != "curl/8" {
		t.Fatalf("obs: %+v", obs)
	}
	body, ct, err := e.svc.ObservationBody(context.Background(), e.scope, obs.ID)
	if err != nil || string(body) != "short body" || ct != "text/plain" {
		t.Fatalf("body %q %q %v", body, ct, err)
	}
	// larger than the limit without Content-Length: truncated and flagged
	long := strings.Repeat("x", 100)
	req := httptest.NewRequest("POST", base, strings.NewReader(long))
	req.ContentLength = -1
	req.RemoteAddr = "203.0.113.9:1"
	rr := httptest.NewRecorder()
	e.srv.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("truncated post: %d", rr.Code)
	}
	obs = e.lastObs(t, "job")
	body, _, _ = e.svc.ObservationBody(context.Background(), e.scope, obs.ID)
	if obs.Detail["truncated"] != true || len(body) != 32 {
		t.Fatalf("truncation: detail=%v len=%d", obs.Detail, len(body))
	}
	// declared Content-Length over the limit is refused
	if rec := e.do("POST", base, long, nil); rec.Code != 413 {
		t.Fatalf("413 expected, got %d", rec.Code)
	}
	// empty body stores nothing
	e.do("GET", base, "", nil)
	if obs := e.lastObs(t, "job"); obs.HasBody {
		t.Error("empty body must not be stored")
	}
	// msg is capped
	e.do("GET", base+"?msg="+strings.Repeat("m", 2500), "", nil)
	if obs := e.lastObs(t, "job"); len(obs.Detail["msg"].(string)) != MaxMsgLen {
		t.Error("msg not capped")
	}
}

func TestMethodRestriction(t *testing.T) {
	e := newEnv(t)
	e.monitor(t, "postonly", &domain.HeartbeatSpec{Schedule: domain.Schedule{Period: domain.MustDuration("1h")}, Methods: []string{"POST"}})
	base := "/ping/" + e.project.PingKey + "/postonly"
	if rec := e.do("GET", base, "", nil); rec.Code != 405 || rec.Header().Get("Allow") != "POST" {
		t.Fatalf("GET on POST-only: %d allow=%q", rec.Code, rec.Header().Get("Allow"))
	}
	if rec := e.do("POST", base, "", nil); rec.Code != 200 {
		t.Fatalf("POST: %d", rec.Code)
	}
}

func TestRateLimits(t *testing.T) {
	e := newEnv(t)
	e.monitor(t, "job", nil)
	base := "/ping/" + e.project.PingKey + "/job"
	got429 := false
	for i := 0; i < 40; i++ {
		rec := e.do("GET", base, "", nil)
		if rec.Code == 429 {
			if rec.Header().Get("Retry-After") == "" {
				t.Fatal("429 without Retry-After")
			}
			got429 = true
			break
		}
	}
	if !got429 {
		t.Fatal("per-monitor limit never triggered")
	}
	// another monitor from the same IP still works (IP limit is higher)
	e.monitor(t, "other", nil)
	if rec := e.do("GET", "/ping/"+e.project.PingKey+"/other", "", nil); rec.Code != 200 {
		t.Fatalf("other monitor: %d", rec.Code)
	}
	// per-IP limit
	e.h.byIP = ratelimit.New(5, 5)
	blocked := false
	for i := 0; i < 10; i++ {
		if rec := e.do("GET", "/ping/"+e.project.PingKey+"/other", "", nil); rec.Code == 429 {
			blocked = true
			break
		}
	}
	if !blocked {
		t.Fatal("per-IP limit never triggered")
	}
	// a different IP is unaffected
	req := httptest.NewRequest("GET", "/ping/"+e.project.PingKey+"/other", nil)
	req.RemoteAddr = "198.51.100.7:1"
	rr := httptest.NewRecorder()
	e.srv.ServeHTTP(rr, req)
	if rr.Code == 429 {
		t.Fatal("other IP must not be limited")
	}
}

func TestTrustedProxyAddress(t *testing.T) {
	e := newEnv(t)
	e.monitor(t, "job", nil)
	req := httptest.NewRequest("GET", "/ping/"+e.project.PingKey+"/job", nil)
	req.RemoteAddr = "10.1.2.3:1"
	req.Header.Set("X-Forwarded-For", "198.51.100.42")
	rr := httptest.NewRecorder()
	e.srv.ServeHTTP(rr, req)
	if obs := e.lastObs(t, "job"); obs.RemoteAddr != "198.51.100.42" {
		t.Fatalf("remote addr = %s", obs.RemoteAddr)
	}
}
