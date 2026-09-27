package view

import (
	"testing"
	"time"
)

func TestTimes(t *testing.T) {
	now := time.Date(2026, 9, 27, 14, 7, 42, 0, time.UTC)
	cases := map[string]string{
		Ago(now.Add(-2*time.Second), now):  "just now",
		Ago(now.Add(-41*time.Second), now): "41 s ago",
		Ago(now.Add(-2*time.Minute), now):  "2 min ago",
		Ago(now.Add(-3*time.Hour), now):    "3 h ago",
		Ago(now.Add(-50*time.Hour), now):   "2 d ago",
		In(now.Add(21*time.Hour), now):     "in 21 h",
		In(now.Add(-3*time.Minute), now):   "3 min late",
		For(now.Add(-4*time.Minute), now):  "for 4 min",
		RunDuration(252000):                "4m12s",
		RunDuration(850):                   "850 ms",
		Clock(now, time.UTC):               "14:07:42",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("got %q want %q", got, want)
		}
	}
	ams, _ := time.LoadLocation("Europe/Amsterdam")
	if got := Abs(now, ams); got != "2026-09-27 16:07 CEST" {
		t.Errorf("Abs = %q", got)
	}
	if got := Abs(now, nil); got != "2026-09-27 14:07 UTC" {
		t.Errorf("Abs nil = %q", got)
	}
}
