package cli

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
		case "/ping/k/s":
			w.WriteHeader(200)
		case "/ping/k/missing":
			w.WriteHeader(404)
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
	if err := c.Ping(context.Background(), srv.URL+"/ping/k/s", "k", []byte("hi"), ""); err != nil {
		t.Fatalf("ping: %v", err)
	}
	if err := c.Ping(context.Background(), srv.URL+"/ping/k/missing", "k", nil, ""); !errors.As(err, &ee) || ee.Code != ExitUser {
		t.Fatalf("ping 404: %v", err)
	}
}

// TestPingHidesTheKey: the ping key never reaches a debug line or an
// error, which end up in cron mail and pasted logs.
func TestPingHidesTheKey(t *testing.T) {
	const key = "s3cretpingkey"
	var log bytes.Buffer
	dead := NewClient("", "")
	dead.Debug, dead.Log = true, &log
	err := dead.Ping(context.Background(), "http://127.0.0.1:1/vink/ping/"+key+"/job/start", key, nil, "")
	var ee *ExitError
	if err == nil || !errors.As(err, &ee) || ee.Code != ExitServer {
		t.Fatalf("unreachable: %v", err)
	}
	if strings.Contains(err.Error(), key) || !strings.Contains(err.Error(), "/vink/ping/<ping key>/job/start") {
		t.Errorf("error shows the key: %v", err)
	}
	if strings.Contains(log.String(), key) || !strings.Contains(log.String(), "> POST http://127.0.0.1:1/vink/ping/<ping key>/job/start") {
		t.Errorf("debug line shows the key: %q", log.String())
	}
	var ue *url.Error
	if !errors.As(err, &ue) {
		t.Errorf("the transport error must stay in the chain: %#v", err)
	}
	if err := dead.Ping(context.Background(), "http://bad host/ping/"+key+"/job", key, nil, ""); err == nil || strings.Contains(err.Error(), key) {
		t.Errorf("bad URL error shows the key: %v", err)
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
