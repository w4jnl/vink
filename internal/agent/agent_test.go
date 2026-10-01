package agent

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/w4jnl/vink/internal/db/dbtest"
	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/http/agentgw"
	"github.com/w4jnl/vink/internal/service"
)

func TestSocketURLAndPin(t *testing.T) {
	for in, want := range map[string]string{
		"wss://vink.example.com":            "wss://vink.example.com/agent/v1",
		"https://vink.example.com/":         "wss://vink.example.com/agent/v1",
		"http://localhost:8090":             "ws://localhost:8090/agent/v1",
		"ws://localhost:8090/agent/v1":      "ws://localhost:8090/agent/v1",
		"https://vink.example.com/vink?x=1": "wss://vink.example.com/vink/agent/v1",
	} {
		got, err := socketURL(in)
		if err != nil || got != want {
			t.Errorf("%s: %s %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "ftp://x", "https://", "vink.example.com"} {
		if _, err := socketURL(bad); err == nil {
			t.Errorf("%q must fail", bad)
		}
	}
	hexPin := strings.Repeat("ab", 32)
	for _, ok := range []string{hexPin, "sha256:" + hexPin, strings.ToUpper(hexPin), "AB:" + strings.Repeat("ab:", 30) + "ab"} {
		if p, err := parsePin(ok); err != nil || len(p) != 32 {
			t.Errorf("%s: %v", ok, err)
		}
	}
	if _, err := parsePin("abcd"); err == nil {
		t.Error("short pin must fail")
	}
	if p, err := parsePin(""); err != nil || p != nil {
		t.Error("empty pin is no pin")
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestAgentAgainstGateway(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := service.New(dbtest.Open(t), nil, quiet, service.DefaultConfig())
	root := domain.Scope{InstanceAdmin: true, Role: domain.RoleOwner, Actor: "test"}
	org, err := svc.CreateOrg(ctx, root, "homelab", "Homelab")
	if err != nil {
		t.Fatal(err)
	}
	project, err := svc.CreateProject(ctx, root, org.ID, "prod", "Prod", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	member := domain.Scope{OrgID: org.ID, ProjectID: project.ID, UserID: "u1", Role: domain.RoleMember, Actor: "user:j"}
	admin := domain.Scope{OrgID: org.ID, UserID: "u2", Role: domain.RoleAdmin, Actor: "user:a"}
	gw := agentgw.New(svc, quiet)
	gw.Sweep = 50 * time.Millisecond
	mux := http.NewServeMux()
	gw.Mount(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	go func() { _ = gw.Run(ctx) }()

	// the target the agent probes, with a switch to make it fail
	var fail atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if fail.Load() {
			http.Error(w, "nope", http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, "fine")
	}))
	defer target.Close()

	agentRow, token, err := svc.CreateAgent(ctx, admin, "dc1", nil)
	if err != nil {
		t.Fatal(err)
	}
	m, err := svc.CreateMonitor(ctx, member, &domain.Monitor{Slug: "web", Kind: domain.KindHTTP, Pull: &domain.PullSpec{
		Location: "site=dc1", Interval: domain.MustDuration("30s"), FailureThreshold: 1,
		Confirm: domain.Confirm{Retries: 1, Delay: domain.Duration(10 * time.Millisecond)},
		HTTP:    &domain.HTTPCheck{URL: target.URL, ExpectBody: &domain.ExpectBody{Contains: "fine"}},
	}})
	if err != nil {
		t.Fatal(err)
	}

	a, err := New(Options{Server: srv.URL, Token: token, Labels: map[string]string{"site": "dc1"}, Version: "test", Log: quiet, MinBackoff: 50 * time.Millisecond, MaxBackoff: 200 * time.Millisecond, Heartbeat: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	agentCtx, stopAgent := context.WithCancel(ctx)
	defer stopAgent()
	done := make(chan error, 1)
	go func() { done <- a.Run(agentCtx) }()

	waitFor(t, "connected", func() bool { return a.Connected() && gw.Connected(agentRow.ID) })
	waitFor(t, "assigned", func() bool { return len(a.Assigned()) == 1 })
	waitFor(t, "up", func() bool {
		cur, _ := svc.MonitorBySlug(ctx, member, "web")
		return cur != nil && cur.State == domain.StateUp
	})
	obs, _ := svc.ListObservations(ctx, member, "web", service.ObservationPage{Limit: 5})
	if len(obs) == 0 || obs[0].Source != "agent:dc1" || obs[0].LatencyMs == nil || obs[0].Detail["status"] != float64(200) {
		t.Fatalf("observation: %+v", obs)
	}
	got, _ := svc.Agent(ctx, admin, "dc1")
	if got.Labels["site"] != "dc1" || got.Version != "test" || got.LastSeenAt == nil {
		t.Fatalf("agent after hello: %+v", got)
	}

	// a failing target: the edit re-sends the spec with a short interval so
	// the next attempt is near; the confirm retry runs, then down
	fail.Store(true)
	if _, err := svc.UpdateMonitor(ctx, member, "web", &domain.Monitor{Name: "Web", Pull: &domain.PullSpec{
		Location: "site=dc1", Interval: domain.MustDuration("30s"), FailureThreshold: 1,
		Confirm: domain.Confirm{Retries: 1, Delay: domain.Duration(10 * time.Millisecond)},
		HTTP:    &domain.HTTPCheck{URL: target.URL + "/again", ExpectBody: &domain.ExpectBody{Contains: "fine"}},
	}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "down", func() bool {
		cur, _ := svc.MonitorBySlug(ctx, member, "web")
		return cur != nil && cur.State == domain.StateDown
	})
	obs, _ = svc.ListObservations(ctx, member, "web", service.ObservationPage{Limit: 5})
	if obs[0].OK || obs[0].Detail["reason"] == nil || obs[0].Detail["attempts"] != float64(2) {
		t.Fatalf("failed observation: %+v", obs[0])
	}

	// moving the check to this server revokes it on the agent
	if _, err := svc.UpdateMonitor(ctx, member, "web", &domain.Monitor{Name: "Web", Pull: &domain.PullSpec{HTTP: &domain.HTTPCheck{URL: target.URL}}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "revoked", func() bool { return len(a.Assigned()) == 0 })

	// a second agent with the same token bumps this one; it reconnects
	// and takes the monitor back once it is remote again
	_ = m
	if _, err := svc.UpdateMonitor(ctx, member, "web", &domain.Monitor{Name: "Web", Pull: &domain.PullSpec{Location: "agent:dc1", HTTP: &domain.HTTPCheck{URL: target.URL}}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "assigned again", func() bool { return len(a.Assigned()) == 1 })
	other, err := New(Options{Server: srv.URL, Token: token, Version: "other", Log: quiet, MinBackoff: 50 * time.Millisecond, Heartbeat: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	otherCtx, stopOther := context.WithCancel(ctx)
	go func() { _ = other.Run(otherCtx) }()
	waitFor(t, "other connected", func() bool { return other.Connected() })
	stopOther()
	waitFor(t, "first back with the monitor", func() bool {
		cur, _ := svc.Agent(ctx, admin, "dc1")
		return a.Connected() && len(a.Assigned()) == 1 && cur.Version == "test"
	})

	stopAgent()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("agent did not stop")
	}
	waitFor(t, "released", func() bool {
		cur, _ := svc.MonitorBySlug(ctx, member, "web")
		return !gw.Connected(agentRow.ID) && cur.AgentID == ""
	})
}
