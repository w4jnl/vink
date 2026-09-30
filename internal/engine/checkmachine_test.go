package engine

import (
	"testing"
	"time"

	"github.com/w4jnl/vink/internal/domain"
)

func pullMon(threshold, recovery int) *domain.Monitor {
	return &domain.Monitor{
		ID: "m", Slug: "web", Kind: domain.KindHTTP, State: domain.StateNew, StateSince: t0, BaseAt: t0,
		Pull: &domain.PullSpec{Interval: domain.MustDuration("60s"), Timeout: domain.MustDuration("10s"), FailureThreshold: threshold, RecoveryThreshold: recovery, HTTP: &domain.HTTPCheck{URL: "https://x"}},
	}
}

func check(ok bool, reason string) *domain.Observation {
	obs := &domain.Observation{Signal: domain.SignalOK, OK: ok, At: t0, Source: "local", Detail: map[string]any{}}
	if !ok {
		obs.Signal = domain.SignalFail
	}
	if reason != "" {
		obs.Detail["reason"] = reason
	}
	return obs
}

func TestApplyCheckThresholds(t *testing.T) {
	m := pullMon(3, 2)
	steps := []struct {
		ok     bool
		warn   bool
		reason string
		want   domain.State
		event  string
	}{
		{true, false, "", domain.StateUp, "first ok"},
		{false, false, "status 503", domain.StateLate, "status 503"},
		{false, false, "status 503", domain.StateLate, ""},
		{false, false, "timeout after 10s", domain.StateDown, "timeout after 10s"},
		{false, false, "status 503", domain.StateDown, ""},
		{true, false, "", domain.StateDown, ""},
		{true, false, "", domain.StateUp, "recovered"},
		{true, true, "certificate expires in 10 d", domain.StateLate, "certificate expires in 10 d"},
		{true, true, "certificate expires in 10 d", domain.StateLate, ""},
		{true, false, "", domain.StateUp, "ok"},
	}
	at := t0
	for i, s := range steps {
		at = at.Add(time.Minute)
		d, err := ApplyCheck(m, check(s.ok, s.reason), s.warn, at)
		if err != nil {
			t.Fatal(err)
		}
		if d.To != s.want || (s.event != "" && (!d.Changed || d.Reason != s.event)) || (s.event == "" && d.Changed) {
			t.Fatalf("step %d: got %s changed=%v reason=%q, want %s %q", i, d.To, d.Changed, d.Reason, s.want, s.event)
		}
		if d.NextDueAt == nil || !d.NextDueAt.Equal(at.Add(time.Minute)) {
			t.Fatalf("step %d: next due %v", i, d.NextDueAt)
		}
		applyTo(t, m, d)
	}
	if m.LastOkAt == nil || m.OkStreak != 5 {
		t.Errorf("streaks: ok=%d fail=%d", m.OkStreak, m.FailStreak)
	}
}

func TestApplyCheckThresholdOneGoesStraightDown(t *testing.T) {
	m := pullMon(1, 1)
	d, _ := ApplyCheck(m, check(false, "connection refused"), false, t0)
	if d.To != domain.StateDown || d.Reason != "connection refused" {
		t.Fatalf("got %s %q", d.To, d.Reason)
	}
	applyTo(t, m, d)
	d, _ = ApplyCheck(m, check(true, ""), false, t0.Add(time.Minute))
	if d.To != domain.StateUp || d.Reason != "recovered" {
		t.Fatalf("got %s %q", d.To, d.Reason)
	}
}

func TestApplyCheckPausedAndMissingSpec(t *testing.T) {
	m := pullMon(3, 1)
	m.Paused, m.State = true, domain.StatePaused
	d, err := ApplyCheck(m, check(false, "x"), false, t0)
	if err != nil || d.Changed || d.NextDueAt != nil {
		t.Fatalf("paused: %+v %v", d, err)
	}
	m.Pull = nil
	if _, err := ApplyCheck(m, check(true, ""), false, t0); err == nil {
		t.Fatal("missing spec must fail")
	}
}
