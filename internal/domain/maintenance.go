package domain

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// MaxWindow bounds a one-off maintenance window.
const MaxWindow = 30 * 24 * time.Hour

// Maintenance is a window in which the monitors carrying its tags keep
// recording but never go down and never alert. A one-off window runs
// from StartsAt to EndsAt; a weekly one repeats on Days between From and
// To in Timezone, crossing midnight when To is not after From.
type Maintenance struct {
	ID        string
	ProjectID string
	Name      string
	MatchTags []string
	StartsAt  *time.Time
	EndsAt    *time.Time
	Weekly    bool
	Days      []time.Weekday
	From      string
	To        string
	Timezone  string
	// EndedUntil is set by End now: occurrences ending at or before it
	// are skipped.
	EndedUntil *time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

var weekdayCodes = []struct {
	code string
	day  time.Weekday
}{{"MO", time.Monday}, {"TU", time.Tuesday}, {"WE", time.Wednesday}, {"TH", time.Thursday}, {"FR", time.Friday}, {"SA", time.Saturday}, {"SU", time.Sunday}}

// Normalize trims and orders the fields. Call before Validate.
func (w *Maintenance) Normalize() {
	w.Name = strings.TrimSpace(w.Name)
	w.MatchTags = NormalizeTags(w.MatchTags)
	w.Timezone = strings.TrimSpace(w.Timezone)
	w.From = strings.TrimSpace(w.From)
	w.To = strings.TrimSpace(w.To)
	seen := map[time.Weekday]bool{}
	days := w.Days[:0]
	for _, d := range w.Days {
		if !seen[d] && d >= time.Sunday && d <= time.Saturday {
			seen[d] = true
			days = append(days, d)
		}
	}
	// Monday first, Sunday last
	sort.Slice(days, func(i, j int) bool { return weekIndex(days[i]) < weekIndex(days[j]) })
	w.Days = days
}

func weekIndex(d time.Weekday) int {
	if d == time.Sunday {
		return 7
	}
	return int(d)
}

// Validate checks the window. Field names match the JSON form.
func (w *Maintenance) Validate() error {
	ve := &ValidationError{}
	if w.Name == "" {
		ve.Add("name", "must not be empty")
	} else if len([]rune(w.Name)) > MaxNameLen {
		ve.Addf("name", "at most %d characters", MaxNameLen)
	}
	ValidateTags(ve, "match_tags", w.MatchTags)
	if w.Timezone != "" && !ValidTimezone(w.Timezone) {
		ve.Addf("timezone", "unknown timezone %q", w.Timezone)
	}
	if w.Weekly {
		if len(w.Days) == 0 {
			ve.Add("days", "pick at least one day")
		}
		if _, _, err := ParseClock(w.From); err != nil {
			ve.Add("from", "must be a time such as 02:00")
		}
		if _, _, err := ParseClock(w.To); err != nil {
			ve.Add("to", "must be a time such as 04:00")
		} else if w.To == w.From {
			ve.Add("to", "must differ from the start")
		}
	} else {
		switch {
		case w.StartsAt == nil:
			ve.Add("starts_at", "set when the window starts")
		case w.EndsAt == nil:
			ve.Add("ends_at", "set when the window ends")
		case !w.EndsAt.After(*w.StartsAt):
			ve.Add("ends_at", "must be after the start")
		case w.EndsAt.Sub(*w.StartsAt) > MaxWindow:
			ve.Add("ends_at", "a window lasts at most 30d")
		}
	}
	return ve.OrNil()
}

// ParseClock reads "HH:MM".
func ParseClock(s string) (hour, minute int, err error) {
	t, err := time.Parse("15:04", strings.TrimSpace(s))
	if err != nil {
		return 0, 0, fmt.Errorf("invalid time %q", s)
	}
	return t.Hour(), t.Minute(), nil
}

// RRule renders the weekly rule: "FREQ=WEEKLY;BYDAY=SA,SU". Empty for a one-off window.
func (w *Maintenance) RRule() string {
	if !w.Weekly {
		return ""
	}
	codes := make([]string, 0, len(w.Days))
	for _, d := range w.Days {
		for _, c := range weekdayCodes {
			if c.day == d {
				codes = append(codes, c.code)
			}
		}
	}
	return "FREQ=WEEKLY;BYDAY=" + strings.Join(codes, ",")
}

// ParseRRule reads the one rule vink supports: FREQ=WEEKLY with BYDAY.
func ParseRRule(s string) ([]time.Weekday, error) {
	var days []time.Weekday
	freq := ""
	for _, part := range strings.Split(strings.TrimSpace(s), ";") {
		k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
		switch strings.ToUpper(k) {
		case "FREQ":
			freq = strings.ToUpper(v)
		case "BYDAY":
			for _, code := range strings.Split(v, ",") {
				code = strings.ToUpper(strings.TrimSpace(code))
				found := false
				for _, c := range weekdayCodes {
					if c.code == code {
						days = append(days, c.day)
						found = true
					}
				}
				if !found {
					return nil, fmt.Errorf("rrule: unknown day %q", code)
				}
			}
		case "":
		default:
			return nil, fmt.Errorf("rrule: %s is not supported", k)
		}
	}
	if freq != "WEEKLY" {
		return nil, fmt.Errorf("rrule: only FREQ=WEEKLY is supported")
	}
	if len(days) == 0 {
		return nil, fmt.Errorf("rrule: BYDAY must list days")
	}
	return days, nil
}

// Location resolves the window's timezone, UTC when unknown.
func (w *Maintenance) Location() *time.Location {
	if loc, err := time.LoadLocation(w.Timezone); err == nil && w.Timezone != "" {
		return loc
	}
	return time.UTC
}

func (w *Maintenance) hasDay(d time.Weekday) bool {
	for _, x := range w.Days {
		if x == d {
			return true
		}
	}
	return false
}

// Occurrence returns the occurrence that is running at now or the next
// one to start, and whether there is any.
func (w *Maintenance) Occurrence(now time.Time) (start, end time.Time, ok bool) {
	if !w.Weekly {
		if w.StartsAt == nil || w.EndsAt == nil || !w.EndsAt.After(now) {
			return start, end, false
		}
		if w.EndedUntil != nil && !w.EndsAt.After(*w.EndedUntil) {
			return start, end, false
		}
		return *w.StartsAt, *w.EndsAt, true
	}
	fh, fm, err := ParseClock(w.From)
	if err != nil {
		return start, end, false
	}
	th, tm, err := ParseClock(w.To)
	if err != nil {
		return start, end, false
	}
	loc := w.Location()
	local := now.In(loc)
	for i := -1; i <= 7; i++ {
		day := time.Date(local.Year(), local.Month(), local.Day()+i, 0, 0, 0, 0, loc)
		if !w.hasDay(day.Weekday()) {
			continue
		}
		s := time.Date(day.Year(), day.Month(), day.Day(), fh, fm, 0, 0, loc)
		e := time.Date(day.Year(), day.Month(), day.Day(), th, tm, 0, 0, loc)
		if !e.After(s) {
			e = time.Date(day.Year(), day.Month(), day.Day()+1, th, tm, 0, 0, loc)
		}
		if !e.After(now) || (w.EndedUntil != nil && !e.After(*w.EndedUntil)) {
			continue
		}
		if !ok || s.Before(start) {
			start, end, ok = s, e, true
		}
	}
	return start, end, ok
}

// ActiveAt reports whether the window covers now, and until when.
func (w *Maintenance) ActiveAt(now time.Time) (until time.Time, active bool) {
	s, e, ok := w.Occurrence(now)
	if !ok || now.Before(s) {
		return time.Time{}, false
	}
	return e, true
}

// Covers reports whether the monitor carries every tag of the window.
func (w *Maintenance) Covers(m *Monitor) bool { return m.HasAllTags(w.MatchTags) }
