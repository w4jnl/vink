package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/w4jnl/vink/ping"
)

func TestContextsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.toml")
	cfg, err := LoadConfig(path)
	if err != nil || len(cfg.Contexts) != 0 {
		t.Fatalf("empty load: %v %v", cfg, err)
	}
	if err := cfg.Add("home", Context{Server: "https://vink.w4j.nl/", Key: "vk_x"}); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Add("work", Context{Server: "https://vink.corp", Key: "vk_y"}); err != nil {
		t.Fatal(err)
	}
	if cfg.Current != "home" || cfg.Contexts["home"].Server != "https://vink.w4j.nl" {
		t.Fatalf("after add: %+v", cfg)
	}
	for _, bad := range []struct{ name, server, key string }{{"", "https://x", "k"}, {"n", "vink.example.com", "k"}, {"n", "https://x", ""}} {
		if err := cfg.Add(bad.name, Context{Server: bad.server, Key: bad.key}); err == nil {
			t.Errorf("Add(%+v) must fail", bad)
		}
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0o600 {
		t.Errorf("config mode %v", st.Mode().Perm())
	}
	again, err := LoadConfig(path)
	if err != nil || again.Current != "home" || again.Contexts["work"].Key != "vk_y" || len(again.Names()) != 2 {
		t.Fatalf("reload: %+v %v", again, err)
	}
	if err := again.Use("work"); err != nil || again.Current != "work" {
		t.Fatal("use")
	}
	if err := again.Use("nope"); err == nil {
		t.Fatal("use unknown")
	}
	r, err := again.Resolve("", func(string) string { return "" })
	if err != nil || r.Name != "work" || r.Server != "https://vink.corp" {
		t.Fatalf("resolve current: %+v %v", r, err)
	}
	r, _ = again.Resolve("home", func(string) string { return "" })
	if r.Key != "vk_x" {
		t.Fatalf("resolve named: %+v", r)
	}
	env := map[string]string{"VINK_SERVER": "http://ci:8080/", "VINK_KEY": "vk_ci"}
	r, _ = again.Resolve("", func(k string) string { return env[k] })
	if !r.FromEnv || r.Server != "http://ci:8080" || r.Key != "vk_ci" {
		t.Fatalf("resolve env: %+v", r)
	}
	if err := again.Remove("work"); err != nil || again.Current != "home" {
		t.Fatalf("remove: %v current=%s", err, again.Current)
	}
	if err := again.Remove("work"); err == nil {
		t.Fatal("remove twice")
	}
	empty, _ := LoadConfig(filepath.Join(t.TempDir(), "none.toml"))
	if _, err := empty.Resolve("", func(string) string { return "" }); err == nil {
		t.Fatal("resolve without contexts must fail")
	}
	t.Setenv("VINK_CONFIG", "/tmp/x.toml")
	if ConfigPath() != "/tmp/x.toml" {
		t.Error("VINK_CONFIG")
	}
}

func TestClientDoAndErrors(t *testing.T) {
	var gotAuth, gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotUA = r.Header.Get("Authorization"), r.Header.Get("User-Agent")
		switch r.URL.Path {
		case "/api/v1/ok":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"items":[{"slug":"a"}],"next_cursor":null}`))
		case "/api/v1/problem":
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(422)
			_, _ = w.Write([]byte(`{"type":"x","title":"Validation failed","status":422,"errors":[{"field":"grace","msg":"must be at least 60s"}]}`))
		case "/api/v1/down":
			w.WriteHeader(502)
			_, _ = w.Write([]byte("bad gateway"))
		}
	}))
	defer srv.Close()
	var dbg bytes.Buffer
	c := NewClient(srv.URL+"/", "vk_test")
	c.Debug, c.Log = true, &dbg
	var out struct {
		Items []struct{ Slug string } `json:"items"`
	}
	raw, err := c.DoRaw(context.Background(), "GET", "/ok", nil, &out)
	if err != nil || len(out.Items) != 1 || !strings.Contains(string(raw), `"slug":"a"`) {
		t.Fatalf("ok: %v %+v", err, out)
	}
	if gotAuth != "Bearer vk_test" || !strings.HasPrefix(gotUA, "vink-cli/") {
		t.Errorf("headers: %q %q", gotAuth, gotUA)
	}
	if !strings.Contains(dbg.String(), "> GET ") || !strings.Contains(dbg.String(), "< 200") {
		t.Errorf("debug output: %q", dbg.String())
	}
	err = c.Do(context.Background(), "POST", "/problem", map[string]string{"a": "b"}, nil)
	var ee *ExitError
	if !errors.As(err, &ee) || ee.Code != ExitUser || err.Error() != "Validation failed: grace must be at least 60s" {
		t.Fatalf("problem: %v", err)
	}
	err = c.Do(context.Background(), "GET", "/down", nil, nil)
	if !errors.As(err, &ee) || ee.Code != ExitServer {
		t.Fatalf("5xx: %v", err)
	}
	dead := NewClient("http://127.0.0.1:1", "k")
	if err := dead.Do(context.Background(), "GET", "/x", nil, nil); !errors.As(err, &ee) || ee.Code != ExitServer {
		t.Fatalf("connection refused: %v", err)
	}
}

// TestPingClient: pings go through the ping module with the CLI's
// User-Agent; its errors get the CLI's exit codes; the ping key never
// reaches a debug line or an error, which end up in cron mail and pasted
// logs.
func TestPingClient(t *testing.T) {
	const key = "s3cretpingkey"
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.UserAgent()
		switch r.URL.Path {
		case "/ping/" + key + "/ok":
		case "/ping/" + key + "/busy":
			w.Header().Set("Retry-After", "120")
			w.WriteHeader(http.StatusTooManyRequests)
		case "/ping/" + key + "/broken":
			w.WriteHeader(http.StatusInternalServerError)
		case "/ping/" + key + "/odd":
			w.WriteHeader(http.StatusTeapot)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	var log bytes.Buffer
	pc, err := PingClient(srv.URL+"/ping/", key, true, &log, ping.WithAttempts(1))
	if err != nil {
		t.Fatal(err)
	}
	if err := pc.Monitor("ok").Success(ctx); err != nil || !strings.HasPrefix(gotUA, "vink-cli/") {
		t.Fatalf("ping: %v, User-Agent %q", err, gotUA)
	}
	if !strings.Contains(log.String(), "> POST "+srv.URL+"/ping/<ping key>/ok (0 bytes)\n< 200 OK") || strings.Contains(log.String(), key) {
		t.Errorf("debug lines: %q", log.String())
	}
	for _, tc := range []struct {
		slug string
		code int
		msg  string
	}{
		{"missing", ExitUser, "unknown ping key or monitor (404)"},
		{"odd", ExitUser, "ping rejected: 418"},
		{"busy", ExitServer, "rate limited (429)"},
		{"broken", ExitServer, "ping failed: 500"},
	} {
		var ee *ExitError
		err := PingError(pc.Monitor(tc.slug).Success(ctx))
		if !errors.As(err, &ee) || ee.Code != tc.code || !strings.Contains(err.Error(), tc.msg) {
			t.Errorf("%s: %v, want exit %d with %q", tc.slug, err, tc.code, tc.msg)
		}
	}
	// vink away: exit 2, the URL shown without the key, the cause kept
	dead, err := PingClient("http://127.0.0.1:1/vink/ping/", key, false, io.Discard, ping.WithAttempts(1))
	if err != nil {
		t.Fatal(err)
	}
	err = PingError(dead.Monitor("job").Start(ctx))
	var ee *ExitError
	var ue *url.Error
	if !errors.As(err, &ee) || ee.Code != ExitServer || !errors.As(err, &ue) {
		t.Fatalf("unreachable: %#v", err)
	}
	if strings.Contains(err.Error(), key) || !strings.HasPrefix(err.Error(), `Post "http://127.0.0.1:1/vink/ping/<ping key>/job/start"`) {
		t.Errorf("unreachable error: %v", err)
	}
	// the module's own refusals are the caller's to fix
	if err := PingError(pc.Monitor("ok").Exit(ctx, -1)); !errors.As(err, &ee) || ee.Code != ExitUser || strings.HasPrefix(err.Error(), "ping:") {
		t.Errorf("bad exit code: %v", err)
	}
	if _, err := PingClient("https://vink.example.com/", key, false, io.Discard); !errors.As(err, &ee) || ee.Code != ExitUser || !strings.Contains(err.Error(), "ending in /ping/") {
		t.Errorf("bad address: %v", err)
	}
}

func TestClientUnderAPath(t *testing.T) {
	// a context whose server URL carries the deployment's path: the client keeps it in front of /api/v1
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))
	defer srv.Close()
	for _, server := range []string{srv.URL + "/vink", srv.URL + "/vink/"} {
		c := NewClient(server, "vk_test")
		if _, err := c.DoRaw(context.Background(), "GET", "/monitors", nil, nil); err != nil {
			t.Fatalf("%s: %v", server, err)
		}
		if got != "/vink/api/v1/monitors" {
			t.Fatalf("%s: request path %q", server, got)
		}
	}
}

func TestPrinter(t *testing.T) {
	var buf bytes.Buffer
	p := &Printer{Out: &buf}
	p.Table([]string{"SLUG", "STATE"}, [][]string{{"a", p.State("up")}, {"bb", p.State("down")}})
	if !strings.Contains(buf.String(), "● up") || !strings.Contains(buf.String(), "◆ down") || strings.Contains(buf.String(), "\x1b[") {
		t.Errorf("table: %q", buf.String())
	}
	buf.Reset()
	p.JSON([]byte(`{"a":1}`))
	if buf.String() != "{\"a\":1}\n" {
		t.Errorf("json: %q", buf.String())
	}
	cp := &Printer{Out: &buf, Color: true}
	if !strings.Contains(cp.State("late"), "◐ late") {
		t.Error("coloured state keeps the word and glyph")
	}
	if p.State("weird") != "? weird" {
		t.Error("unknown state")
	}
}
