package engine

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/w4jnl/vink/internal/domain"
)

type fakeSender struct {
	rows      []domain.Delivery
	fail      map[string]int // remaining failures per id
	sent      []string
	repeats   int
	delivered map[string]int
	failed    map[string]string
	resched   map[string]time.Time
}

func (f *fakeSender) DueDeliveries(_ context.Context, now time.Time, limit int) ([]domain.Delivery, error) {
	var out []domain.Delivery
	for _, r := range f.rows {
		if r.DeliveredAt == nil && r.FailedAt == nil && !r.NextAttemptAt.After(now) {
			out = append(out, r)
		}
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

func (f *fakeSender) EarlierPending(_ context.Context, d domain.Delivery) (int, error) {
	n := 0
	for _, r := range f.rows {
		if r.MonitorID == d.MonitorID && r.ChannelID == d.ChannelID && r.DeliveredAt == nil && r.FailedAt == nil && r.CreatedAt.Before(d.CreatedAt) {
			n++
		}
	}
	return n, nil
}

func (f *fakeSender) Deliver(_ context.Context, d domain.Delivery) error {
	if f.fail[d.ID] > 0 {
		f.fail[d.ID]--
		return errors.New("boom")
	}
	f.sent = append(f.sent, d.ID)
	return nil
}

func (f *fakeSender) row(id string) *domain.Delivery {
	for i := range f.rows {
		if f.rows[i].ID == id {
			return &f.rows[i]
		}
	}
	return nil
}

func (f *fakeSender) MarkDelivered(_ context.Context, id string, attempt int, now time.Time) error {
	r := f.row(id)
	r.DeliveredAt, r.Attempt = &now, attempt
	f.delivered[id] = attempt
	return nil
}

func (f *fakeSender) RescheduleDelivery(_ context.Context, id string, attempt int, next time.Time, msg string) error {
	r := f.row(id)
	r.Attempt, r.NextAttemptAt, r.Error = attempt, next, msg
	f.resched[id] = next
	return nil
}

func (f *fakeSender) FailDelivery(_ context.Context, id string, attempt int, now time.Time, msg string) error {
	r := f.row(id)
	r.FailedAt, r.Attempt, r.Error = &now, attempt, msg
	f.failed[id] = msg
	return nil
}

func (f *fakeSender) EnqueueRepeats(context.Context, time.Time) (int, error) {
	f.repeats++
	return 0, nil
}

func newFake(rows ...domain.Delivery) *fakeSender {
	return &fakeSender{rows: rows, fail: map[string]int{}, delivered: map[string]int{}, failed: map[string]string{}, resched: map[string]time.Time{}}
}

func TestDispatcherBackoffAndOrder(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	down := domain.Delivery{ID: "down", MonitorID: "m", ChannelID: "c", Kind: "down", NextAttemptAt: now, CreatedAt: now.Add(-2 * time.Minute)}
	up := domain.Delivery{ID: "up", MonitorID: "m", ChannelID: "c", Kind: "up", NextAttemptAt: now, CreatedAt: now.Add(-time.Minute)}
	other := domain.Delivery{ID: "other", MonitorID: "x", ChannelID: "c", Kind: "down", NextAttemptAt: now, CreatedAt: now}
	f := newFake(down, up, other)
	f.fail["down"] = 2
	d := NewDispatcher(f, slog.New(slog.NewTextHandler(io.Discard, nil)), func() time.Time { return now })

	sent, failed, err := d.RunOnce(context.Background())
	if err != nil || sent != 1 || failed != 0 {
		t.Fatalf("pass 1: sent=%d failed=%d err=%v", sent, failed, err)
	}
	if len(f.sent) != 1 || f.sent[0] != "other" {
		t.Fatalf("pass 1 sent %v; up must wait for down", f.sent)
	}
	if next := f.resched["down"]; !next.Equal(now.Add(30 * time.Second)) {
		t.Fatalf("first backoff = %v", next)
	}
	if f.repeats != 1 {
		t.Error("repeats not enqueued")
	}
	// too early: nothing due
	now = now.Add(10 * time.Second)
	if sent, _, _ := d.RunOnce(context.Background()); sent != 0 {
		t.Fatal("retry ran early")
	}
	// second failure: 2m backoff
	now = now.Add(30 * time.Second)
	_, _, _ = d.RunOnce(context.Background())
	if next := f.resched["down"]; !next.Equal(now.Add(2 * time.Minute)) {
		t.Fatalf("second backoff = %v", next)
	}
	// third try succeeds, then up follows in the same pass order
	now = now.Add(3 * time.Minute)
	sent, _, _ = d.RunOnce(context.Background())
	if sent != 2 || f.delivered["down"] != 3 || f.delivered["up"] != 1 {
		t.Fatalf("recovery pass: sent=%d delivered=%v", sent, f.delivered)
	}
}

func TestDispatcherGivesUpAfterMaxAttempts(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	f := newFake(domain.Delivery{ID: "d", MonitorID: "m", ChannelID: "c", NextAttemptAt: now, CreatedAt: now})
	f.fail["d"] = 100
	d := NewDispatcher(f, slog.New(slog.NewTextHandler(io.Discard, nil)), func() time.Time { return now })
	for i := 0; i < 6; i++ {
		_, failed, _ := d.RunOnce(context.Background())
		if i < 5 && failed != 0 {
			t.Fatalf("failed early on attempt %d", i+1)
		}
		if i == 5 && failed != 1 {
			t.Fatalf("attempt 6 must be final")
		}
		if next, ok := f.resched["d"]; ok {
			now = next
		}
	}
	if f.failed["d"] != "boom" || f.row("d").FailedAt == nil {
		t.Fatalf("final failure not recorded: %+v", f.row("d"))
	}
	if _, _, _ = d.RunOnce(context.Background()); len(f.sent) != 0 {
		t.Fatal("failed row must not be retried")
	}
}

func TestDispatcherRunStops(t *testing.T) {
	f := newFake()
	d := NewDispatcher(f, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	d.Interval = 5 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := d.Run(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run returned %v", err)
	}
	if f.repeats < 2 {
		t.Fatalf("expected several passes, got %d", f.repeats)
	}
}
