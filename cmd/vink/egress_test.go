package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestNoEgress runs the whole server with the egress log on, exercises it
// as an operator and a client would, lets every loop run, and checks that
// nothing left the process. A local http check afterwards proves the log
// sees connections when they happen.
func TestNoEgress(t *testing.T) {
	dir := t.TempDir()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	egress := filepath.Join(dir, "egress.log")
	cfgPath := filepath.Join(dir, "vink.toml")
	cfg := fmt.Sprintf(`[server]
listen = %q
base_url = "http://%s"
[db]
path = %q
[outbound]
allow_private_targets = true
egress_log = %q
[metrics]
token = "metrics-token"
[secrets]
key_file = %q
[log]
level = "error"
`, addr, addr, filepath.Join(dir, "vink.db"), egress, filepath.Join(dir, "secret.key"))
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	runCLI := func(stdin string, args ...string) (string, string, int) {
		var out, errb bytes.Buffer
		code := run(context.Background(), append([]string{"--color", "never"}, args...), strings.NewReader(stdin), &out, &errb)
		return out.String(), errb.String(), code
	}
	out, errs, code := runCLI("hunter2hunter2\n", "admin", "init", "--config", cfgPath, "--org", "homelab", "--user", "j", "--password-stdin", "--json")
	if code != 0 {
		t.Fatalf("init: %s", errs)
	}
	var boot map[string]string
	if err := json.Unmarshal([]byte(out), &boot); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 1)
	var serveErr bytes.Buffer
	go func() {
		done <- run(ctx, []string{"--color", "never", "serve", "--config", cfgPath}, strings.NewReader(""), io.Discard, &serveErr)
	}()
	base := "http://" + addr
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	call := func(method, path, token string, body string) (int, string) {
		t.Helper()
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		req, _ := http.NewRequestWithContext(ctx, method, base+path, rd)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := client.Do(req)
		if err != nil {
			return 0, err.Error()
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	ready := false
	for i := 0; i < 150 && !ready; i++ {
		if code, _ := call("GET", "/readyz", "", ""); code == 200 {
			ready = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !ready {
		cancel()
		t.Fatalf("server not ready: %s", serveErr.String())
	}

	// an operator's and a client's traffic
	for _, c := range []struct {
		method, path, token, body string
		want                      int
	}{
		{"GET", "/healthz", "", "", 200},
		{"GET", "/", "", "", 303},
		{"GET", "/login", "", "", 200},
		{"GET", "/api/v1/me", boot["api_key"], "", 200},
		{"GET", "/api/v1/me", "", "", 401},
		{"GET", "/metrics", "metrics-token", "", 200},
		{"GET", "/s/nope", "", "", 404},
		{"POST", "/api/v1/monitors", boot["api_key"], `{"slug":"nightly","kind":"heartbeat","schedule":{"period":"1h"},"grace":"5m"}`, 201},
		{"GET", "/ping/" + boot["ping_key"] + "/nightly", "", "", 200},
		{"GET", "/api/v1/monitors/nightly", boot["api_key"], "", 200},
		{"GET", "/api/v1/openapi.yaml", "", "", 200},
	} {
		if code, body := call(c.method, c.path, c.token, c.body); code != c.want {
			t.Errorf("%s %s: %d %s", c.method, c.path, code, body)
		}
	}
	// every loop gets a few turns: scheduler, checks, dispatcher, retention, agents
	time.Sleep(2500 * time.Millisecond)
	if b, err := os.ReadFile(egress); err == nil && len(bytes.TrimSpace(b)) > 0 {
		t.Fatalf("the server opened connections on its own:\n%s", b)
	}

	// the control: a local http check is one dial, and the log says so
	hits := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits++; _, _ = io.WriteString(w, "ok") }))
	defer target.Close()
	if code, body := call("POST", "/api/v1/monitors", boot["api_key"], `{"slug":"local","kind":"http","interval":"60s","http":{"url":"`+target.URL+`"}}`); code != 201 {
		t.Fatalf("create http monitor: %d %s", code, body)
	}
	if code, body := call("POST", "/api/v1/monitors/local/check", boot["api_key"], ""); code != 200 {
		t.Fatalf("check: %d %s", code, body)
	}
	b, err := os.ReadFile(egress)
	if err != nil {
		t.Fatalf("egress log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if hits == 0 || len(lines) != 1 || !strings.Contains(lines[0], " dial tcp "+strings.TrimPrefix(target.URL, "http://")) {
		t.Fatalf("control: hits=%d log=%q", hits, string(b))
	}

	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("serve exit %d: %s", code, serveErr.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatal("serve did not stop")
	}
}
