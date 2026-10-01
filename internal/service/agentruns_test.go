package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/w4jnl/vink/internal/domain"
)

func (f *fixture) remote(t *testing.T, slug, location string) *domain.Monitor {
	t.Helper()
	m, err := f.svc.CreateMonitor(context.Background(), f.member, &domain.Monitor{
		Slug: slug, Name: slug, Kind: domain.KindHTTP,
		Pull: &domain.PullSpec{Location: location, HTTP: &domain.HTTPCheck{URL: "https://example.internal/" + slug}},
	})
	if err != nil {
		t.Fatalf("create %s: %v", slug, err)
	}
	return m
}

func TestAgentAssignmentResultsAndSweep(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	orgAdmin := domain.Scope{OrgID: f.org.ID, UserID: "u3", Role: domain.RoleAdmin, Actor: "user:a"}
	dc1, _, err := f.svc.CreateAgent(ctx, orgAdmin, "dc1", map[string]string{"site": "dc1", "zone": "dmz"})
	if err != nil {
		t.Fatal(err)
	}
	dc2, _, err := f.svc.CreateAgent(ctx, orgAdmin, "dc2", map[string]string{"site": "dc2", "zone": "dmz"})
	if err != nil {
		t.Fatal(err)
	}
	online := map[string]bool{}
	f.svc.SetAgentPresence(func(id string) bool { return online[id] })

	// a location must name an agent the org has; selectors are free
	if _, err := f.svc.CreateMonitor(ctx, f.member, &domain.Monitor{Slug: "x", Kind: domain.KindHTTP, Pull: &domain.PullSpec{Location: "agent:nope", HTTP: &domain.HTTPCheck{URL: "https://example.internal"}}}); err == nil || !strings.Contains(err.Error(), "no agent named nope") {
		t.Fatalf("unknown agent: %v", err)
	}
	byName := f.remote(t, "by-name", "agent:dc1")
	byLabel := f.remote(t, "by-label", "zone=dmz")
	byLabel2 := f.remote(t, "by-label-2", "zone=dmz")
	onlyDC2 := f.remote(t, "only-dc2", "site=dc2")
	local := f.heartbeat(t, "hb", "1h", "5m")
	_ = local

	// the pool never sees remote checks
	due, err := f.svc.ListDueChecks(ctx, f.clock.Add(time.Minute), 100)
	if err != nil || len(due) != 0 {
		t.Fatalf("pool due: %v %v", due, err)
	}
	if _, ok, _ := f.svc.NextCheckDueAt(ctx); ok {
		t.Fatal("pool must not wait on remote checks")
	}

	// nobody connected: nothing assigned
	changes, err := f.svc.AssignAgents(ctx, f.org.ID)
	if err != nil || len(changes) != 0 {
		t.Fatalf("no agents: %v %v", changes, err)
	}

	// dc1 connects: takes by-name and the two dmz monitors; only-dc2 waits
	online[dc1.ID] = true
	changes, err = f.svc.AssignAgents(ctx, f.org.ID)
	if err != nil || len(changes) != 3 {
		t.Fatalf("dc1 online: %+v %v", changes, err)
	}
	mine, err := f.svc.AgentMonitors(ctx, dc1.ID)
	if err != nil || len(mine) != 3 {
		t.Fatalf("dc1 monitors: %d %v", len(mine), err)
	}

	// dc2 connects: gets only-dc2, and the dmz monitors stay with dc1 (sticky)
	online[dc2.ID] = true
	changes, err = f.svc.AssignAgents(ctx, f.org.ID)
	if err != nil || len(changes) != 1 || changes[0].MonitorID != onlyDC2.ID || changes[0].To != dc2.ID {
		t.Fatalf("dc2 online: %+v %v", changes, err)
	}

	// a new dmz monitor goes to the least loaded (dc2 holds 1, dc1 holds 3)
	byLabel3 := f.remote(t, "by-label-3", "zone=dmz")
	changes, err = f.svc.AssignAgents(ctx, f.org.ID)
	if err != nil || len(changes) != 1 || changes[0].To != dc2.ID {
		t.Fatalf("least loaded: %+v %v", changes, err)
	}

	// results: ok flips new → up; from the wrong agent they are dropped; repeats are ignored
	at := f.clock.Now()
	if err := f.svc.RecordAgentResult(ctx, dc2, AgentResult{MonitorID: byName.ID, At: at, OK: true, LatencyMs: 12}); err != nil {
		t.Fatal(err)
	}
	if m, _ := f.svc.MonitorBySlug(ctx, f.member, "by-name"); m.State != domain.StateNew {
		t.Fatalf("wrong agent must be dropped: %s", m.State)
	}
	for range 2 {
		if err := f.svc.RecordAgentResult(ctx, dc1, AgentResult{MonitorID: byName.ID, At: at, OK: true, LatencyMs: 12, Detail: map[string]any{"status": 200}}); err != nil {
			t.Fatal(err)
		}
	}
	m, _ := f.svc.MonitorBySlug(ctx, f.member, "by-name")
	if m.State != domain.StateUp || m.NextDueAt == nil || !m.NextDueAt.Equal(at.Add(time.Minute)) {
		t.Fatalf("after ok: %s next=%v", m.State, m.NextDueAt)
	}
	obs, _ := f.svc.ListObservations(ctx, f.member, "by-name", ObservationPage{Limit: 10})
	if len(obs) != 1 || obs[0].Source != "agent:dc1" || obs[0].Detail["status"] != float64(200) {
		t.Fatalf("observations: %+v", obs)
	}
	// a failure with the threshold: late, then down with the agent's reason
	for i := range 3 {
		if err := f.svc.RecordAgentResult(ctx, dc1, AgentResult{MonitorID: byName.ID, At: at.Add(time.Duration(i+1) * time.Minute), OK: false, Reason: "connection refused"}); err != nil {
			t.Fatal(err)
		}
	}
	m, _ = f.svc.MonitorBySlug(ctx, f.member, "by-name")
	if m.State != domain.StateDown {
		t.Fatalf("after failures: %s", m.State)
	}
	events, _ := f.svc.ListEvents(ctx, f.member, "by-name", 10)
	if len(events) < 2 || events[0].Reason != "connection refused" {
		t.Fatalf("events: %+v", events)
	}

	// dc1 drops: its monitors are released and dc2 picks up the dmz ones
	online[dc1.ID] = false
	if err := f.svc.AgentDisconnected(ctx, dc1.ID); err != nil {
		t.Fatal(err)
	}
	changes, err = f.svc.AssignAgents(ctx, f.org.ID)
	if err != nil || len(changes) != 2 {
		t.Fatalf("dc1 gone: %+v %v", changes, err)
	}
	for _, c := range changes {
		if c.To != dc2.ID || (c.MonitorID != byLabel.ID && c.MonitorID != byLabel2.ID) {
			t.Fatalf("dc1 gone: %+v", c)
		}
	}
	if m, _ := f.svc.MonitorBySlug(ctx, f.member, "by-name"); m.AgentID != "" {
		t.Fatalf("by-name must be unassigned, got %s", m.AgentID)
	}
	_ = byLabel3

	// the sweep: dc1 was never seen → its monitors are gone already; by-name
	// (unassigned, down) stays down; a fresh unassigned remote monitor turns
	// late with no matching agent once it is old enough
	waiting := f.remote(t, "waiting", "site=dc9")
	if n, err := f.svc.SweepOfflineAgents(ctx, f.clock.Now(), 2*time.Minute); err != nil || n != 0 {
		t.Fatalf("early sweep: %d %v", n, err)
	}
	later := f.clock.Add(3 * time.Minute)
	n, err := f.svc.SweepOfflineAgents(ctx, later, 2*time.Minute)
	if err != nil || n != 1 {
		t.Fatalf("sweep: %d %v", n, err)
	}
	m, _ = f.svc.MonitorBySlug(ctx, f.member, "waiting")
	if m.State != domain.StateLate {
		t.Fatalf("waiting: %s", m.State)
	}
	events, _ = f.svc.ListEvents(ctx, f.member, "waiting", 10)
	if len(events) != 1 || events[0].Reason != ReasonNoAgent {
		t.Fatalf("waiting events: %+v", events)
	}
	_ = waiting

	// dc2 goes quiet while still holding monitors: they turn late, never down
	if err := f.svc.TouchAgent(ctx, dc2.ID, "10.0.0.2", "0.1"); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.RecordAgentResult(ctx, dc2, AgentResult{MonitorID: onlyDC2.ID, At: later, OK: true}); err != nil {
		t.Fatal(err)
	}
	online[dc2.ID] = false
	n, err = f.svc.SweepOfflineAgents(ctx, later.Add(time.Minute), 2*time.Minute)
	if err != nil || n != 0 {
		t.Fatalf("dc2 recently seen: %d %v", n, err)
	}
	// dc2 holds only-dc2 and the three dmz monitors
	n, err = f.svc.SweepOfflineAgents(ctx, later.Add(5*time.Minute), 2*time.Minute)
	if err != nil || n != 4 {
		t.Fatalf("dc2 quiet: %d %v", n, err)
	}
	m, _ = f.svc.MonitorBySlug(ctx, f.member, "only-dc2")
	if m.State != domain.StateLate || m.AgentID != dc2.ID {
		t.Fatalf("only-dc2: %s agent=%s", m.State, m.AgentID)
	}
	events, _ = f.svc.ListEvents(ctx, f.member, "only-dc2", 10)
	if events[0].Reason != ReasonAgentOffline {
		t.Fatalf("only-dc2 events: %+v", events)
	}
	// a second sweep changes nothing more
	if n, _ := f.svc.SweepOfflineAgents(ctx, later.Add(6*time.Minute), 2*time.Minute); n != 0 {
		t.Fatalf("repeat sweep: %d", n)
	}
	// the agent comes back with an ok: late → up
	online[dc2.ID] = true
	if err := f.svc.RecordAgentResult(ctx, dc2, AgentResult{MonitorID: onlyDC2.ID, At: later.Add(7 * time.Minute), OK: true}); err != nil {
		t.Fatal(err)
	}
	if m, _ := f.svc.MonitorBySlug(ctx, f.member, "only-dc2"); m.State != domain.StateUp {
		t.Fatalf("recovered: %s", m.State)
	}

	// revoking an agent releases its monitors
	if err := f.svc.RevokeAgent(ctx, orgAdmin, "dc2"); err != nil {
		t.Fatal(err)
	}
	if m, _ := f.svc.MonitorBySlug(ctx, f.member, "only-dc2"); m.AgentID != "" {
		t.Fatalf("revoke must release, got %s", m.AgentID)
	}
	if _, err := f.svc.RecordAgentResult(ctx, dc2, AgentResult{MonitorID: onlyDC2.ID, At: later.Add(8 * time.Minute), OK: true}), error(nil); err != nil && !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("result after revoke: %v", err)
	}
}
