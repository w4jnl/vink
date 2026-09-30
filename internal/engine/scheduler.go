package engine

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"
)

// Ticker is what the scheduler drives: the service layer's deadline path.
type Ticker interface {
	// ListDue returns ids of monitors whose next_due_at is at or before now.
	ListDue(ctx context.Context, now time.Time, limit int) ([]string, error)
	// Tick applies deadlines and run timeouts for one monitor at now.
	Tick(ctx context.Context, monitorID string, now time.Time) error
	// NextDueAt returns the earliest pending wake-up, if any.
	NextDueAt(ctx context.Context) (time.Time, bool, error)
}

// Scheduler sleeps until the earliest next_due_at, or a bus event, and
// then ticks every due monitor.
type Scheduler struct {
	ticker Ticker
	bus    *Bus
	log    *slog.Logger
	now    func() time.Time
	// MaxSleep bounds a sleep so a missed bus event costs at most this long.
	MaxSleep time.Duration
	// Batch is how many due monitors one pass loads.
	Batch int
	// LagWarn is the lateness above which a tick is logged as behind.
	LagWarn time.Duration
	// OnLag, when set, receives how far behind each pass ran (zero when on time).
	OnLag    func(time.Duration)
	lastTick atomic.Int64
}

// NewScheduler wires a scheduler. now is injectable for tests.
func NewScheduler(ticker Ticker, bus *Bus, log *slog.Logger, now func() time.Time) *Scheduler {
	if now == nil {
		now = time.Now
	}
	return &Scheduler{ticker: ticker, bus: bus, log: log, now: now, MaxSleep: 30 * time.Second, Batch: 100, LagWarn: 5 * time.Second}
}

// LastTick is when the loop last ran; readiness checks compare it to now.
func (s *Scheduler) LastTick() time.Time {
	ms := s.lastTick.Load()
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

// RunOnce ticks every monitor that is due at now and returns how many.
func (s *Scheduler) RunOnce(ctx context.Context) (int, error) {
	now := s.now()
	s.lastTick.Store(now.UnixMilli())
	total := 0
	seen := map[string]bool{}
	for range 10 {
		ids, err := s.ticker.ListDue(ctx, now, s.Batch)
		if err != nil {
			return total, err
		}
		progressed := false
		for _, id := range ids {
			if seen[id] {
				continue
			}
			seen[id] = true
			progressed = true
			if err := s.ticker.Tick(ctx, id, now); err != nil {
				s.log.Error("scheduler tick failed", "monitor_id", id, "err", err)
				continue
			}
			total++
		}
		if !progressed || len(ids) < s.Batch {
			break
		}
	}
	return total, nil
}

// Run loops until ctx is done.
func (s *Scheduler) Run(ctx context.Context) error {
	events, cancel := s.bus.Subscribe()
	defer cancel()
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		n, err := s.RunOnce(ctx)
		if err != nil {
			s.log.Error("scheduler pass failed", "err", err)
		} else if n > 0 {
			s.log.Debug("scheduler pass", "ticked", n)
		}
		sleep := s.MaxSleep
		reason := "max sleep"
		if next, ok, err := s.ticker.NextDueAt(ctx); err != nil {
			s.log.Error("scheduler next due", "err", err)
		} else if ok {
			until := next.Sub(s.now())
			lag := time.Duration(0)
			if until < 0 {
				lag = -until
				if lag > s.LagWarn {
					s.log.Warn("scheduler behind", "lag_ms", lag.Milliseconds())
				}
				until = 0
			}
			if s.OnLag != nil {
				s.OnLag(lag)
			}
			if until < sleep {
				sleep = until
				reason = "next due"
			}
		}
		s.log.Debug("scheduler sleeping", "for", sleep.String(), "reason", reason)
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(sleep)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case e := <-events:
			s.log.Debug("scheduler woke", "reason", "monitor changed", "monitor_id", e.MonitorID)
			for len(events) > 0 {
				<-events
			}
		case <-timer.C:
		}
	}
}
