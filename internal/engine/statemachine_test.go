package engine

import (
	"testing"
	"time"

	"github.com/w4jnl/vink/internal/domain"
)

var t0 = time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

func hb(period, grace string) *domain.Monitor {
	return &domain.Monitor{
		ID: "m", Slug: "job", Kind: domain.KindHeartbeat, State: domain.StateNew, StateSince: t0, BaseAt: t0,
		Heartbeat: &domain.HeartbeatSpec{
			Schedule: domain.Schedule{Period: domain.MustDuration(period)}, Grace: domain.MustDuration(grace),
			FailureThreshold: 1, RecoveryThreshold: 1,
		},
	}
}

func ping(sig domain.Signal, ok bool, at time.Time) *domain.Observation {
	return &domain.Observation{Signal: sig, OK: ok, At: at, Source: "ping"}
}

func applyTo(t *testing.T, m *domain.Monitor, d Decision) {
	t.Helper()
	if d.Changed {
		m.State = d.To
		m.StateSince = t0 // tests set explicitly where it matters
	}
	m.BaseAt, m.LastOkAt, m.FailStreak, m.OkStreak, m.RunStartedAt, m.RunID, m.NextDueAt = d.BaseAt, d.LastOkAt, d.FailStreak, d.OkStreak, d.RunStartedAt, d.RunID, d.NextDueAt
}

func TestNewMonitorHasDeadlineFromCreation(t *testing.T) {
	m := hb("1h", "5m")
	d, err := Apply(m, nil, t0, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if d.Changed || d.NextDueAt == nil || !d.NextDueAt.Equal(t0.Add(time.Hour)) {
		t.Fatalf("new tick: changed=%v next=%v", d.Changed, d.NextDueAt)
	}
	// deadline passes without a ping
	d, _ = Apply(m, nil, t0.Add(time.Hour), time.UTC)
	if !d.Changed || d.To != domain.StateLate || d.Reason != "deadline passed" {
		t.Fatalf("expected new->late, got %+v", d)
	}
	if !d.NextDueAt.Equal(t0.Add(65 * time.Minute)) {
		t.Fatalf("late wake = %v", d.NextDueAt)
	}
}

func TestFullHeartbeatLifecycle(t *testing.T) {
	m := hb("1h", "5m")
	// first ok: new -> up
	d, _ := Apply(m, ping(domain.SignalOK, true, t0), t0, time.UTC)
	if !d.Changed || d.To != domain.StateUp || d.Reason != "first ok" {
		t.Fatalf("first ok: %+v", d)
	}
	if !d.NextDueAt.Equal(t0.Add(time.Hour)) || !d.ExpectedAt.Equal(t0.Add(time.Hour)) {
		t.Fatalf("next due after ok = %v", d.NextDueAt)
	}
	applyTo(t, m, d)
	// on-time ping keeps up and moves the deadline
	at := t0.Add(50 * time.Minute)
	d, _ = Apply(m, ping(domain.SignalOK, true, at), at, time.UTC)
	if d.Changed || !d.NextDueAt.Equal(at.Add(time.Hour)) {
		t.Fatalf("on-time ping: %+v", d)
	}
	applyTo(t, m, d)
	// tick before deadline: nothing
	d, _ = Apply(m, nil, at.Add(59*time.Minute), time.UTC)
	if d.Changed {
		t.Fatal("early tick must not flip")
	}
	// deadline: up -> late
	d, _ = Apply(m, nil, at.Add(time.Hour), time.UTC)
	if !d.Changed || d.To != domain.StateLate {
		t.Fatalf("deadline: %+v", d)
	}
	applyTo(t, m, d)
	// still late inside grace
	d, _ = Apply(m, nil, at.Add(time.Hour+2*time.Minute), time.UTC)
	if d.Changed {
		t.Fatal("inside grace must not flip")
	}
	// grace over: late -> down
	d, _ = Apply(m, nil, at.Add(65*time.Minute), time.UTC)
	if !d.Changed || d.To != domain.StateDown || d.Reason != "grace over" || d.NextDueAt != nil {
		t.Fatalf("grace over: %+v", d)
	}
	applyTo(t, m, d)
	// down stays down on ticks
	d, _ = Apply(m, nil, at.Add(5*time.Hour), time.UTC)
	if d.Changed || d.NextDueAt != nil {
		t.Fatalf("down tick: %+v", d)
	}
	// ok: down -> up
	rec := at.Add(6 * time.Hour)
	d, _ = Apply(m, ping(domain.SignalOK, true, rec), rec, time.UTC)
	if !d.Changed || d.To != domain.StateUp || d.Reason != "recovered" || !d.NextDueAt.Equal(rec.Add(time.Hour)) {
		t.Fatalf("recovery: %+v", d)
	}
}

func TestLateThenOkReturnsUp(t *testing.T) {
	m := hb("1h", "5m")
	m.State = domain.StateLate
	ok := t0.Add(time.Hour)
	m.LastOkAt = &t0
	m.BaseAt = t0
	d, _ := Apply(m, ping(domain.SignalOK, true, ok.Add(2*time.Minute)), ok.Add(2*time.Minute), time.UTC)
	if !d.Changed || d.To != domain.StateUp || d.Reason != "ok" {
		t.Fatalf("late->up: %+v", d)
	}
}

func TestCatchUpGoesStraightToDown(t *testing.T) {
	m := hb("1h", "5m")
	m.State = domain.StateUp
	m.LastOkAt = &t0
	d, _ := Apply(m, nil, t0.Add(3*time.Hour), time.UTC)
	if !d.Changed || d.To != domain.StateDown || d.Reason != "grace over" {
		t.Fatalf("catch-up: %+v", d)
	}
}

func TestFailSignalsAndThresholds(t *testing.T) {
	m := hb("1h", "5m")
	m.State = domain.StateUp
	m.LastOkAt = &t0
	m.Heartbeat.FailureThreshold = 2
	d, _ := Apply(m, ping(domain.SignalFail, false, t0.Add(time.Minute)), t0.Add(time.Minute), time.UTC)
	if d.Changed || d.FailStreak != 1 {
		t.Fatalf("first fail below threshold: %+v", d)
	}
	applyTo(t, m, d)
	code := int64(3)
	exit := &domain.Observation{Signal: domain.SignalExit, OK: false, ExitCode: &code, At: t0.Add(2 * time.Minute)}
	d, _ = Apply(m, exit, t0.Add(2*time.Minute), time.UTC)
	if !d.Changed || d.To != domain.StateDown || d.Reason != "exit 3" || d.NextDueAt != nil {
		t.Fatalf("second fail: %+v", d)
	}
	applyTo(t, m, d)
	// recovery threshold 2: first ok stays down
	m.Heartbeat.RecoveryThreshold = 2
	d, _ = Apply(m, ping(domain.SignalOK, true, t0.Add(3*time.Minute)), t0.Add(3*time.Minute), time.UTC)
	if d.Changed || d.OkStreak != 1 || d.FailStreak != 0 {
		t.Fatalf("first ok under recovery threshold: %+v", d)
	}
	applyTo(t, m, d)
	d, _ = Apply(m, ping(domain.SignalOK, true, t0.Add(4*time.Minute)), t0.Add(4*time.Minute), time.UTC)
	if !d.Changed || d.To != domain.StateUp {
		t.Fatalf("second ok: %+v", d)
	}
}

func TestFailFromNewGoesDown(t *testing.T) {
	m := hb("1h", "5m")
	d, _ := Apply(m, ping(domain.SignalFail, false, t0), t0, time.UTC)
	if !d.Changed || d.To != domain.StateDown || d.Reason != "fail signal" {
		t.Fatalf("new + fail: %+v", d)
	}
}

func TestStartLogAndRunDuration(t *testing.T) {
	m := hb("1h", "5m")
	m.State = domain.StateUp
	m.LastOkAt = &t0
	start := &domain.Observation{Signal: domain.SignalStart, At: t0.Add(time.Minute), RunID: "r1"}
	d, _ := Apply(m, start, start.At, time.UTC)
	if d.Changed || d.RunStartedAt == nil || d.RunID != "r1" {
		t.Fatalf("start: %+v", d)
	}
	if !d.NextDueAt.Equal(t0.Add(time.Hour)) {
		t.Fatalf("start must not move the deadline: %v", d.NextDueAt)
	}
	applyTo(t, m, d)
	logObs := &domain.Observation{Signal: domain.SignalLog, At: t0.Add(2 * time.Minute)}
	d, _ = Apply(m, logObs, logObs.At, time.UTC)
	if d.Changed || d.RunStartedAt == nil {
		t.Fatalf("log must keep the run open: %+v", d)
	}
	done := ping(domain.SignalOK, true, t0.Add(4*time.Minute+12*time.Second))
	done.RunID = "r1"
	d, _ = Apply(m, done, done.At, time.UTC)
	if d.DurationMs == nil || *d.DurationMs != (3*time.Minute+12*time.Second).Milliseconds() || d.RunStartedAt != nil {
		t.Fatalf("duration: %+v", d)
	}
}

func TestMaxRuntimeSyntheticFail(t *testing.T) {
	m := hb("1h", "5m")
	m.State = domain.StateUp
	m.LastOkAt = &t0
	m.Heartbeat.MaxRuntime = domain.MustDuration("10m")
	started := t0.Add(time.Minute)
	m.RunStartedAt = &started
	m.RunID = "r1"
	// the wake-up is the run timeout, earlier than the deadline
	d, _ := Apply(m, nil, t0.Add(2*time.Minute), time.UTC)
	if d.Changed || !d.NextDueAt.Equal(started.Add(10*time.Minute)) {
		t.Fatalf("wake at run timeout: %+v", d)
	}
	d, _ = Apply(m, nil, started.Add(10*time.Minute), time.UTC)
	if !d.Changed || d.To != domain.StateDown || d.Reason != "run timeout" || d.Synthetic == nil || d.Synthetic.Signal != domain.SignalFail {
		t.Fatalf("run timeout: %+v", d)
	}
	if d.Synthetic.Detail["reason"] != "run_timeout" || d.Synthetic.RunID != "r1" {
		t.Fatalf("synthetic detail: %+v", d.Synthetic)
	}
}

func TestPausedIgnoresEverything(t *testing.T) {
	m := hb("1h", "5m")
	m.State = domain.StatePaused
	m.Paused = true
	d, _ := Apply(m, ping(domain.SignalOK, true, t0), t0, time.UTC)
	if d.Changed || d.NextDueAt != nil {
		t.Fatalf("paused ping: %+v", d)
	}
	d, _ = Apply(m, nil, t0.Add(100*time.Hour), time.UTC)
	if d.Changed {
		t.Fatal("paused tick must not flip")
	}
}

func TestPlanAfterResume(t *testing.T) {
	m := hb("1h", "5m")
	m.State = domain.StateNew
	resumed := t0.Add(24 * time.Hour)
	m.StateSince = resumed
	m.BaseAt = resumed
	old := t0
	m.LastOkAt = &old
	exp, next, err := Plan(m, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if !exp.Equal(resumed.Add(time.Hour)) || !next.Equal(resumed.Add(time.Hour)) {
		t.Fatalf("resume plan: expected=%v next=%v", exp, next)
	}
	m.Paused = true
	if _, next, _ := Plan(m, time.UTC); next != nil {
		t.Fatal("paused plan must have no wake-up")
	}
}

func TestCronDeadlineUsesLocation(t *testing.T) {
	ams, _ := time.LoadLocation("Europe/Amsterdam")
	m := hb("1h", "30m")
	m.Heartbeat.Schedule = domain.Schedule{Cron: "0 3 * * *"}
	m.State = domain.StateUp
	ok := time.Date(2026, 9, 27, 3, 5, 0, 0, ams)
	m.LastOkAt = &ok
	m.BaseAt = ok
	d, _ := Apply(m, nil, ok.Add(time.Minute), ams)
	want := time.Date(2026, 9, 28, 3, 0, 0, 0, ams)
	if !d.NextDueAt.Equal(want) {
		t.Fatalf("cron next due = %v, want %v", d.NextDueAt, want)
	}
}

func TestApplyWithoutSpec(t *testing.T) {
	m := &domain.Monitor{Kind: domain.KindHeartbeat}
	if _, err := Apply(m, nil, t0, time.UTC); err == nil {
		t.Fatal("expected error")
	}
}
