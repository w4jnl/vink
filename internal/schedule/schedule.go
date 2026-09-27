// Package schedule computes when a heartbeat is next expected: a fixed
// period, or a five-field cron expression evaluated in the monitor's
// location. DST is handled as the design document says: a wall-clock time
// that does not exist on a spring-forward day is skipped, and a fixed
// wall-clock time that occurs twice on a fall-back day fires once. Cron
// expressions with a wildcard hour keep running every real hour, as
// vixie-cron does.
package schedule

import (
	"errors"
	"fmt"
	"strings"
	"time"
	_ "time/tzdata" // per-monitor timezones must work in a FROM scratch image

	"github.com/adhocore/gronx"
)

// Schedule is exactly one of a period or a cron expression.
type Schedule struct {
	Period time.Duration
	Cron   string
}

// MinPeriod is the shortest allowed period.
const MinPeriod = 60 * time.Second

// Validate checks that exactly one form is set and that it is well-formed.
func (s Schedule) Validate() error {
	switch {
	case s.Period != 0 && s.Cron != "":
		return errors.New("set either period or cron, not both")
	case s.Period == 0 && s.Cron == "":
		return errors.New("set period or cron")
	case s.Period != 0:
		if s.Period < MinPeriod {
			return fmt.Errorf("period must be at least %s", MinPeriod)
		}
		return nil
	default:
		return ValidateCron(s.Cron)
	}
}

// ValidateCron accepts five-field expressions (names, ranges, steps) and the
// @hourly, @daily, @weekly, @monthly and @yearly shorthands.
func ValidateCron(expr string) error {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return errors.New("cron expression is empty")
	}
	if strings.HasPrefix(expr, "@") {
		switch strings.ToLower(expr) {
		case "@hourly", "@daily", "@midnight", "@weekly", "@monthly", "@yearly", "@annually":
			return nil
		}
		return fmt.Errorf("unknown cron shorthand %q", expr)
	}
	if n := len(strings.Fields(expr)); n != 5 {
		return fmt.Errorf("cron expression must have 5 fields, got %d", n)
	}
	if !gronx.IsValid(expr) {
		return fmt.Errorf("invalid cron expression %q", expr)
	}
	return nil
}

// Next returns the first occurrence strictly after `after`, in loc.
func Next(s Schedule, after time.Time, loc *time.Location) (time.Time, error) {
	if loc == nil {
		loc = time.UTC
	}
	if s.Period != 0 {
		return after.Add(s.Period), nil
	}
	return nextCron(s.Cron, after, loc)
}

func nextCron(expr string, after time.Time, loc *time.Location) (time.Time, error) {
	expr = strings.TrimSpace(expr)
	fixedHour := hourIsFixed(expr)
	g := gronx.New()
	cursor := after.In(loc).Truncate(time.Second)
	for range 64 {
		n, err := gronxNext(g, expr, cursor, after, loc)
		if err != nil {
			return time.Time{}, err
		}
		// gronx steps through wall-clock fields, so around a fall-back
		// transition it can miss the first of two identical wall-clock
		// minutes, or the repeated real hour of a wildcard expression.
		// Scan those windows for anything due before n.
		if t, ok, err := missedInFallBack(g, expr, after, n, loc, fixedHour); err != nil {
			return time.Time{}, err
		} else if ok {
			return t, nil
		}
		if fixedHour && isRepeatedWallClock(n) {
			cursor = n
			continue
		}
		return n, nil
	}
	return time.Time{}, fmt.Errorf("cron %q: no occurrence found after %s", expr, after)
}

// gronxNext returns gronx's next candidate strictly after `after` that
// really matches expr; a candidate normalised across a DST gap may not.
func gronxNext(g *gronx.Gronx, expr string, cursor, after time.Time, loc *time.Location) (time.Time, error) {
	for range 64 {
		n, err := gronx.NextTickAfter(expr, cursor, false)
		if err != nil {
			return time.Time{}, fmt.Errorf("cron %q: %w", expr, err)
		}
		n = n.In(loc)
		if !n.After(after) {
			cursor = cursor.Add(time.Minute)
			continue
		}
		due, err := g.IsDue(expr, n)
		if err != nil {
			return time.Time{}, fmt.Errorf("cron %q: %w", expr, err)
		}
		if !due {
			cursor = n
			continue
		}
		return n, nil
	}
	return time.Time{}, fmt.Errorf("cron %q: no occurrence found after %s", expr, after)
}

type transition struct {
	at    time.Time     // first instant of the new offset
	shift time.Duration // how far the clock went back
}

// fallBackTransitions lists the fall-back transitions in (from, to].
func fallBackTransitions(from, to time.Time, loc *time.Location) []transition {
	var out []transition
	step := from.In(loc)
	_, prevOff := step.Zone()
	for step.Before(to) {
		next := step.Add(24 * time.Hour)
		if next.After(to) {
			next = to
		}
		_, off := next.In(loc).Zone()
		if off < prevOff {
			out = append(out, transition{at: findTransition(step, next, off), shift: time.Duration(prevOff-off) * time.Second})
		}
		prevOff = off
		step = next
	}
	return out
}

// findTransition binary-searches the minute in (lo, hi] at which the zone
// offset becomes off.
func findTransition(lo, hi time.Time, off int) time.Time {
	base := lo.Truncate(time.Minute)
	l, h := 0, int(hi.Sub(base)/time.Minute)+1
	for h-l > 1 {
		mid := (l + h) / 2
		if _, o := base.Add(time.Duration(mid) * time.Minute).In(lo.Location()).Zone(); o == off {
			h = mid
		} else {
			l = mid
		}
	}
	return base.Add(time.Duration(h) * time.Minute).In(lo.Location())
}

// missedInFallBack scans the minutes around each fall-back transition
// between after and n. For a fixed-hour expression only the first pass of
// the repeated wall-clock window counts; a wildcard hour counts every
// real minute that matches.
func missedInFallBack(g *gronx.Gronx, expr string, after, n time.Time, loc *time.Location, fixedHour bool) (time.Time, bool, error) {
	for _, tr := range fallBackTransitions(after, n, loc) {
		start := tr.at.Add(-tr.shift)
		end := tr.at.Add(tr.shift)
		if fixedHour {
			end = tr.at
		}
		if !start.After(after) {
			start = after.Truncate(time.Minute).Add(time.Minute)
		}
		for t := start; t.Before(end) && t.Before(n); t = t.Add(time.Minute) {
			due, err := g.IsDue(expr, t.In(loc))
			if err != nil {
				return time.Time{}, false, fmt.Errorf("cron %q: %w", expr, err)
			}
			if due {
				return t.In(loc), true, nil
			}
		}
	}
	return time.Time{}, false, nil
}

// hourIsFixed reports whether the hour field names specific hours, in
// which case a fall-back repeat is suppressed. A wildcard hour, with or
// without a step, runs every real hour.
func hourIsFixed(expr string) bool {
	if strings.HasPrefix(expr, "@") {
		return !strings.EqualFold(expr, "@hourly")
	}
	fields := strings.Fields(expr)
	if len(fields) < 2 {
		return false
	}
	return !strings.HasPrefix(fields[1], "*")
}

// isRepeatedWallClock reports whether t is the second time this wall-clock
// minute occurs today, which happens after a fall-back transition of 30 or
// 60 minutes.
func isRepeatedWallClock(t time.Time) bool {
	const layout = "2006-01-02 15:04"
	wall := t.Format(layout)
	for _, back := range []time.Duration{30 * time.Minute, time.Hour} {
		if t.Add(-back).In(t.Location()).Format(layout) == wall {
			return true
		}
	}
	return false
}
