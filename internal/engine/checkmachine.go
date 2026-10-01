package engine

import (
	"fmt"
	"time"

	"github.com/w4jnl/vink/internal/domain"
)

// ApplyCheck runs the state machine for one confirmed pull attempt. A
// failed attempt makes a new or up monitor late at once and down once
// failure_threshold attempts failed in a row; an ok attempt recovers a
// down monitor after recovery_threshold; warn (a certificate inside
// warn_days) keeps the monitor late without counting as a failure. The
// next attempt is due interval after this one started.
func ApplyCheck(m *domain.Monitor, obs *domain.Observation, warn bool, at time.Time) (Decision, error) {
	d := Decision{From: m.State, To: m.State, BaseAt: m.BaseAt, LastOkAt: m.LastOkAt, FailStreak: m.FailStreak, OkStreak: m.OkStreak}
	spec := m.Pull
	if spec == nil {
		return d, fmt.Errorf("monitor %s has no pull spec", m.Slug)
	}
	if obs == nil {
		return d, fmt.Errorf("monitor %s: a check needs an observation", m.Slug)
	}
	if m.Paused || m.State == domain.StatePaused {
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
	reason := "check failed"
	if r, ok := obs.Detail["reason"].(string); ok && r != "" {
		reason = r
	}
	if obs.OK {
		d.OkStreak++
		d.FailStreak = 0
		d.LastOkAt = &at
		d.BaseAt = at
		if warn {
			switch m.State {
			case domain.StateNew, domain.StateUp:
				flip(domain.StateLate, reason)
			case domain.StateDown:
				if d.OkStreak >= spec.RecoveryThreshold {
					flip(domain.StateLate, reason)
				}
			}
		} else {
			switch m.State {
			case domain.StateNew:
				flip(domain.StateUp, "first ok")
			case domain.StateLate:
				flip(domain.StateUp, "ok")
			case domain.StateDown:
				if d.OkStreak >= spec.RecoveryThreshold {
					flip(domain.StateUp, "recovered")
				}
			}
		}
	} else {
		d.FailStreak++
		d.OkStreak = 0
		switch {
		case d.FailStreak >= spec.FailureThreshold:
			flip(domain.StateDown, reason)
		case m.State != domain.StateDown:
			flip(domain.StateLate, reason)
		}
	}
	next := at.Add(spec.Interval.Std())
	d.NextDueAt = &next
	d.ExpectedAt = &next
	return d, nil
}

// ApplyAgentOffline is the decision for a remote check whose agent went
// quiet: a new or up monitor turns late with the reason, a late or down
// one stays as it is, and nothing counts as a failed attempt, so the
// monitor never goes down for lack of an agent.
func ApplyAgentOffline(m *domain.Monitor, reason string) Decision {
	d := Decision{From: m.State, To: m.State, BaseAt: m.BaseAt, LastOkAt: m.LastOkAt, FailStreak: m.FailStreak, OkStreak: m.OkStreak, NextDueAt: m.NextDueAt, ExpectedAt: m.NextDueAt}
	if m.Paused {
		return d
	}
	switch m.State {
	case domain.StateNew, domain.StateUp:
		d.To, d.Changed, d.Reason = domain.StateLate, true, reason
	}
	return d
}
