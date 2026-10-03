package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/w4jnl/vink/internal/checks"
	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/outbound"
)

func withChecker(t *testing.T, f *fixture) {
	t.Helper()
	reg, err := checks.NewRegistry(checks.Options{Outbound: outbound.Options{AllowPrivateTargets: true}})
	if err != nil {
		t.Fatal(err)
	}
	f.svc.SetChecker(reg)
}

func TestRunCheckDrivesPullState(t *testing.T) {
	f := newFixture(t)
	withChecker(t, f)
	ctx := context.Background()
	var status atomic.Int32
	status.Store(200)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(int(status.Load())) }))
	defer srv.Close()
	m, err := f.svc.CreateMonitor(ctx, f.member, &domain.Monitor{Slug: "web", Name: "Web", Kind: domain.KindHTTP, Tags: []string{"prod"}, Pull: &domain.PullSpec{
		Interval: domain.MustDuration("10s"), Timeout: domain.MustDuration("2s"), FailureThreshold: 2,
		Confirm: domain.Confirm{Retries: 1, Delay: domain.MustDuration("10ms")}, HTTP: &domain.HTTPCheck{URL: srv.URL},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if m.State != domain.StateNew || m.NextDueAt == nil || !m.NextDueAt.Equal(start) || m.Pull.Target() != srv.URL {
		t.Fatalf("created: %+v", m)
	}
	if ids, _ := f.svc.ListDue(ctx, start, 10); len(ids) != 0 {
		t.Fatal("the heartbeat scheduler must not see pull monitors")
	}
	if ids, _ := f.svc.ListDueChecks(ctx, start, 10); len(ids) != 1 || ids[0] != m.ID {
		t.Fatalf("due checks: %v", ids)
	}
	if err := f.svc.RunCheck(ctx, m.ID, f.clock.Now()); err != nil {
		t.Fatal(err)
	}
	m, _ = f.svc.MonitorBySlug(ctx, f.member, "web")
	if m.State != domain.StateUp || m.NextDueAt == nil || !m.NextDueAt.Equal(start.Add(10*time.Second)) || m.LastOkAt == nil {
		t.Fatalf("after first ok: %+v", m)
	}
	obs, _ := f.svc.ListObservations(ctx, f.member, "web", HistoryPage{Limit: 10})
	if len(obs) != 1 || !obs[0].OK || obs[0].LatencyMs == nil || obs[0].Source != "local" || obs[0].Detail["status"] != float64(200) {
		t.Fatalf("observation: %+v", obs)
	}
	if ids, _ := f.svc.ListDueChecks(ctx, start, 10); len(ids) != 0 {
		t.Fatal("not due again yet")
	}

	// failing: the confirm retry runs, the monitor turns late, then down
	status.Store(503)
	f.clock.Set(f.clock.Now().Add(10 * time.Second))
	if err := f.svc.RunCheck(ctx, m.ID, f.clock.Now()); err != nil {
		t.Fatal(err)
	}
	m, _ = f.svc.MonitorBySlug(ctx, f.member, "web")
	if m.State != domain.StateLate || m.FailStreak != 1 {
		t.Fatalf("after first failure: %s streak=%d", m.State, m.FailStreak)
	}
	obs, _ = f.svc.ListObservations(ctx, f.member, "web", HistoryPage{Limit: 10})
	if len(obs) != 3 || obs[0].OK || obs[0].Detail["reason"] != "503 Service Unavailable" || obs[0].Detail["attempt"] != float64(2) || obs[1].Detail["attempts"] != float64(2) {
		t.Fatalf("confirm observations: %+v %+v", obs[0].Detail, obs[1].Detail)
	}
	events, _ := f.svc.ListEvents(ctx, f.member, "web", 10)
	if events[0].To != domain.StateLate || events[0].Reason != "503 Service Unavailable" {
		t.Fatalf("late event: %+v", events[0])
	}
	f.clock.Set(f.clock.Now().Add(10 * time.Second))
	if err := f.svc.RunCheck(ctx, m.ID, f.clock.Now()); err != nil {
		t.Fatal(err)
	}
	m, _ = f.svc.MonitorBySlug(ctx, f.member, "web")
	if m.State != domain.StateDown {
		t.Fatalf("after threshold: %s", m.State)
	}
	if open, _ := f.svc.ListIncidents(ctx, f.member, true, 10, time.Time{}); len(open) != 1 || open[0].Reason != "503 Service Unavailable" {
		t.Fatalf("incident: %+v", open)
	}
	// recovery closes the incident
	status.Store(200)
	f.clock.Set(f.clock.Now().Add(10 * time.Second))
	if err := f.svc.RunCheck(ctx, m.ID, f.clock.Now()); err != nil {
		t.Fatal(err)
	}
	m, _ = f.svc.MonitorBySlug(ctx, f.member, "web")
	if m.State != domain.StateUp {
		t.Fatalf("after recovery: %s", m.State)
	}
	if open, _ := f.svc.ListIncidents(ctx, f.member, true, 10, time.Time{}); len(open) != 0 {
		t.Fatal("incident still open")
	}
	// pings are for heartbeats
	if _, err := f.svc.ResolvePing(ctx, f.project.PingKey, "web", "", false); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("ping a pull monitor: %v", err)
	}
}

func TestCheckNowRules(t *testing.T) {
	f := newFixture(t)
	withChecker(t, f)
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	defer srv.Close()
	if _, err := f.svc.CreateMonitor(ctx, f.member, &domain.Monitor{Slug: "web", Kind: domain.KindHTTP, Pull: &domain.PullSpec{HTTP: &domain.HTTPCheck{URL: srv.URL}}}); err != nil {
		t.Fatal(err)
	}
	f.heartbeat(t, "job", "1h", "5m")
	if _, err := f.svc.CheckNow(ctx, f.viewer, "web"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("viewer: %v", err)
	}
	if _, err := f.svc.CheckNow(ctx, f.member, "job"); err == nil || !strings.Contains(err.Error(), "kind") {
		t.Fatalf("heartbeat: %v", err)
	}
	m, err := f.svc.CheckNow(ctx, f.member, "web")
	if err != nil || m.State != domain.StateUp {
		t.Fatalf("check now: %v %+v", err, m)
	}
	if _, err := f.svc.PauseMonitor(ctx, f.member, "web"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.CheckNow(ctx, f.member, "web"); err == nil || !strings.Contains(err.Error(), "paused") {
		t.Fatalf("paused: %v", err)
	}
	if ids, _ := f.svc.ListDueChecks(ctx, f.clock.Now().Add(time.Hour), 10); len(ids) != 0 {
		t.Fatal("paused monitors are never due")
	}
	resumed, _ := f.svc.ResumeMonitor(ctx, f.member, "web")
	if resumed.NextDueAt == nil {
		t.Fatal("resume must make the monitor due")
	}
	// the instance floor on the interval
	f.svc.cfg.MinInterval = domain.MustDuration("30s")
	_, err = f.svc.CreateMonitor(ctx, f.member, &domain.Monitor{Slug: "fast", Kind: domain.KindTCP, Pull: &domain.PullSpec{Interval: domain.MustDuration("20s"), Timeout: domain.MustDuration("5s"), TCP: &domain.TCPCheck{Host: "db", Port: 5432}}})
	if err == nil || !strings.Contains(err.Error(), "on this instance") {
		t.Fatalf("min interval: %v", err)
	}
}
