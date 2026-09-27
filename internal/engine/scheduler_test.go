package engine

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

type fakeTicker struct {
	mu   sync.Mutex
	due  map[string]time.Time
	tick []string
	fail map[string]bool
}

func (f *fakeTicker) ListDue(_ context.Context, now time.Time, limit int) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var ids []string
	for id, at := range f.due {
		if !at.After(now) {
			ids = append(ids, id)
		}
		if len(ids) == limit {
			break
		}
	}
	return ids, nil
}

func (f *fakeTicker) Tick(_ context.Context, id string, now time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tick = append(f.tick, id)
	if f.fail[id] {
		return errors.New("boom")
	}
	f.due[id] = now.Add(time.Hour)
	return nil
}

func (f *fakeTicker) NextDueAt(context.Context) (time.Time, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var min time.Time
	for _, at := range f.due {
		if min.IsZero() || at.Before(min) {
			min = at
		}
	}
	return min, !min.IsZero(), nil
}

func TestRunOnceTicksDueMonitors(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	f := &fakeTicker{due: map[string]time.Time{"a": now.Add(-time.Minute), "b": now, "c": now.Add(time.Minute)}, fail: map[string]bool{"b": true}}
	s := NewScheduler(f, NewBus(), slog.New(slog.NewTextHandler(io.Discard, nil)), func() time.Time { return now })
	n, err := s.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 || len(f.tick) != 2 {
		t.Fatalf("ticked %d ok of %v", n, f.tick)
	}
	if s.LastTick().IsZero() {
		t.Error("LastTick not recorded")
	}
	// a failing tick does not loop forever
	n, _ = s.RunOnce(context.Background())
	if n != 0 {
		t.Fatalf("second pass ticked %d", n)
	}
}

func TestRunWakesOnBusAndTimer(t *testing.T) {
	var mu sync.Mutex
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	f := &fakeTicker{due: map[string]time.Time{"a": now.Add(50 * time.Millisecond)}}
	bus := NewBus()
	s := NewScheduler(f, bus, slog.New(slog.NewTextHandler(io.Discard, nil)), clock)
	s.MaxSleep = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	// advance the clock past the due time; the max-sleep timer picks it up
	mu.Lock()
	now = now.Add(time.Minute)
	mu.Unlock()
	for time.Now().Before(deadline) {
		f.mu.Lock()
		n := len(f.tick)
		f.mu.Unlock()
		if n >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	f.mu.Lock()
	ticked := len(f.tick)
	f.mu.Unlock()
	if ticked < 1 {
		t.Fatal("scheduler never ticked the due monitor")
	}
	// a bus event wakes it again immediately
	f.mu.Lock()
	f.due["b"] = now
	f.mu.Unlock()
	bus.Publish(MonitorChanged{MonitorID: "b"})
	for time.Now().Before(deadline) {
		f.mu.Lock()
		found := false
		for _, id := range f.tick {
			if id == "b" {
				found = true
			}
		}
		f.mu.Unlock()
		if found {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, id := range f.tick {
		if id == "b" {
			return
		}
	}
	t.Fatal("bus event did not wake the scheduler")
}
