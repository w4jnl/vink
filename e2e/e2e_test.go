//go:build e2e

// Package e2e runs the binary against a temp database. The heartbeat
// smoke test is the phase 0 gate: a heartbeat with a one-minute period
// and grace goes late, then down with a webhook delivery, then up again.
// The homelab test is the phase 1 gate, the agent test the phase 2 gate
// and the onboarding test the phase 3 gate. Run with `make e2e`; together
// they take about six minutes.
package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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

// kinds lists "event monitor" per delivery, in order.
func (h *hook) kinds() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, 0, len(h.events))
	for _, e := range h.events {
		k, _ := e["event"].(string)
		slug := ""
		if m, ok := e["monitor"].(map[string]any); ok {
			slug, _ = m["slug"].(string)
		}
		out = append(out, k+" "+slug)
	}
	return out
}

func TestHeartbeatSmoke(t *testing.T) {
	in := startInstance(t)
	receiver := &hook{}
	hookSrv := httptest.NewServer(receiver)
	defer hookSrv.Close()
	in.api("POST", "/channels", map[string]any{"name": "hook", "kind": "webhook", "config": map[string]any{"url": hookSrv.URL}})
	in.api("POST", "/monitors", map[string]any{"slug": "smoke", "name": "Smoke", "schedule": map[string]string{"period": "60s"}, "grace": "60s"})

	start := time.Now()
	if out, err := in.vink("", "ping", "smoke"); err != nil || !strings.Contains(out, "ok") {
		t.Fatalf("ping: %s %v", out, err)
	}
	if s := in.state("smoke"); s != "up" {
		t.Fatalf("after ping: %s", s)
	}
	t.Logf("up after %s; waiting for late (period 60s)", time.Since(start).Round(time.Second))
	waitFor(t, 90*time.Second, func() bool { return in.state("smoke") == "late" })
	t.Logf("late after %s; waiting for down (grace 60s) and the webhook", time.Since(start).Round(time.Second))
	waitFor(t, 90*time.Second, func() bool { return in.state("smoke") == "down" })
	waitFor(t, 15*time.Second, func() bool { return len(receiver.kinds()) >= 1 })
	if kinds := receiver.kinds(); kinds[0] != "down smoke" {
		t.Fatalf("first webhook: %v", kinds)
	}
	out, err := in.vink("", "status")
	if err == nil || !strings.Contains(err.Error(), "exit status 3") || !strings.Contains(out, "down 1") {
		t.Fatalf("status while down: %s %v", out, err)
	}
	if _, err := in.vink("", "ping", "smoke"); err != nil {
		t.Fatalf("recovery ping: %v", err)
	}
	if s := in.state("smoke"); s != "up" {
		t.Fatalf("after recovery ping: %s", s)
	}
	waitFor(t, 15*time.Second, func() bool { return len(receiver.kinds()) >= 2 })
	if kinds := receiver.kinds(); kinds[1] != "up smoke" {
		t.Fatalf("second webhook: %v", kinds)
	}
	if _, err := in.vink("", "status"); err != nil {
		t.Fatalf("status after recovery: %v", err)
	}
	t.Logf("done in %s", time.Since(start).Round(time.Second))
}
