// Package timefmt renders times the way the UI and the CLI write them:
// relative first, absolute in the monitor's timezone on request.
package timefmt

import (
	"strconv"
	"time"
)

// Span renders a duration as the UI writes it: "41 s", "3 min", "21 h", "2 d".
func Span(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	switch {
	case d < time.Minute:
		return strconv.Itoa(int(d.Seconds())) + " s"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + " min"
	case d < 48*time.Hour:
		return strconv.Itoa(int(d.Hours())) + " h"
	default:
		return strconv.Itoa(int(d.Hours()/24)) + " d"
	}
}

// Ago renders "3 min ago" or "just now".
func Ago(t, now time.Time) string {
	d := now.Sub(t)
	if d < 5*time.Second {
		return "just now"
	}
	return Span(d) + " ago"
}

// In renders "in 21 h", or "3 min late" when t has passed.
func In(t, now time.Time) string {
	if t.Before(now) {
		return Span(now.Sub(t)) + " late"
	}
	return "in " + Span(t.Sub(now))
}

// For renders "for 4 min", for state pills.
func For(since, now time.Time) string {
	return "for " + Span(now.Sub(since))
}

// Abs renders the absolute time in loc, for hover titles.
func Abs(t time.Time, loc *time.Location) string {
	if loc == nil {
		loc = time.UTC
	}
	return t.In(loc).Format("2006-01-02 15:04 MST")
}

// Clock renders the time of day in loc.
func Clock(t time.Time, loc *time.Location) string {
	if loc == nil {
		loc = time.UTC
	}
	return t.In(loc).Format("15:04:05")
}

// RunDuration renders a job duration: "4m12s", "850 ms".
func RunDuration(ms int64) string {
	if ms < 1000 {
		return strconv.FormatInt(ms, 10) + " ms"
	}
	d := time.Duration(ms) * time.Millisecond
	return d.Truncate(time.Second).String()
}
