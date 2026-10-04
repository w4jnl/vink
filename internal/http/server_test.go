package http

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/w4jnl/vink/internal/config"
	"github.com/w4jnl/vink/internal/db/dbtest"
	"github.com/w4jnl/vink/internal/engine"
	"github.com/w4jnl/vink/internal/metrics"
	"github.com/w4jnl/vink/internal/service"
)

func testDeps(t *testing.T) Deps {
	t.Helper()
	d := dbtest.Open(t)
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := service.New(d, nil, quiet, service.DefaultConfig())
	sched := engine.NewScheduler(svc, svc.Bus(), quiet, nil)
	return Deps{Cfg: config.Default(), Svc: svc, Log: quiet, Sched: sched}
}

func TestHealthAndReady(t *testing.T) {
	d := testDeps(t)
	h := Handler(d, true)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != 200 || !strings.HasPrefix(rec.Body.String(), "ok ") {
		t.Fatalf("healthz: %d %q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("X-Request-Id") == "" {
		t.Error("request id missing")
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
	if rec.Code != 503 {
		t.Fatalf("readyz before the scheduler ticked: %d", rec.Code)
	}
	if _, err := d.Sched.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
	if rec.Code != 200 {
		t.Fatalf("readyz after tick: %d %s", rec.Code, rec.Body.String())
	}
	// ping route is mounted on the main handler
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/ping/nope/x", nil))
	if rec.Code != 404 || rec.Body.String() != "not found\n" {
		t.Fatalf("ping mounted: %d %q", rec.Code, rec.Body.String())
	}
	// and not when a separate ping listener is configured
	rec = httptest.NewRecorder()
	Handler(d, false).ServeHTTP(rec, httptest.NewRequest("GET", "/ping/nope/x", nil))
	if rec.Body.String() == "not found\n" {
		t.Fatal("ping must not be mounted on the main handler when split")
	}
}

func TestServeStartsAndStops(t *testing.T) {
	d := testDeps(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, d.Log, "test", "127.0.0.1:0", Handler(d, true)) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
	if err := Run(context.Background(), d.Log, "bad", "256.0.0.1:1", http.NotFoundHandler()); err == nil {
		t.Fatal("expected listen error")
	}
}

func TestMetricsEndpoint(t *testing.T) {
	d := testDeps(t)
	rec := httptest.NewRecorder()
	Handler(d, true).ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if rec.Code != 404 {
		t.Fatalf("metrics without instruments: %d", rec.Code)
	}
	d.Metrics = metrics.New("test")
	d.Svc.SetMetrics(d.Metrics)
	d.Cfg.Metrics.Token = "scrape-me"
	h := Handler(d, true)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if rec.Code != 401 {
		t.Fatalf("metrics without token: %d", rec.Code)
	}
	req := httptest.NewRequest("GET", "/metrics", nil)
	req.Header.Set("Authorization", "Bearer scrape-me")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "vink_build_info") || !strings.Contains(rec.Body.String(), "vink_deliveries_pending 0") {
		t.Fatalf("metrics: %d %s", rec.Code, rec.Body.String()[:200])
	}
}

func TestMountedUnderAPath(t *testing.T) {
	d := testDeps(t)
	d.Cfg.Server.BaseURL = "http://vink.test/vink"
	h := Handler(d, true)
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		return rec
	}
	if rec := get("/vink/healthz"); rec.Code != 200 || !strings.HasPrefix(rec.Body.String(), "ok ") {
		t.Fatalf("healthz under the prefix: %d %q", rec.Code, rec.Body.String())
	}
	// one deployment, one mount: the root is not served
	for _, path := range []string{"/healthz", "/", "/vinkx/healthz", "/ping/nope/x"} {
		if rec := get(path); rec.Code != 404 {
			t.Fatalf("%s outside the prefix: %d", path, rec.Code)
		}
	}
	if rec := get("/vink"); rec.Code != 301 || rec.Header().Get("Location") != "/vink/" {
		t.Fatalf("bare prefix: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec := get("/vink?x=1"); rec.Header().Get("Location") != "/vink/?x=1" {
		t.Fatalf("bare prefix keeps the query: %q", rec.Header().Get("Location"))
	}
	// Go's canonical-path redirect is built from the stripped path; the mount puts the prefix back
	if rec := get("/vink/ping"); (rec.Code != 301 && rec.Code != 307) || rec.Header().Get("Location") != "/vink/ping/" {
		t.Fatalf("canonical redirect: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec := get("/vink/ping/nope/x"); rec.Code != 404 || rec.Body.String() != "not found\n" {
		t.Fatalf("ping under the prefix: %d %q", rec.Code, rec.Body.String())
	}
	// the separate ping listener follows the ping base URL's path
	d.Cfg.Ping.Listen = ":0"
	d.Cfg.Ping.BaseURL = "http://ping.test/hooks"
	ph := PingHandler(d)
	rec := httptest.NewRecorder()
	ph.ServeHTTP(rec, httptest.NewRequest("GET", "/hooks/ping/nope/x", nil))
	if rec.Code != 404 || rec.Body.String() != "not found\n" {
		t.Fatalf("split ping under its path: %d %q", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	ph.ServeHTTP(rec, httptest.NewRequest("GET", "/ping/nope/x", nil))
	if rec.Code != 404 || rec.Body.String() == "not found\n" {
		t.Fatalf("split ping at the root must not be served: %d %q", rec.Code, rec.Body.String())
	}
}
