// Package view builds the values templates render: relative and absolute
// times in the monitor's timezone and uptime cells.
package view

import (
	"time"

	"github.com/w4jnl/vink/internal/timefmt"
)

// Ago renders "3 min ago".
func Ago(t, now time.Time) string { return timefmt.Ago(t, now) }

// In renders "in 21 h" or "3 min late".
func In(t, now time.Time) string { return timefmt.In(t, now) }

// For renders "for 4 min".
func For(since, now time.Time) string { return timefmt.For(since, now) }

// Span renders a duration: "41 s", "3 min", "21 h", "2 d".
func Span(d time.Duration) string { return timefmt.Span(d) }

// Abs renders the absolute time in loc.
func Abs(t time.Time, loc *time.Location) string { return timefmt.Abs(t, loc) }

// Clock renders the time of day in loc.
func Clock(t time.Time, loc *time.Location) string { return timefmt.Clock(t, loc) }

// RunDuration renders a job duration.
func RunDuration(ms int64) string { return timefmt.RunDuration(ms) }
