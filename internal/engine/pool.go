package engine

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// CheckRunner is the service side of the checker pool.
type CheckRunner interface {
	// ListDueChecks returns ids of pull monitors due at or before now.
	ListDueChecks(ctx context.Context, now time.Time, limit int) ([]string, error)
	// NextCheckDueAt returns the earliest pending check, if any.
	NextCheckDueAt(ctx context.Context) (time.Time, bool, error)
	// RunCheck runs the attempts for one monitor and persists the outcome.
	RunCheck(ctx context.Context, monitorID string, now time.Time) error
}

// Pool drives pull monitors: a loop hands due monitors to workers, and a
// per-monitor lock keeps attempts from overlapping, also with check now.
type Pool struct {
	runner CheckRunner
	bus    *Bus
	log    *slog.Logger
	now    func() time.Time
	// Workers is the number of concurrent attempts.
	Workers int
	// MaxSleep bounds a sleep so a missed bus event costs at most this long.
	MaxSleep time.Duration
	// Batch is how many due monitors one pass loads.
	Batch int
	// Backoff is how long a monitor whose run errored is left alone.
	Backoff time.Duration
	// OnLag, when set, receives how far behind each pass ran (zero when on time).
	OnLag func(time.Duration)

	mu       sync.Mutex
	inflight map[string]bool
	locks    map[string]*sync.Mutex
	retryAt  map[string]time.Time
	lastTick atomic.Int64
}

// NewPool wires a pool. now is injectable for tests.
func NewPool(runner CheckRunner, workers int, bus *Bus, log *slog.Logger, now func() time.Time) *Pool {
	if now == nil {
		now = time.Now
	}
	if workers <= 0 {
		workers = 32
	}
	return &Pool{
		runner: runner, bus: bus, log: log, now: now, Workers: workers, MaxSleep: 30 * time.Second, Batch: 200, Backoff: 30 * time.Second,
		inflight: map[string]bool{}, locks: map[string]*sync.Mutex{}, retryAt: map[string]time.Time{},
	}
}

// LastTick is when the loop last looked for due monitors.
func (p *Pool) LastTick() time.Time {
	ms := p.lastTick.Load()
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

func (p *Pool) lockFor(id string) *sync.Mutex {
	p.mu.Lock()
	defer p.mu.Unlock()
	l, ok := p.locks[id]
	if !ok {
		l = &sync.Mutex{}
		p.locks[id] = l
	}
	return l
}

// claim marks id in flight unless it already is or is backing off.
func (p *Pool) claim(id string, now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.inflight[id] || now.Before(p.retryAt[id]) {
		return false
	}
	p.inflight[id] = true
	return true
}

func (p *Pool) release(id string, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.inflight, id)
	if err != nil {
		p.retryAt[id] = p.now().Add(p.Backoff)
	} else {
		delete(p.retryAt, id)
	}
}

// run executes one monitor under its lock.
func (p *Pool) run(ctx context.Context, id string) error {
	l := p.lockFor(id)
	l.Lock()
	defer l.Unlock()
	return p.runner.RunCheck(ctx, id, p.now())
}

// CheckNow runs a monitor at once, waiting for an attempt in flight.
func (p *Pool) CheckNow(ctx context.Context, id string) error {
	return p.run(ctx, id)
}

// RunOnce claims every due monitor and runs them on the workers, then
// waits for them. Tests and the loop use it.
func (p *Pool) RunOnce(ctx context.Context) (int, error) {
	now := p.now()
	p.lastTick.Store(now.UnixMilli())
	ids, err := p.runner.ListDueChecks(ctx, now, p.Batch)
	if err != nil {
		return 0, err
	}
	var claimed []string
	for _, id := range ids {
		if p.claim(id, now) {
			claimed = append(claimed, id)
		}
	}
	sem := make(chan struct{}, p.Workers)
	var wg sync.WaitGroup
	for _, id := range claimed {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			p.release(id, nil)
			continue
		}
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			defer func() { <-sem }()
			err := p.run(ctx, id)
			if err != nil && ctx.Err() == nil {
				p.log.Error("check failed", "monitor_id", id, "err", err)
			}
			p.release(id, err)
		}(id)
	}
	wg.Wait()
	return len(claimed), nil
}

// Run loops until ctx is done: run what is due, sleep until the next due
// time, a bus event or MaxSleep.
func (p *Pool) Run(ctx context.Context) error {
	events, cancel := p.bus.Subscribe()
	defer cancel()
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		n, err := p.RunOnce(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			p.log.Error("check pass failed", "err", err)
		} else if n > 0 {
			p.log.Debug("check pass", "ran", n)
		}
		sleep := p.MaxSleep
		if next, ok, err := p.runner.NextCheckDueAt(ctx); ctx.Err() != nil {
			return ctx.Err()
		} else if err != nil {
			p.log.Error("check next due", "err", err)
		} else if ok {
			until := next.Sub(p.now())
			if p.OnLag != nil {
				lag := -until
				if lag < 0 {
					lag = 0
				}
				p.OnLag(lag)
			}
			if until < time.Second {
				// Due but backing off or just finished: look again soon.
				until = time.Second
			}
			if until < sleep {
				sleep = until
			}
		}
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
		case <-events:
			for len(events) > 0 {
				<-events
			}
		case <-timer.C:
		}
	}
}
