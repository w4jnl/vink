package engine

import (
	"fmt"
	"time"

	"github.com/w4jnl/vink/internal/domain"
)

// Decision is the outcome of applying one observation, or one scheduler
// tick, to a monitor. The caller persists it in the same transaction as
// the observation.
type Decision struct {
	From    domain.State
	To      domain.State
	Changed bool
	Reason  string

	// Updated working state for the monitor row.
	BaseAt       time.Time
	LastOkAt     *time.Time
	FailStreak   int
	OkStreak     int
	RunStartedAt *time.Time
	RunID        string
	// NextDueAt is when the scheduler must look at this monitor again;
	// nil when nothing is pending.
	NextDueAt *time.Time
	// ExpectedAt is the next expected ping, for display; nil while paused
	// or down.
	ExpectedAt *time.Time

	// DurationMs is set on a completing ping when a start was open.
	DurationMs *int64
	// Synthetic is a run-timeout observation the caller must store.
	Synthetic *domain.Observation
}

// Apply runs the state machine. obs is nil for a scheduler tick, which
// evaluates deadlines and run timeouts at now. loc is the monitor's
// resolved location.
func Apply(m *domain.Monitor, obs *domain.Observation, now time.Time, loc *time.Location) (Decision, error) {
	d := Decision{
		From: m.State, To: m.State, BaseAt: m.BaseAt,
		LastOkAt: m.LastOkAt, FailStreak: m.FailStreak, OkStreak: m.OkStreak,
		RunStartedAt: m.RunStartedAt, RunID: m.RunID,
	}
	spec := m.Heartbeat
	if spec == nil {
		return d, fmt.Errorf("monitor %s has no heartbeat spec", m.Slug)
	}
	if m.Paused || m.State == domain.StatePaused {
		// Observations are stored but nothing moves while paused.
		return d, nil
	}
	flip := func(to domain.State, reason string) {
		if to == d.To {
			return
		}
		d.To = to
		d.Changed = true
		d.Reason = reason
	}

	if obs != nil {
		switch obs.Signal {
		case domain.SignalStart:
			at := obs.At
			d.RunStartedAt = &at
			d.RunID = obs.RunID
		case domain.SignalLog:
			// informational only
		default:
			if d.RunStartedAt != nil && (obs.RunID == "" || obs.RunID == d.RunID) {
				ms := obs.At.Sub(*d.RunStartedAt).Milliseconds()
				d.DurationMs = &ms
			}
			d.RunStartedAt = nil
			d.RunID = ""
			if obs.OK {
				at := obs.At
				d.OkStreak++
				d.FailStreak = 0
				d.LastOkAt = &at
				d.BaseAt = at
				switch m.State {
				case domain.StateDown:
					if d.OkStreak >= spec.RecoveryThreshold {
						flip(domain.StateUp, "recovered")
					}
				case domain.StateNew:
					flip(domain.StateUp, "first ok")
				case domain.StateLate:
					flip(domain.StateUp, "ok")
				}
			} else {
				d.FailStreak++
				d.OkStreak = 0
				if d.FailStreak >= spec.FailureThreshold {
					flip(domain.StateDown, failReason(obs))
				}
			}
		}
	} else {
		if d.RunStartedAt != nil && spec.MaxRuntime > 0 && !now.Before(d.RunStartedAt.Add(spec.MaxRuntime.Std())) {
			d.Synthetic = &domain.Observation{
				MonitorID: m.ID, ProjectID: m.ProjectID, At: now, Source: "local",
				Signal: domain.SignalFail, OK: false, RunID: d.RunID,
				Detail: map[string]any{"reason": "run_timeout", "max_runtime": spec.MaxRuntime.String()},
			}
			d.RunStartedAt = nil
			d.RunID = ""
			d.FailStreak++
			d.OkStreak = 0
			if d.FailStreak >= spec.FailureThreshold {
				flip(domain.StateDown, "run timeout")
			}
		}
		if d.To == domain.StateNew || d.To == domain.StateUp || d.To == domain.StateLate {
			expected, err := spec.ExpectedAfter(d.BaseAt, loc)
			if err != nil {
				return d, err
			}
			downAt := expected.Add(spec.Grace.Std())
			switch {
			case !now.Before(downAt):
				flip(domain.StateDown, "grace over")
			case !now.Before(expected):
				flip(domain.StateLate, "deadline passed")
			}
		}
	}

	if err := d.plan(spec, loc); err != nil {
		return d, err
	}
	return d, nil
}

// Plan computes ExpectedAt and NextDueAt for a monitor whose row is
// already in its final state, for example after create or resume.
func Plan(m *domain.Monitor, loc *time.Location) (expected, nextDue *time.Time, err error) {
	d := Decision{To: m.State, BaseAt: m.BaseAt, LastOkAt: m.LastOkAt, RunStartedAt: m.RunStartedAt}
	if m.Paused {
		d.To = domain.StatePaused
	}
	if m.Heartbeat == nil {
		return nil, nil, fmt.Errorf("monitor %s has no heartbeat spec", m.Slug)
	}
	if err := d.plan(m.Heartbeat, loc); err != nil {
		return nil, nil, err
	}
	return d.ExpectedAt, d.NextDueAt, nil
}

func (d *Decision) plan(spec *domain.HeartbeatSpec, loc *time.Location) error {
	d.ExpectedAt, d.NextDueAt = nil, nil
	var wake *time.Time
	if d.RunStartedAt != nil && spec.MaxRuntime > 0 {
		t := d.RunStartedAt.Add(spec.MaxRuntime.Std())
		wake = &t
	}
	switch d.To {
	case domain.StateNew, domain.StateUp, domain.StateLate:
		expected, err := spec.ExpectedAfter(d.BaseAt, loc)
		if err != nil {
			return err
		}
		d.ExpectedAt = &expected
		deadline := expected
		if d.To == domain.StateLate {
			deadline = expected.Add(spec.Grace.Std())
		}
		if wake == nil || deadline.Before(*wake) {
			wake = &deadline
		}
	}
	d.NextDueAt = wake
	return nil
}

func failReason(obs *domain.Observation) string {
	if obs.Signal == domain.SignalExit && obs.ExitCode != nil {
		return fmt.Sprintf("exit %d", *obs.ExitCode)
	}
	if r, ok := obs.Detail["reason"].(string); ok && r != "" {
		return r
	}
	return "fail signal"
}
