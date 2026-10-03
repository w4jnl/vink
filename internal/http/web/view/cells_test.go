package view

import (
	"testing"
	"time"

	"github.com/w4jnl/vink/internal/domain"
)

func TestUpPercentWeighsTimeNotCells(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	created := now.Add(-48 * time.Hour)
	ev := func(at time.Time, from, to domain.State) *domain.Event {
		return &domain.Event{At: at, From: from, To: to}
	}
	// a monitor that was late for one minute in each of the last 24 hours:
	// every cell is amber, but it was up for 99.93% of the time
	var flaps []*domain.Event
	for h := 24; h >= 1; h-- {
		at := now.Add(-time.Duration(h) * time.Hour).Add(30 * time.Minute)
		flaps = append(flaps, ev(at, domain.StateUp, domain.StateLate), ev(at.Add(time.Minute), domain.StateLate, domain.StateUp))
	}
	cases := []struct {
		name    string
		created time.Time
		current domain.State
		events  []*domain.Event
		want    string
	}{
		{"always up", created, domain.StateUp, nil, "100%"},
		{"a minute late every hour", created, domain.StateUp, flaps, "98.33%"},
		{"down for six hours", created, domain.StateUp, []*domain.Event{ev(now.Add(-8*time.Hour), domain.StateUp, domain.StateDown), ev(now.Add(-2*time.Hour), domain.StateDown, domain.StateUp)}, "75.00%"},
		{"created an hour ago, half of it down", now.Add(-time.Hour), domain.StateDown, []*domain.Event{ev(now.Add(-30*time.Minute), domain.StateUp, domain.StateDown)}, "50.00%"},
		{"new since creation", created, domain.StateNew, nil, "no data"},
		{"not yet created", now.Add(time.Hour), domain.StateNew, nil, "no data"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := UpPercent(now, c.created, c.current, c.events, 24*time.Hour)
			if got != c.want {
				t.Fatalf("UpPercent = %q, want %q", got, c.want)
			}
			if c.name == "a minute late every hour" {
				cells := HourCells(now, c.created, c.current, c.events)
				for i, cell := range cells {
					if cell != "late" {
						t.Fatalf("cell %d = %q, want late: the bar shows the worst state, the legend the time", i, cell)
					}
				}
			}
		})
	}
}
