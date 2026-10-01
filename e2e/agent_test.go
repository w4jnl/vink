//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestAgentSmoke is the phase 2 gate: a probe agent on the "other side"
// runs a check the server never touches, its results drive the monitor,
// losing the agent turns the monitor late with reason agent offline and
// never down, and the server's egress log stays empty throughout.
func TestAgentSmoke(t *testing.T) {
	var healthy atomic.Bool
	healthy.Store(true)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !healthy.Load() {
			w.WriteHeader(503)
			return
		}
		fmt.Fprint(w, "fine")
	}))
	defer target.Close()

	egress := filepath.Join(t.TempDir(), "egress.log")
	in := startInstance(t, "VINK_AGENTS_OFFLINE_AFTER=5s", "VINK_OUTBOUND_EGRESS_LOG="+egress)

	// the agent is registered on the server host, with the DB file
	out, err := in.vink("", "admin", "agent", "add", "--org", "homelab", "dc1", "--labels", "site=dc1", "--json")
	if err != nil {
		t.Fatalf("agent add: %v", err)
	}
	var added struct{ Token, Command string }
	if err := json.Unmarshal([]byte(out), &added); err != nil || !strings.HasPrefix(added.Token, "vat_") || !strings.Contains(added.Command, "vink agent --server ws://") {
		t.Fatalf("agent add output: %s", out)
	}
	startAgent := func() (context.CancelFunc, *bytes.Buffer) {
		ctx, cancel := context.WithCancel(context.Background())
		cmd := exec.CommandContext(ctx, in.bin, "agent", "--server", in.base, "--token-file", writeFile(t, added.Token))
		cmd.Env = append(in.env, "VINK_LOG_FORMAT=text")
		var log bytes.Buffer
		cmd.Stdout, cmd.Stderr = &log, &log
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		return func() { cancel(); _ = cmd.Wait() }, &log
	}
	stop, agentLog := startAgent()
	defer func() { stop() }()

	// a monitor that runs on the agent, through the apply file
	file := fmt.Sprintf(`version: 1
monitors:
  - slug: intranet
    name: Intranet
    kind: http
    interval: 30s
    failure_threshold: 1
    confirm: {retries: 1, delay: 2s}
    location: agent:dc1
    http: {url: %q, expect_body: {contains: fine}}
`, target.URL)
	if _, err := in.vink("", "apply", "-f", writeFile(t, file)); err != nil {
		t.Fatalf("apply: %v", err)
	}
	t.Log("waiting for the agent to run the check")
	waitFor(t, 45*time.Second, func() bool { return in.state("intranet") == "up" })
	obs := in.api("GET", "/monitors/intranet/observations?limit=1", nil)
	items, _ := obs["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("observations: %v", obs)
	}
	if first, _ := items[0].(map[string]any); first["source"] != "agent:dc1" {
		t.Fatalf("the check must come from the agent: %v\nagent log:\n%s", items[0], agentLog.String())
	}
	agents, err := in.vink("", "admin", "agent", "ls", "--org", "homelab")
	if err != nil || !strings.Contains(agents, "dc1") || !strings.Contains(agents, "site=dc1") || strings.Contains(agents, "never") {
		t.Fatalf("agent ls: %v\n%s", err, agents)
	}

	// the target fails: the agent's failure takes the monitor down, with its reason
	healthy.Store(false)
	t.Log("target broken; waiting for down")
	waitFor(t, 60*time.Second, func() bool { return in.state("intranet") == "down" })
	events := in.api("GET", "/monitors/intranet/events?limit=1", nil)
	if ev, _ := events["items"].([]any); len(ev) != 1 || !strings.Contains(fmt.Sprint(ev[0].(map[string]any)["reason"]), "503") {
		t.Fatalf("down event: %v", events)
	}
	healthy.Store(true)
	waitFor(t, 60*time.Second, func() bool { return in.state("intranet") == "up" })

	// the agent goes away: late with reason agent offline, never down
	stop()
	t.Log("agent stopped; waiting for late (offline after 5 s, sweep every 30 s)")
	waitFor(t, 60*time.Second, func() bool { return in.state("intranet") == "late" })
	events = in.api("GET", "/monitors/intranet/events?limit=1", nil)
	if ev, _ := events["items"].([]any); len(ev) != 1 || ev[0].(map[string]any)["reason"] != "agent offline" {
		t.Fatalf("late event: %v", events)
	}
	time.Sleep(3 * time.Second)
	if s := in.state("intranet"); s != "late" {
		t.Fatalf("a missing agent must not take the monitor down, got %s", s)
	}

	// it comes back: the check runs again and the monitor recovers
	stop, _ = startAgent()
	t.Log("agent restarted; waiting for up")
	waitFor(t, 45*time.Second, func() bool { return in.state("intranet") == "up" })

	// the server itself never opened a connection
	if b, err := os.ReadFile(egress); err == nil && len(bytes.TrimSpace(b)) > 0 {
		t.Fatalf("the server opened connections of its own:\n%s", b)
	}
	t.Logf("agent smoke done")
}

func writeFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
