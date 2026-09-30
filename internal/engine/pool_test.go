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

type fakeRunner struct {
	mu     sync.Mutex
	due    []string
	runs   map[string]int
	active int
	maxAct int
	fail   map[string]bool
	delay  time.Duration
}

func (f *fakeRunner) ListDueChecks(context.Context, time.Time, int) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.due...), nil
}

func (f *fakeRunner) NextCheckDueAt(context.Context) (time.Time, bool, error) {
	return time.Time{}, false, nil
}

func (f *fakeRunner) RunCheck(_ context.Context, id string, _ time.Time) error {
	f.mu.Lock()
	f.active++
	if f.active > f.maxAct {
		f.maxAct = f.active
	}
	f.runs[id]++
	fail := f.fail[id]
	f.mu.Unlock()
	time.Sleep(f.delay)
	f.mu.Lock()
	f.active--
	f.mu.Unlock()
	if fail {
		return errors.New("boom")
	}
	return nil
}

func TestPoolRunsDueOnceWithWorkersAndBackoff(t *testing.T) {
	f := &fakeRunner{due: []string{"a", "b", "c", "d"}, runs: map[string]int{}, fail: map[string]bool{"d": true}, delay: 20 * time.Millisecond}
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	p := NewPool(f, 2, NewBus(), slog.New(slog.NewTextHandler(io.Discard, nil)), func() time.Time { return now })
	n, err := p.RunOnce(context.Background())
	if err != nil || n != 4 {
		t.Fatalf("first pass: %d %v", n, err)
	}
	if f.maxAct != 2 {
		t.Errorf("workers: max active %d, want 2", f.maxAct)
	}
	// still due: a, b, c run again; d backs off after its error
	n, _ = p.RunOnce(context.Background())
	if n != 3 || f.runs["d"] != 1 || f.runs["a"] != 2 {
		t.Fatalf("second pass: n=%d runs=%v", n, f.runs)
	}
	now = now.Add(time.Minute)
	n, _ = p.RunOnce(context.Background())
	if n != 4 || f.runs["d"] != 2 {
		t.Fatalf("after backoff: n=%d runs=%v", n, f.runs)
	}
	if p.LastTick().IsZero() {
		t.Error("last tick")
	}
}

func TestPoolCheckNowWaitsForInflight(t *testing.T) {
	f := &fakeRunner{due: []string{"a"}, runs: map[string]int{}, delay: 60 * time.Millisecond}
	p := NewPool(f, 4, NewBus(), slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = p.RunOnce(context.Background())
	}()
	time.Sleep(10 * time.Millisecond)
	start := time.Now()
	if err := p.CheckNow(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < 40*time.Millisecond {
		t.Error("check now must wait for the attempt in flight")
	}
	wg.Wait()
	if f.maxAct != 1 || f.runs["a"] != 2 {
		t.Errorf("overlap: max active %d runs %d", f.maxAct, f.runs["a"])
	}
}

func TestPoolRunLoopStops(t *testing.T) {
	f := &fakeRunner{runs: map[string]int{}}
	p := NewPool(f, 1, NewBus(), slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	p.MaxSleep = 10 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := p.Run(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("run: %v", err)
	}
}
