package engine

import (
	"context"
	"log/slog"
	"time"

	"github.com/w4jnl/vink/internal/domain"
)

// Sender is the outbox side of the service layer.
type Sender interface {
	DueDeliveries(ctx context.Context, now time.Time, limit int) ([]domain.Delivery, error)
	EarlierPending(ctx context.Context, d domain.Delivery) (int, error)
	Deliver(ctx context.Context, d domain.Delivery) error
	MarkDelivered(ctx context.Context, id string, attempt int, now time.Time) error
	RescheduleDelivery(ctx context.Context, id string, attempt int, next time.Time, errMsg string) error
	FailDelivery(ctx context.Context, id string, attempt int, now time.Time, errMsg string) error
	EnqueueRepeats(ctx context.Context, now time.Time) (int, error)
}

// Backoff is the delay after each failed attempt; the sixth failure is
// final.
var Backoff = []time.Duration{30 * time.Second, 2 * time.Minute, 10 * time.Minute, 30 * time.Minute, 2 * time.Hour}

// Dispatcher drains the deliveries outbox.
type Dispatcher struct {
	sender      Sender
	log         *slog.Logger
	now         func() time.Time
	Interval    time.Duration
	SendTimeout time.Duration
	MaxAttempts int
	Batch       int
}

// NewDispatcher wires a dispatcher with the design document's defaults.
func NewDispatcher(sender Sender, log *slog.Logger, now func() time.Time) *Dispatcher {
	if now == nil {
		now = time.Now
	}
	return &Dispatcher{sender: sender, log: log, now: now, Interval: time.Second, SendTimeout: 10 * time.Second, MaxAttempts: 6, Batch: 50}
}

// RunOnce enqueues due repeats and processes every due delivery once.
func (d *Dispatcher) RunOnce(ctx context.Context) (sent, failed int, err error) {
	now := d.now()
	if n, err := d.sender.EnqueueRepeats(ctx, now); err != nil {
		d.log.Error("enqueue repeats", "err", err)
	} else if n > 0 {
		d.log.Debug("repeat notifications enqueued", "n", n)
	}
	due, err := d.sender.DueDeliveries(ctx, now, d.Batch)
	if err != nil {
		return 0, 0, err
	}
	for _, dl := range due {
		if pending, err := d.sender.EarlierPending(ctx, dl); err != nil {
			d.log.Error("check delivery order", "id", dl.ID, "err", err)
			continue
		} else if pending > 0 {
			continue // an earlier delivery to this channel is still pending
		}
		attempt := dl.Attempt + 1
		sendCtx, cancel := context.WithTimeout(ctx, d.SendTimeout)
		sendErr := d.sender.Deliver(sendCtx, dl)
		cancel()
		switch {
		case sendErr == nil:
			if err := d.sender.MarkDelivered(ctx, dl.ID, attempt, now); err != nil {
				d.log.Error("mark delivered", "id", dl.ID, "err", err)
			}
			sent++
		case attempt >= d.MaxAttempts:
			d.log.Error("delivery failed for good", "id", dl.ID, "kind", dl.Kind, "attempt", attempt, "err", sendErr)
			if err := d.sender.FailDelivery(ctx, dl.ID, attempt, now, sendErr.Error()); err != nil {
				d.log.Error("mark failed", "id", dl.ID, "err", err)
			}
			failed++
		default:
			wait := Backoff[min(attempt-1, len(Backoff)-1)]
			d.log.Warn("delivery attempt failed", "id", dl.ID, "kind", dl.Kind, "attempt", attempt, "retry_in", wait.String(), "err", sendErr)
			if err := d.sender.RescheduleDelivery(ctx, dl.ID, attempt, now.Add(wait), sendErr.Error()); err != nil {
				d.log.Error("reschedule", "id", dl.ID, "err", err)
			}
		}
	}
	return sent, failed, nil
}

// Run polls until ctx is done.
func (d *Dispatcher) Run(ctx context.Context) error {
	t := time.NewTicker(d.Interval)
	defer t.Stop()
	for {
		if _, _, err := d.RunOnce(ctx); err != nil {
			d.log.Error("dispatcher pass failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}
