package domain

import (
	"strings"
	"testing"
	"time"
)

func ams(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Amsterdam")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestWeeklyOccurrence(t *testing.T) {
	loc := ams(t)
	w := &Maintenance{Name: "patching", Weekly: true, Days: []time.Weekday{time.Saturday}, From: "01:00", To: "03:00", Timezone: "Europe/Amsterdam"}
	w.Normalize()
	if err := w.Validate(); err != nil {
		t.Fatal(err)
	}
	thu := time.Date(2026, 10, 1, 12, 0, 0, 0, loc)
	s, e, ok := w.Occurrence(thu)
	if !ok || !s.Equal(time.Date(2026, 10, 3, 1, 0, 0, 0, loc)) || !e.Equal(time.Date(2026, 10, 3, 3, 0, 0, 0, loc)) {
		t.Fatalf("next: %v %v %v", s, e, ok)
	}
	if _, active := w.ActiveAt(thu); active {
		t.Fatal("not active on Thursday")
	}
	inside := time.Date(2026, 10, 3, 2, 0, 0, 0, loc)
	until, active := w.ActiveAt(inside)
	if !active || !until.Equal(e) {
		t.Fatalf("inside: %v %v", until, active)
	}
	// End now skips this occurrence and finds next week's
	w.EndedUntil = &e
	if _, active := w.ActiveAt(inside); active {
		t.Fatal("ended occurrence still active")
	}
	s2, _, ok := w.Occurrence(inside)
	if !ok || !s2.Equal(time.Date(2026, 10, 10, 1, 0, 0, 0, loc)) {
		t.Fatalf("after end now: %v %v", s2, ok)
	}
	if w.RRule() != "FREQ=WEEKLY;BYDAY=SA" {
		t.Errorf("rrule: %s", w.RRule())
	}
	days, err := ParseRRule("FREQ=WEEKLY;BYDAY=SU,MO")
	if err != nil || len(days) != 2 || days[0] != time.Sunday {
		t.Errorf("parse rrule: %v %v", days, err)
	}
	if _, err := ParseRRule("FREQ=DAILY"); err == nil {
		t.Error("daily must be rejected")
	}
}

func TestWeeklyOccurrenceCrossesMidnight(t *testing.T) {
	loc := ams(t)
	w := &Maintenance{Name: "night", Weekly: true, Days: []time.Weekday{time.Friday}, From: "23:00", To: "01:00", Timezone: "Europe/Amsterdam"}
	w.Normalize()
	sat0030 := time.Date(2026, 10, 3, 0, 30, 0, 0, loc)
	until, active := w.ActiveAt(sat0030)
	if !active || !until.Equal(time.Date(2026, 10, 3, 1, 0, 0, 0, loc)) {
		t.Fatalf("crossing midnight: %v %v", until, active)
	}
	sat0130 := time.Date(2026, 10, 3, 1, 30, 0, 0, loc)
	s, _, ok := w.Occurrence(sat0130)
	if !ok || !s.Equal(time.Date(2026, 10, 9, 23, 0, 0, 0, loc)) {
		t.Fatalf("next after: %v %v", s, ok)
	}
}

func TestOnceWindowAndValidation(t *testing.T) {
	start := time.Date(2026, 9, 29, 19, 0, 0, 0, time.UTC)
	end := start.Add(2 * time.Hour)
	w := &Maintenance{Name: "NAS disk swap", MatchTags: []string{"Homelab"}, StartsAt: &start, EndsAt: &end}
	w.Normalize()
	if err := w.Validate(); err != nil || w.MatchTags[0] != "homelab" {
		t.Fatalf("valid once window: %v %v", err, w.MatchTags)
	}
	if _, active := w.ActiveAt(start.Add(-time.Minute)); active {
		t.Error("before start")
	}
	if until, active := w.ActiveAt(start.Add(time.Minute)); !active || !until.Equal(end) {
		t.Error("inside")
	}
	if _, _, ok := w.Occurrence(end); ok {
		t.Error("past windows have no occurrence")
	}
	m := &Monitor{Tags: []string{"homelab", "prod"}}
	if !w.Covers(m) || w.Covers(&Monitor{Tags: []string{"prod"}}) {
		t.Error("covers by all-of tags")
	}
	cases := []struct {
		name  string
		w     Maintenance
		field string
	}{
		{"no name", Maintenance{StartsAt: &start, EndsAt: &end}, "name"},
		{"no end", Maintenance{Name: "x", StartsAt: &start}, "ends_at"},
		{"end before start", Maintenance{Name: "x", StartsAt: &end, EndsAt: &start}, "ends_at"},
		{"too long", Maintenance{Name: "x", StartsAt: &start, EndsAt: ptrTime(start.Add(31 * 24 * time.Hour))}, "ends_at"},
		{"weekly no days", Maintenance{Name: "x", Weekly: true, From: "01:00", To: "02:00"}, "days"},
		{"weekly bad from", Maintenance{Name: "x", Weekly: true, Days: []time.Weekday{time.Monday}, From: "1am", To: "02:00"}, "from"},
		{"weekly same", Maintenance{Name: "x", Weekly: true, Days: []time.Weekday{time.Monday}, From: "01:00", To: "01:00"}, "to"},
		{"bad tz", Maintenance{Name: "x", StartsAt: &start, EndsAt: &end, Timezone: "Mars/Olympus"}, "timezone"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := c.w
			w.Normalize()
			err := w.Validate()
			if err == nil || !strings.Contains(err.Error(), c.field) {
				t.Fatalf("want error on %s, got %v", c.field, err)
			}
		})
	}
}

func ptrTime(t time.Time) *time.Time { return &t }
