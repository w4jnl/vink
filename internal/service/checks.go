package service

import (
	"context"
	"errors"
	"time"

	"github.com/w4jnl/vink/internal/checks"
	"github.com/w4jnl/vink/internal/db"
	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/engine"
)

// SetChecker installs the checker registry that runs pull monitors.
func (s *Service) SetChecker(r *checks.Registry) { s.checker = r }

// SetCheckNow installs the pool's entry point for check now, which holds
// the per-monitor lock. Without it CheckNow runs the check inline.
func (s *Service) SetCheckNow(fn func(ctx context.Context, monitorID string) error) { s.checkNow = fn }

// ListDueChecks returns pull monitors whose next attempt is due.
func (s *Service) ListDueChecks(ctx context.Context, now time.Time, limit int) ([]string, error) {
	return s.db.Read().ListDueChecks(ctx, db.ListDueChecksParams{NextDueAt: ptri(domain.Millis(now)), Limit: int64(limit)})
}

// NextCheckDueAt returns the earliest pending attempt.
func (s *Service) NextCheckDueAt(ctx context.Context) (time.Time, bool, error) {
	ms, err := s.db.Read().NextCheckDueAt(ctx)
	if err != nil || ms == 0 {
		return time.Time{}, false, err
	}
	return domain.FromMillis(ms), true, nil
}

// attempt is one run of the checker with when it started.
type attempt struct {
	at  time.Time
	res checks.Result
}

// runAttempts runs the checker and, on failure, the confirm retries.
func (s *Service) runAttempts(ctx context.Context, m *domain.Monitor) []attempt {
	spec := m.Pull
	var out []attempt
	for i := 0; ; i++ {
		at := s.now()
		res := s.checker.Attempt(ctx, m.Kind, spec)
		out = append(out, attempt{at: at, res: res})
		if res.OK || i >= spec.Confirm.Retries || ctx.Err() != nil {
			return out
		}
		select {
		case <-ctx.Done():
			return out
		case <-time.After(spec.Confirm.Delay.Std()):
		}
	}
}

// observationFrom stores one attempt: the checker's detail plus the
// reason, the warn flag and, inside a confirm sequence, the attempt
// number so the UI can say "confirming (1 of 3)".
func observationFrom(m *domain.Monitor, a attempt, n, total int) *domain.Observation {
	detail := make(map[string]any, len(a.res.Detail)+3)
	for k, v := range a.res.Detail {
		detail[k] = v
	}
	if !a.res.OK || a.res.Warn {
		detail["reason"] = a.res.Reason
	}
	if a.res.Warn {
		detail["warn"] = true
	}
	if total > 1 {
		detail["attempt"] = n
		detail["attempts"] = total
	}
	latency := a.res.LatencyMs
	obs := &domain.Observation{
		ID: domain.NewID(), MonitorID: m.ID, ProjectID: m.ProjectID, At: a.at, Source: "local", Signal: domain.SignalOK, OK: a.res.OK, LatencyMs: &latency, Detail: detail,
	}
	if !a.res.OK {
		obs.Signal = domain.SignalFail
	}
	return obs
}

// RunCheck runs the attempts for one pull monitor and persists them with
// the state machine's decision. The network work happens outside the
// transaction; the monitor is reloaded inside it.
func (s *Service) RunCheck(ctx context.Context, monitorID string, now time.Time) error {
	if s.checker == nil {
		return errors.New("no checker registry configured")
	}
	row, err := s.db.Read().GetMonitorByID(ctx, monitorID)
	if err != nil {
		if db.IsNotFound(err) {
			return nil
		}
		return err
	}
	m, err := monitorFromRow(row)
	if err != nil {
		return err
	}
	if !m.Kind.IsPull() || m.Paused || m.Pull == nil {
		return nil
	}
	attempts := s.runAttempts(ctx, m)
	if len(attempts) == 0 {
		return ctx.Err()
	}
	final := attempts[len(attempts)-1]
	var decision engine.Decision
	err = s.db.Tx(ctx, func(q *db.Queries) error {
		row, err := q.GetMonitorByID(ctx, monitorID)
		if err != nil {
			if db.IsNotFound(err) {
				return nil
			}
			return err
		}
		cur, err := monitorFromRow(row)
		if err != nil {
			return err
		}
		if cur.Paused {
			return nil
		}
		var last *domain.Observation
		for i, a := range attempts {
			obs := observationFrom(cur, a, i+1, len(attempts))
			if err := insertObservation(ctx, q, obs, nil, ""); err != nil {
				return err
			}
			last = obs
		}
		decision, err = engine.ApplyCheck(cur, last, final.res.OK && final.res.Warn, final.at)
		if err != nil {
			return err
		}
		return s.persistDecision(ctx, q, cur, last, decision, now)
	})
	if err != nil {
		return err
	}
	if decision.Changed {
		s.bus.Publish(engine.MonitorChanged{ProjectID: m.ProjectID, MonitorID: m.ID})
	}
	return nil
}

// CheckNow runs a pull monitor at once for a member and returns it fresh.
func (s *Service) CheckNow(ctx context.Context, sc domain.Scope, slug string) (*domain.Monitor, error) {
	if err := requireOperate(sc); err != nil {
		return nil, err
	}
	m, err := s.MonitorBySlug(ctx, sc, slug)
	if err != nil {
		return nil, err
	}
	if !m.Kind.IsPull() {
		return nil, (&domain.ValidationError{Errors: []domain.FieldError{{Field: "kind", Msg: "only http, tcp, dns, tls and icmp monitors run checks; heartbeats wait for a ping"}}}).OrNil()
	}
	if m.Paused {
		return nil, (&domain.ValidationError{Errors: []domain.FieldError{{Field: "state", Msg: "the monitor is paused; resume it first"}}}).OrNil()
	}
	run := s.checkNow
	if run == nil {
		run = func(ctx context.Context, id string) error { return s.RunCheck(ctx, id, s.now()) }
	}
	if err := run(ctx, m.ID); err != nil {
		return nil, err
	}
	s.log.Info("check now", "project_id", sc.ProjectID, "monitor", slug, "actor", sc.Actor)
	return s.MonitorBySlug(ctx, sc, slug)
}

// checkPull applies the instance floor on the interval.
func (s *Service) checkPull(m *domain.Monitor) error {
	if m.Pull == nil || s.cfg.MinInterval <= 0 || m.Pull.Interval >= s.cfg.MinInterval {
		return nil
	}
	return (&domain.ValidationError{Errors: []domain.FieldError{{Field: "interval", Msg: "must be at least " + s.cfg.MinInterval.String() + " on this instance"}}}).OrNil()
}
