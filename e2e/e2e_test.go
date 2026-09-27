//go:build e2e

// Package e2e is the heartbeat smoke test from the design document: it
// builds the binary, starts it against a temp database, creates a
// heartbeat with a one-minute period and grace, pings it, waits past the
// deadline and asserts late, then down with a webhook delivery, then up
// again. Run with `make e2e`; it takes about three minutes.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type hook struct {
	mu     sync.Mutex
	events []map[string]any
}

func (h *hook) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	h.mu.Lock()
	h.events = append(h.events, m)
	h.mu.Unlock()
	w.WriteHeader(200)
}

func (h *hook) kinds() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, 0, len(h.events))
	for _, e := range h.events {
		k, _ := e["event"].(string)
		out = append(out, k)
	}
	return out
}

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func TestHeartbeatSmoke(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "vink")
	build := exec.Command("go", "build", "-o", bin, "./cmd/vink")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	dbPath := filepath.Join(dir, "vink.db")
	addr := freePort(t)
	base := "http://" + addr
	env := append(os.Environ(), "VINK_DB_PATH="+dbPath, "VINK_SERVER_LISTEN="+addr, "VINK_SERVER_BASE_URL="+base, "VINK_CONFIG="+filepath.Join(dir, "cli.toml"), "NO_COLOR=1")

	vink := func(stdin string, args ...string) (string, error) {
		cmd := exec.Command(bin, args...)
		cmd.Env = env
		cmd.Stdin = strings.NewReader(stdin)
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		err := cmd.Run()
		if err != nil {
			return out.String(), &cmdErr{err: err, stderr: errb.String()}
		}
		return out.String(), nil
	}

	initOut, err := vink("e2e-password\n", "admin", "init", "--org", "homelab", "--user", "j", "--password-stdin", "--json")
	if err != nil {
		t.Fatalf("admin init: %v", err)
	}
	var init map[string]string
	if err := json.Unmarshal([]byte(initOut), &init); err != nil {
		t.Fatalf("init json: %s", initOut)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serve := exec.CommandContext(ctx, bin, "serve")
	serve.Env = env
	var serveLog bytes.Buffer
	serve.Stdout, serve.Stderr = &serveLog, &serveLog
	if err := serve.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cancel()
		_ = serve.Wait()
		if t.Failed() {
			t.Logf("server log:\n%s", serveLog.String())
		}
	}()
	waitFor(t, 10*time.Second, func() bool {
		resp, err := http.Get(base + "/readyz")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		return resp.StatusCode == 200
	})

	receiver := &hook{}
	hookSrv := httptest.NewServer(receiver)
	defer hookSrv.Close()

	if _, err := vink("", "ctx", "add", "e2e", "--server", base, "--key", init["api_key"]); err != nil {
		t.Fatalf("ctx add: %v", err)
	}
	api := func(method, path string, body any) map[string]any {
		t.Helper()
		var rdr io.Reader
		if body != nil {
			raw, _ := json.Marshal(body)
			rdr = bytes.NewReader(raw)
		}
		req, _ := http.NewRequest(method, base+"/api/v1"+path, rdr)
		req.Header.Set("Authorization", "Bearer "+init["api_key"])
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode >= 300 {
			t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, raw)
		}
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		return m
	}
	api("POST", "/channels", map[string]any{"name": "hook", "kind": "webhook", "config": map[string]any{"url": hookSrv.URL}})
	api("POST", "/monitors", map[string]any{"slug": "smoke", "name": "Smoke", "schedule": map[string]string{"period": "60s"}, "grace": "60s"})

	start := time.Now()
	if out, err := vink("", "ping", "smoke"); err != nil || !strings.Contains(out, "ok") {
		t.Fatalf("ping: %s %v", out, err)
	}
	state := func() string {
		m := api("GET", "/monitors/smoke", nil)
		s, _ := m["state"].(string)
		return s
	}
	if s := state(); s != "up" {
		t.Fatalf("after ping: %s", s)
	}
	t.Logf("up after %s; waiting for late (period 60s)", time.Since(start).Round(time.Second))
	waitFor(t, 90*time.Second, func() bool { return state() == "late" })
	t.Logf("late after %s; waiting for down (grace 60s) and the webhook", time.Since(start).Round(time.Second))
	waitFor(t, 90*time.Second, func() bool { return state() == "down" })
	waitFor(t, 15*time.Second, func() bool { return len(receiver.kinds()) >= 1 })
	if kinds := receiver.kinds(); kinds[0] != "down" {
		t.Fatalf("first webhook: %v", kinds)
	}
	out, err := vink("", "status")
	if err == nil || !strings.Contains(err.Error(), "exit status 3") || !strings.Contains(out, "down 1") {
		t.Fatalf("status while down: %s %v", out, err)
	}
	if _, err := vink("", "ping", "smoke"); err != nil {
		t.Fatalf("recovery ping: %v", err)
	}
	if s := state(); s != "up" {
		t.Fatalf("after recovery ping: %s", s)
	}
	waitFor(t, 15*time.Second, func() bool { return len(receiver.kinds()) >= 2 })
	if kinds := receiver.kinds(); kinds[1] != "up" {
		t.Fatalf("second webhook: %v", kinds)
	}
	if _, err := vink("", "status"); err != nil {
		t.Fatalf("status after recovery: %v", err)
	}
	t.Logf("done in %s", time.Since(start).Round(time.Second))
}

type cmdErr struct {
	err    error
	stderr string
}

func (e *cmdErr) Error() string { return e.err.Error() + ": " + strings.TrimSpace(e.stderr) }

func waitFor(t *testing.T, max time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(max)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("condition not met within %s", max)
}
