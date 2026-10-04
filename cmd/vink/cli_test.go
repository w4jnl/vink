package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/w4jnl/vink/internal/auth"
	"github.com/w4jnl/vink/internal/checks"
	"github.com/w4jnl/vink/internal/config"
	"github.com/w4jnl/vink/internal/db/dbtest"
	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/engine"
	vhttp "github.com/w4jnl/vink/internal/http"
	"github.com/w4jnl/vink/internal/outbound"
	"github.com/w4jnl/vink/internal/service"
)

type cliEnv struct {
	t       *testing.T
	svc     *service.Service
	srv     *httptest.Server
	project *domain.Project
	scope   domain.Scope
	rwKey   string
	roKey   string
}

func newCLIEnv(t *testing.T) *cliEnv {
	t.Helper()
	d := dbtest.Open(t)
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := config.Default()
	// bind first so the service knows the ping base before it serves /me
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	svcCfg := service.DefaultConfig()
	svcCfg.PingBaseURL = "http://" + ln.Addr().String()
	svcCfg.BaseURL = svcCfg.PingBaseURL
	svc := service.New(d, nil, quiet, svcCfg)
	ctx := context.Background()
	admin := domain.Scope{InstanceAdmin: true, Role: domain.RoleOwner}
	org, _ := svc.CreateOrg(ctx, admin, "homelab", "Homelab")
	project, _ := svc.CreateProject(ctx, admin, org.ID, "prod", "Prod", "UTC")
	sc := domain.Scope{OrgID: org.ID, ProjectID: project.ID, Role: domain.RoleAdmin, Actor: "test"}
	_, rw, _ := svc.CreateAPIKey(ctx, sc, "rw", domain.AccessRW)
	_, ro, _ := svc.CreateAPIKey(ctx, sc, "ro", domain.AccessRO)
	authn, err := auth.New(svc, cfg.Auth, cfg.Server.BaseURL, quiet)
	if err != nil {
		t.Fatal(err)
	}
	sched := engine.NewScheduler(svc, svc.Bus(), quiet, nil)
	srv := httptest.NewUnstartedServer(vhttp.Handler(vhttp.Deps{Cfg: cfg, Svc: svc, Auth: authn, Log: quiet, Sched: sched}, true))
	_ = srv.Listener.Close()
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)
	e := &cliEnv{t: t, svc: svc, srv: srv, project: project, scope: sc, rwKey: rw, roKey: ro}
	t.Setenv("VINK_CONFIG", filepath.Join(t.TempDir(), "config.toml"))
	t.Setenv("VINK_SERVER", srv.URL)
	t.Setenv("VINK_KEY", rw)
	t.Setenv("NO_COLOR", "1")
	return e
}

func (e *cliEnv) run(stdin string, args ...string) (string, string, int) {
	e.t.Helper()
	var out, errb bytes.Buffer
	code := run(context.Background(), append([]string{"--color", "never"}, args...), strings.NewReader(stdin), &out, &errb)
	return out.String(), errb.String(), code
}

func (e *cliEnv) monitor(slug string) {
	e.t.Helper()
	_, err := e.svc.CreateMonitor(context.Background(), e.scope, &domain.Monitor{Slug: slug, Name: slug, Kind: domain.KindHeartbeat,
		Heartbeat: &domain.HeartbeatSpec{Schedule: domain.Schedule{Period: domain.MustDuration("1h")}, Grace: domain.MustDuration("5m")}})
	if err != nil {
		e.t.Fatal(err)
	}
}

func TestCLIContexts(t *testing.T) {
	e := newCLIEnv(t)
	t.Setenv("VINK_SERVER", "")
	t.Setenv("VINK_KEY", "")
	if _, errs, code := e.run("", "ls"); code != 1 || !strings.Contains(errs, "no context") {
		t.Fatalf("ls without context: %d %s", code, errs)
	}
	out, errs, code := e.run("", "ctx", "add", "test", "--server", e.srv.URL, "--key", e.rwKey)
	if code != 0 {
		t.Fatalf("ctx add: %s", errs)
	}
	if !strings.Contains(out, "added context test") {
		t.Errorf("ctx add output: %s", out)
	}
	e.run("", "ctx", "add", "other", "--server", "https://other.example", "--key", "vk_other")
	out, _, _ = e.run("", "ctx", "ls")
	if !strings.Contains(out, "* test") && !strings.Contains(out, "*  test") {
		t.Errorf("ctx ls must mark the current context: %s", out)
	}
	if _, _, code := e.run("", "ctx", "use", "other"); code != 0 {
		t.Fatal("ctx use")
	}
	if _, errs, code := e.run("", "ls"); code != 2 || !strings.Contains(errs, "other.example") {
		t.Fatalf("ls against unreachable context must exit 2: %d %s", code, errs)
	}
	if _, _, code := e.run("", "ls", "--context", "test"); code != 0 {
		t.Fatal("ls --context test")
	}
	if _, _, code := e.run("", "ctx", "rm", "other"); code != 0 {
		t.Fatal("ctx rm")
	}
	out, _, _ = e.run("", "ctx", "ls", "--json")
	if !strings.Contains(out, `"Name":"test"`) || strings.Contains(out, "other") {
		t.Errorf("ctx ls --json: %s", out)
	}
}

func TestCLIMonitorsFlow(t *testing.T) {
	e := newCLIEnv(t)
	out, _, code := e.run("", "ls")
	if code != 0 || !strings.Contains(out, "no monitors") {
		t.Fatalf("empty ls: %d %s", code, out)
	}
	e.monitor("nightly")
	out, _, code = e.run("", "ls")
	if code != 0 || !strings.Contains(out, "SLUG") || !strings.Contains(out, "nightly") || !strings.Contains(out, "◌ new") || !strings.Contains(out, "in 59 min") {
		t.Fatalf("ls: %d %s", code, out)
	}
	out, _, _ = e.run("", "ls", "--json")
	var page struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &page); err != nil || len(page.Items) != 1 || page.Items[0]["slug"] != "nightly" {
		t.Fatalf("ls --json: %s", out)
	}
	if out, _, _ := e.run("", "ls", "--state", "down"); !strings.Contains(out, "no monitors") {
		t.Errorf("state filter: %s", out)
	}
	out, errs, code := e.run("", "get", "nightly")
	if code != 0 || !strings.Contains(out, "ping url") || !strings.Contains(out, "every 1h") {
		t.Fatalf("get: %d %s %s", code, out, errs)
	}
	if _, errs, code := e.run("", "get", "missing"); code != 1 || !strings.Contains(errs, "Not found") {
		t.Fatalf("get missing: %d %s", code, errs)
	}
	out, _, code = e.run("", "pause", "nightly")
	if code != 0 || !strings.Contains(out, "paused nightly: paused") {
		t.Fatalf("pause: %d %s", code, out)
	}
	if out, _, _ := e.run("", "resume", "nightly", "--quiet"); out != "" {
		t.Errorf("quiet resume printed %q", out)
	}
	// read-only key cannot pause
	t.Setenv("VINK_KEY", e.roKey)
	if _, errs, code := e.run("", "pause", "nightly"); code != 1 || !strings.Contains(errs, "Forbidden") {
		t.Fatalf("ro pause: %d %s", code, errs)
	}
	t.Setenv("VINK_KEY", e.rwKey)
	out, _, code = e.run("", "version", "--check-server")
	if code != 0 || !strings.Contains(out, "server dev (matches)") {
		t.Fatalf("version --check-server: %d %s", code, out)
	}
}

func TestCLIPingRunLogsStatusAck(t *testing.T) {
	e := newCLIEnv(t)
	// ping with --create makes the monitor; the ping key is fetched from /me and cached
	out, errs, code := e.run("", "ping", "job", "--create", "--msg", "hello")
	if code != 0 || out != "ok\n" {
		t.Fatalf("ping create: %d %s %s", code, out, errs)
	}
	if _, errs, code := e.run("", "ping", "nope"); code != 1 || !strings.Contains(errs, "404") {
		t.Fatalf("ping unknown: %d %s", code, errs)
	}
	// run wraps a command: exit 3 and the output tail as body
	out, errs, code = e.run("", "run", "job", "--", "sh", "-c", "echo from job; echo oops >&2; exit 3")
	if code != 3 || !strings.Contains(out, "from job") || !strings.Contains(errs, "oops") {
		t.Fatalf("run: code=%d out=%q err=%q", code, out, errs)
	}
	obs, _ := e.svc.ListObservations(context.Background(), e.scope, "job", service.HistoryPage{Limit: 10})
	if len(obs) != 3 || obs[0].Signal != domain.SignalExit || *obs[0].ExitCode != 3 || obs[0].DurationMs == nil || !obs[0].HasBody {
		t.Fatalf("observations after run: %+v", obs)
	}
	body, _, _ := e.svc.ObservationBody(context.Background(), e.scope, obs[0].ID)
	if !strings.Contains(string(body), "from job") || !strings.Contains(string(body), "oops") {
		t.Errorf("body: %q", body)
	}
	if obs[2].Detail["msg"] != "hello" {
		t.Errorf("msg not stored: %v", obs[2].Detail)
	}
	// a successful run exits 0
	if _, _, code := e.run("", "run", "job", "--", "true"); code != 0 {
		t.Fatalf("run true: %d", code)
	}
	out, _, code = e.run("", "logs", "job", "-n", "10")
	if code != 0 || !strings.Contains(out, "exit 3") || !strings.Contains(out, "● up") {
		t.Fatalf("logs: %d %s", code, out)
	}
	out, _, _ = e.run("", "logs", "job", "--json")
	if !strings.HasPrefix(out, `{"items":`) {
		t.Errorf("logs --json: %s", out[:40])
	}
	// status: exit 0 while up, 3 when down
	out, _, code = e.run("", "status")
	if code != 0 || !strings.Contains(out, "[✓▁] vink") || !strings.Contains(out, "● up 1") {
		t.Fatalf("status up: %d %s", code, out)
	}
	if _, _, code := e.run("", "ping", "job", "--fail"); code != 0 {
		t.Fatal("ping fail")
	}
	out, errs, code = e.run("", "status")
	if code != 3 || !strings.Contains(out, "◆ down 1") || !strings.Contains(out, "job down for") {
		t.Fatalf("status down: %d %s %s", code, out, errs)
	}
	incidents, _ := e.svc.ListIncidents(context.Background(), e.scope, true, 0, time.Time{})
	out, _, code = e.run("", "ack", incidents[0].ID)
	if code != 0 || !strings.Contains(out, "acknowledged incident") {
		t.Fatalf("ack: %d %s", code, out)
	}
	out, _, _ = e.run("", "status")
	if !strings.Contains(out, "acked by key:") {
		t.Errorf("status after ack: %s", out)
	}
	// start/exit pair via ping flags
	if _, _, code := e.run("", "ping", "job", "--start", "--rid", "01ARZ3NDEKTSV4RRFFQ69G5FAV"); code != 0 {
		t.Fatal("ping start")
	}
	time.Sleep(10 * time.Millisecond)
	if _, _, code := e.run("body text\n", "ping", "job", "--exit", "0", "--rid", "01ARZ3NDEKTSV4RRFFQ69G5FAV", "--body", "-"); code != 0 {
		t.Fatal("ping exit 0")
	}
	obs, _ = e.svc.ListObservations(context.Background(), e.scope, "job", service.HistoryPage{Limit: 1})
	if obs[0].DurationMs == nil || !obs[0].HasBody || !obs[0].OK {
		t.Fatalf("paired ping: %+v", obs[0])
	}
}

// TestCLIPingWithPingKey: with a ping key, ping and run need no API key
// and never call the API; the address comes from --ping-url, VINK_PING_URL,
// VINK_SERVER or the context, in that order.
func TestCLIPingWithPingKey(t *testing.T) {
	e := newCLIEnv(t)
	ctx := context.Background()
	key := e.project.PingKey
	// a key the API would refuse: pings succeed only if /me is never asked
	t.Setenv("VINK_KEY", "vk_not_a_key")
	t.Setenv("VINK_PING_KEY", key)
	if out, errs, code := e.run("", "ping", "job", "--create", "--msg", "from env"); code != 0 || out != "ok\n" {
		t.Fatalf("ping with VINK_PING_KEY and VINK_SERVER: %d %q %s", code, out, errs)
	}
	if _, err := os.Stat(os.Getenv("VINK_CONFIG")); !os.IsNotExist(err) { //nolint:gosec // G703: the test's own temp file
		t.Errorf("a ping key must not write the contexts file: %v", err)
	}

	// VINK_PING_URL alone gives the address, with or without the slash
	t.Setenv("VINK_SERVER", "")
	t.Setenv("VINK_KEY", "")
	t.Setenv("VINK_PING_URL", e.srv.URL+"/ping")
	out, errs, code := e.run("", "run", "job", "--", "sh", "-c", "echo from run; exit 2")
	if code != 2 || !strings.Contains(out, "from run") || strings.Contains(errs, "warning") {
		t.Fatalf("run with VINK_PING_URL: %d %q %q", code, out, errs)
	}
	obs, _ := e.svc.ListObservations(ctx, e.scope, "job", service.HistoryPage{Limit: 10})
	if len(obs) != 3 || obs[0].ExitCode == nil || *obs[0].ExitCode != 2 || !obs[0].HasBody || obs[0].DurationMs == nil {
		t.Fatalf("observations after run: %+v", obs)
	}

	// the flags, with nothing in the environment
	t.Setenv("VINK_PING_KEY", "")
	t.Setenv("VINK_PING_URL", "")
	if _, errs, code := e.run("", "ping", "job", "--fail", "--ping-key", key, "--ping-url", e.srv.URL+"/ping/"); code != 0 {
		t.Fatalf("ping with flags: %d %s", code, errs)
	}

	refused := []struct {
		name string
		args []string
		want string
	}{
		{"server url, not a ping url", []string{"--ping-key", key, "--ping-url", e.srv.URL}, "ending in /ping/"},
		{"ping url with a query", []string{"--ping-key", key, "--ping-url", e.srv.URL + "/ping/?x=1"}, "ending in /ping/"},
		{"not http", []string{"--ping-key", key, "--ping-url", "ftp://vink.example/ping/"}, "ending in /ping/"},
		{"a whole ping url as the key", []string{"--ping-key", e.srv.URL + "/ping/" + key, "--ping-url", e.srv.URL + "/ping/"}, "the key alone"},
		{"wrong key", []string{"--ping-key", "nokey", "--ping-url", e.srv.URL + "/ping/"}, "unknown ping key"},
		{"no address", []string{"--ping-key", key}, "needs an address"},
		{"unknown context", []string{"--ping-key", key, "--context", "nope"}, `no context named "nope"`},
	}
	for _, tc := range refused {
		t.Run(tc.name, func(t *testing.T) {
			_, errs, code := e.run("", append([]string{"ping", "job"}, tc.args...)...)
			if code != 1 || !strings.Contains(errs, tc.want) {
				t.Fatalf("want exit 1 with %q, got %d %q", tc.want, code, errs)
			}
		})
	}

	// the context's server is the fallback address
	if _, errs, code := e.run("", "ctx", "add", "ro", "--server", e.srv.URL, "--key", e.roKey); code != 0 {
		t.Fatalf("ctx add: %s", errs)
	}
	if _, errs, code := e.run("", "ping", "job", "--ping-key", key); code != 0 {
		t.Fatalf("ping key with the context's server: %d %s", code, errs)
	}
	// a read-only API key without a ping key points at the way out
	if _, errs, code := e.run("", "ping", "job"); code != 1 || !strings.Contains(errs, "VINK_PING_KEY") {
		t.Fatalf("read-only key: %d %s", code, errs)
	}

	// VINK_PING_URL also redirects the pings of an API-key context
	var got []string
	rec := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Method+" "+r.URL.Path)
	}))
	t.Cleanup(rec.Close)
	t.Setenv("VINK_SERVER", e.srv.URL)
	t.Setenv("VINK_KEY", e.rwKey)
	t.Setenv("VINK_PING_URL", rec.URL+"/vink/ping/")
	if _, errs, code := e.run("", "ping", "job"); code != 0 {
		t.Fatalf("ping through VINK_PING_URL: %d %s", code, errs)
	}
	if len(got) != 1 || got[0] != "POST /vink/ping/"+key+"/job" {
		t.Fatalf("the ping went to %v", got)
	}
}

// TestCLIPingLog: --log stores a note, msg or body, and changes no state;
// it needs a note, and the signal flags exclude each other.
func TestCLIPingLog(t *testing.T) {
	e := newCLIEnv(t)
	ctx := context.Background()
	e.monitor("job")
	state := func() domain.State {
		t.Helper()
		m, err := e.svc.MonitorBySlug(ctx, e.scope, "job")
		if err != nil {
			t.Fatal(err)
		}
		return m.State
	}
	if _, errs, code := e.run("", "ping", "job", "--fail"); code != 0 || state() != domain.StateDown {
		t.Fatalf("ping fail: %d %s %s", code, errs, state())
	}
	if out, errs, code := e.run("", "ping", "job", "--log", "--msg", "step 2 of 5"); code != 0 || out != "ok\n" {
		t.Fatalf("ping --log --msg: %d %q %s", code, out, errs)
	}
	if _, errs, code := e.run("1203 files so far\n", "ping", "job", "--log", "--body", "-", "--quiet"); code != 0 {
		t.Fatalf("ping --log --body: %d %s", code, errs)
	}
	if got := state(); got != domain.StateDown {
		t.Errorf("a note changed the state to %s", got)
	}
	obs, _ := e.svc.ListObservations(ctx, e.scope, "job", service.HistoryPage{Limit: 10})
	if len(obs) != 3 || obs[0].Signal != domain.SignalLog || !obs[0].HasBody || obs[1].Signal != domain.SignalLog || obs[1].Detail["msg"] != "step 2 of 5" {
		t.Fatalf("observations: %+v", obs)
	}
	body, _, _ := e.svc.ObservationBody(ctx, e.scope, obs[0].ID)
	if string(body) != "1203 files so far\n" {
		t.Errorf("note body: %q", body)
	}

	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"a note without text", []string{"--log"}, "--log sends a note"},
		{"two signals", []string{"--start", "--fail"}, "none of the others can be"},
		{"a note and an exit code", []string{"--log", "--msg", "x", "--exit", "0"}, "none of the others can be"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, errs, code := e.run("", append([]string{"ping", "job"}, tc.args...)...)
			if code != 1 || !strings.Contains(errs, tc.want) {
				t.Fatalf("want exit 1 with %q, got %d %q", tc.want, code, errs)
			}
		})
	}
	if obs, _ := e.svc.ListObservations(ctx, e.scope, "job", service.HistoryPage{Limit: 10}); len(obs) != 3 {
		t.Errorf("a refused command sent a ping: %d observations", len(obs))
	}
}

func TestPingBaseURL(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"https://vink.example.com/ping/", "https://vink.example.com/ping/"},
		{"https://vink.example.com/ping", "https://vink.example.com/ping/"},
		{"https://www.example.com/vink/ping", "https://www.example.com/vink/ping/"},
		{"http://127.0.0.1:8080/ping//", "http://127.0.0.1:8080/ping/"},
		{"https://vink.example.com/", ""},
		{"https://vink.example.com/pings/", ""},
		{"https://vink.example.com/ping/abc", ""},
		{"vink.example.com/ping/", ""},
		{"https://vink.example.com/ping/#x", ""},
	} {
		got, err := pingBaseURL(tc.in)
		if (err != nil) != (tc.want == "") || got != tc.want {
			t.Errorf("pingBaseURL(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
}

func TestCheckCommand(t *testing.T) {
	e := newCLIEnv(t)
	reg, err := checks.NewRegistry(checks.Options{Outbound: outbound.Options{AllowPrivateTargets: true}})
	if err != nil {
		t.Fatal(err)
	}
	e.svc.SetChecker(reg)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	defer target.Close()
	if _, err := e.svc.CreateMonitor(context.Background(), e.scope, &domain.Monitor{Slug: "web", Kind: domain.KindHTTP, Pull: &domain.PullSpec{HTTP: &domain.HTTPCheck{URL: target.URL}}}); err != nil {
		t.Fatal(err)
	}
	out, errs, code := e.run("", "check", "web")
	if code != 0 || !strings.Contains(out, "checked web: up") {
		t.Fatalf("check: %d %s %s", code, out, errs)
	}
	out, _, code = e.run("", "get", "web")
	if code != 0 || !strings.Contains(out, target.URL) || !strings.Contains(out, "interval") || !strings.Contains(out, "next check") {
		t.Fatalf("get pull monitor: %d %s", code, out)
	}
	t.Setenv("VINK_KEY", e.roKey)
	if _, errs, code := e.run("", "check", "web"); code != 1 || !strings.Contains(errs, "Forbidden") {
		t.Fatalf("ro check: %d %s", code, errs)
	}
}

func TestApplyAndExport(t *testing.T) {
	e := newCLIEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "vink.yaml")
	file := "version: 1\nchannels:\n  - {name: hook, kind: webhook, url: ${HOOK_URL}}\nmonitors:\n  - {slug: nightly, schedule: {period: 1h}, grace: 10m, tags: [backup]}\n  - {slug: web, kind: http, http: {url: https://example.com/healthz}}\n"
	if err := os.WriteFile(path, []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, errs, code := e.run("", "apply", "-f", path); code != 1 || !strings.Contains(errs, "HOOK_URL") {
		t.Fatalf("unset variable: %d %s", code, errs)
	}
	t.Setenv("HOOK_URL", "https://hooks.example.com/x")
	out, errs, code := e.run("", "apply", "-f", path, "--dry-run")
	if code != 0 || !strings.Contains(out, "+ channel hook") || !strings.Contains(out, "+ monitor web") || !strings.Contains(out, "dry run, nothing applied") {
		t.Fatalf("dry run: %d %s %s", code, out, errs)
	}
	if _, err := e.svc.MonitorBySlug(context.Background(), e.scope, "web"); err == nil {
		t.Fatal("dry run applied")
	}
	out, _, code = e.run("", "apply", "-f", path)
	if code != 0 || !strings.Contains(out, "3 created") {
		t.Fatalf("apply: %d %s", code, out)
	}
	out, _, code = e.run("", "apply", "-f", path)
	if code != 0 || !strings.Contains(out, "0 created, 0 updated, 0 recreated, 0 deleted, 3 unchanged") {
		t.Fatalf("second apply: %d %s", code, out)
	}
	bad := filepath.Join(dir, "bad.yaml")
	_ = os.WriteFile(bad, []byte("version: 1\nmonitors:\n  - {slug: x, grace_period: 5m}\n"), 0o600)
	if _, errs, code := e.run("", "apply", "-f", bad); code != 1 || !strings.Contains(errs, "grace_period") {
		t.Fatalf("schema error: %d %s", code, errs)
	}
	exported := filepath.Join(dir, "export.yaml")
	if out, errs, code := e.run("", "export", "-o", exported); code != 0 || !strings.Contains(out, "wrote") {
		t.Fatalf("export: %d %s %s", code, out, errs)
	}
	text, _ := os.ReadFile(exported)
	if !strings.Contains(string(text), "version: 1") || !strings.Contains(string(text), "slug: web") || !strings.Contains(string(text), "hooks.example.com/x") {
		t.Fatalf("exported:\n%s", text)
	}
	out, _, code = e.run("", "apply", "-f", exported, "--prune")
	if code != 0 || !strings.Contains(out, "0 created, 0 updated, 0 recreated, 0 deleted") {
		t.Fatalf("export round trip: %d %s\n%s", code, out, text)
	}
	t.Setenv("VINK_KEY", e.roKey)
	if _, errs, code := e.run("", "apply", "-f", path); code != 1 || !strings.Contains(errs, "Forbidden") {
		t.Fatalf("ro apply: %d %s", code, errs)
	}
	if _, errs, code := e.run("", "export", "--secrets"); code != 1 || !strings.Contains(errs, "Forbidden") {
		t.Fatalf("ro export with secrets: %d %s", code, errs)
	}
}

func TestImportCommands(t *testing.T) {
	e := newCLIEnv(t)
	dir := t.TempDir()
	kuma := filepath.Join(dir, "kuma.json")
	if err := os.WriteFile(kuma, []byte(`{"version":"1.23.3","notificationList":[{"id":1,"name":"Phone","config":"{\"type\":\"gotify\",\"gotifyserverurl\":\"https://gotify.lan\",\"gotifyapplicationToken\":\"AbC\"}"},{"id":2,"name":"Tg","config":"{\"type\":\"telegram\"}"}],
"monitorList":[{"id":1,"name":"Home page","type":"http","url":"https://example.com/","interval":60,"accepted_statuscodes":["200-299"]},{"id":2,"name":"Docker","type":"docker"},{"id":3,"name":"Backup job","type":"push","interval":3600,"retryInterval":60,"maxretries":3}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out, errs, code := e.run("", "import", "kuma", "-f", kuma)
	if code != 0 {
		t.Fatalf("import kuma: %d %s", code, errs)
	}
	for _, want := range []string{"version: 1", "name: Phone", "kind: gotify", "slug: home-page", "kind: http", "url: https://example.com/", "slug: backup-job", "schedule: {period: 1h}"} {
		if !strings.Contains(out, want) {
			t.Errorf("yaml lacks %q:\n%s", want, out)
		}
	}
	if !strings.Contains(errs, "skipped  Docker (docker): vink has no kind for a docker monitor") || !strings.Contains(errs, "skipped  notification Tg: vink has no channel kind for telegram") || !strings.Contains(errs, "note     Backup job (backup-job): a push monitor") {
		t.Errorf("stderr: %s", errs)
	}
	saved := filepath.Join(dir, "out.yaml")
	if out, errs, code := e.run("", "import", "kuma", "-f", kuma, "-o", saved); code != 0 || !strings.Contains(out, "wrote "+saved+": 2 monitors, 1 channels") {
		t.Fatalf("import -o: %d %s %s", code, out, errs)
	}
	if b, err := os.ReadFile(saved); err != nil || !strings.Contains(string(b), "slug: home-page") {
		t.Fatalf("saved file: %v", err)
	}
	out, errs, code = e.run("", "import", "kuma", "-f", kuma, "--apply", "--dry-run")
	if code != 0 || !strings.Contains(out, "+ ") || !strings.Contains(out, "(dry run, nothing applied)") {
		t.Fatalf("dry run: %d %s %s", code, out, errs)
	}
	if _, err := e.svc.MonitorBySlug(context.Background(), e.scope, "home-page"); err == nil {
		t.Fatal("dry run must not create")
	}
	out, errs, code = e.run("", "import", "kuma", "-f", kuma, "--apply")
	if code != 0 || !strings.Contains(out, "3 created") {
		t.Fatalf("apply: %d %s %s", code, out, errs)
	}
	m, err := e.svc.MonitorBySlug(context.Background(), e.scope, "home-page")
	if err != nil || m.Kind != domain.KindHTTP || m.Pull.HTTP.URL != "https://example.com/" {
		t.Fatalf("imported: %+v %v", m, err)
	}

	hc := filepath.Join(dir, "hc.json")
	if err := os.WriteFile(hc, []byte(`{"checks":[{"name":"Nightly","slug":"nightly","tags":"prod","grace":1800,"kind":"simple","timeout":86400}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out, errs, code = e.run("", "import", "healthchecks", "-f", hc)
	if code != 0 || !strings.Contains(out, "slug: nightly") || !strings.Contains(out, "schedule: {period: 1d}") || !strings.Contains(out, "grace: 30m") || !strings.Contains(out, "tags: [prod]") {
		t.Fatalf("import healthchecks: %d %s %s", code, out, errs)
	}
	if _, errs, code := e.run("", "import", "healthchecks", "-f", kuma); code != 1 || !strings.Contains(errs, "not a Healthchecks listing") {
		t.Fatalf("wrong format: %d %s", code, errs)
	}
	if _, errs, code := e.run("", "import", "kuma", "-f", filepath.Join(dir, "missing.json")); code != 1 || !strings.Contains(errs, "no such file") {
		t.Fatalf("missing file: %d %s", code, errs)
	}
}

func TestOrgExportAndApply(t *testing.T) {
	e := newCLIEnv(t)
	ctx := context.Background()
	e.monitor("web")
	_, orgKey, err := e.svc.CreateOrgAPIKey(ctx, domain.Scope{OrgID: e.project.OrgID, Role: domain.RoleAdmin, Actor: "seed"}, "gitops", domain.AccessRW)
	if err != nil {
		t.Fatal(err)
	}
	// a project key cannot act for the org
	if _, errs, code := e.run("", "export", "--org", "homelab"); code == 0 || !strings.Contains(errs, "project key acts as its project") {
		t.Fatalf("project key: %d %s", code, errs)
	}
	t.Setenv("VINK_KEY", orgKey)
	out, errs, code := e.run("", "export", "--org", "homelab")
	if code != 0 || !strings.Contains(out, "org: homelab") || !strings.Contains(out, "slug: prod") || !strings.Contains(out, "slug: web") {
		t.Fatalf("export --org: %d %s %s", code, out, errs)
	}
	if _, errs, code := e.run("", "export"); code == 0 || !strings.Contains(errs, "org key may only export and apply") {
		t.Fatalf("org key on a project export: %d %s", code, errs)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "org.yaml")
	file := out + "  - slug: lab\n    name: Lab\n    monitors:\n      - slug: nightly\n        schedule: {period: 1d}\n        grace: 1h\n"
	if err := os.WriteFile(path, []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	out, errs, code = e.run("", "apply", "-f", path, "--dry-run")
	if code != 0 || !strings.Contains(out, "lab (created)") || !strings.Contains(out, "  + monitor nightly") || !strings.Contains(out, "2 projects:") || !strings.Contains(out, "(dry run, nothing applied)") {
		t.Fatalf("dry run: %d %s %s", code, out, errs)
	}
	if _, err := e.svc.ProjectBySlug(ctx, e.project.OrgID, "lab"); err == nil {
		t.Fatal("dry run must not create")
	}
	out, errs, code = e.run("", "apply", "-f", path)
	if code != 0 || !strings.Contains(out, "prod\n") || !strings.Contains(out, "lab (created)") || !strings.Contains(out, "2 projects: 1 created, 0 updated") {
		t.Fatalf("apply: %d %s %s", code, out, errs)
	}
	if p, err := e.svc.ProjectBySlug(ctx, e.project.OrgID, "lab"); err != nil || p.Name != "Lab" {
		t.Fatalf("lab: %+v %v", p, err)
	}
	out, _, code = e.run("", "apply", "-f", path, "--json")
	if code != 0 || !strings.Contains(out, `"project_created":false`) {
		t.Fatalf("json: %d %s", code, out)
	}
	// an org file without org:, and an org file against a project key
	if err := os.WriteFile(path, []byte("version: 1\nprojects: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, errs, code := e.run("", "apply", "-f", path); code != 1 || !strings.Contains(errs, "org") {
		t.Fatalf("no org: %d %s", code, errs)
	}
}
