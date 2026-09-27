package schedule

import (
	"testing"
	"time"
)

func loc(t *testing.T, name string) *time.Location {
	t.Helper()
	l, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// wall parses a wall-clock time in l.
func wall(t *testing.T, l *time.Location, s string) time.Time {
	t.Helper()
	v, err := time.ParseInLocation("2006-01-02 15:04:05", s, l)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func utc(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestNextCronDST(t *testing.T) {
	ams := loc(t, "Europe/Amsterdam")
	nyc := loc(t, "America/New_York")
	lhi := loc(t, "Australia/Lord_Howe")

	cases := []struct {
		name  string
		loc   *time.Location
		cron  string
		after time.Time
		want  time.Time
	}{
		// Europe/Amsterdam: 2026-03-29 02:00 CET -> 03:00 CEST; 2026-10-25 03:00 CEST -> 02:00 CET.
		{"ams spring 02:30 skipped", ams, "30 2 * * *", wall(t, ams, "2026-03-28 12:00:00"), utc(t, "2026-03-30T00:30:00Z")},
		{"ams spring 03:00 exists", ams, "0 3 * * *", wall(t, ams, "2026-03-28 12:00:00"), utc(t, "2026-03-29T01:00:00Z")},
		{"ams spring 02:00 skipped", ams, "0 2 * * *", wall(t, ams, "2026-03-28 12:00:00"), utc(t, "2026-03-30T00:00:00Z")},
		{"ams spring hourly keeps real hours", ams, "0 * * * *", utc(t, "2026-03-29T00:30:00Z"), utc(t, "2026-03-29T01:00:00Z")},
		{"ams spring 01:30 before gap", ams, "30 1 * * *", wall(t, ams, "2026-03-28 12:00:00"), utc(t, "2026-03-29T00:30:00Z")},
		{"ams fall 02:30 first occurrence", ams, "30 2 * * *", wall(t, ams, "2026-10-24 12:00:00"), utc(t, "2026-10-25T00:30:00Z")},
		{"ams fall 02:30 fires once", ams, "30 2 * * *", utc(t, "2026-10-25T00:30:00Z"), utc(t, "2026-10-26T01:30:00Z")},
		{"ams fall 02:30 after first, before repeat", ams, "30 2 * * *", utc(t, "2026-10-25T00:45:00Z"), utc(t, "2026-10-26T01:30:00Z")},
		{"ams fall 02:00 first occurrence", ams, "0 2 * * *", wall(t, ams, "2026-10-24 12:00:00"), utc(t, "2026-10-25T00:00:00Z")},
		{"ams fall 02:00 fires once", ams, "0 2 * * *", utc(t, "2026-10-25T00:00:00Z"), utc(t, "2026-10-26T01:00:00Z")},
		{"ams fall hourly runs the repeated hour", ams, "0 * * * *", utc(t, "2026-10-25T00:30:00Z"), utc(t, "2026-10-25T01:00:00Z")},
		{"ams fall hourly next", ams, "0 * * * *", utc(t, "2026-10-25T01:00:00Z"), utc(t, "2026-10-25T02:00:00Z")},
		{"ams fall every 30 in hour 2, second slot", ams, "*/30 2 * * *", utc(t, "2026-10-25T00:00:00Z"), utc(t, "2026-10-25T00:30:00Z")},
		{"ams fall every 30 in hour 2, repeats skipped", ams, "*/30 2 * * *", utc(t, "2026-10-25T00:30:00Z"), utc(t, "2026-10-26T01:00:00Z")},
		{"ams fall 03:00 once", ams, "0 3 * * *", wall(t, ams, "2026-10-24 12:00:00"), utc(t, "2026-10-25T02:00:00Z")},
		{"ams fall 04:00 unaffected", ams, "0 4 * * *", wall(t, ams, "2026-10-24 12:00:00"), utc(t, "2026-10-25T03:00:00Z")},

		// America/New_York: 2026-03-08 02:00 EST -> 03:00 EDT; 2026-11-01 02:00 EDT -> 01:00 EST.
		{"nyc spring 02:30 skipped", nyc, "30 2 * * *", wall(t, nyc, "2026-03-07 12:00:00"), utc(t, "2026-03-09T06:30:00Z")},
		{"nyc spring 03:00 exists", nyc, "0 3 * * *", wall(t, nyc, "2026-03-07 12:00:00"), utc(t, "2026-03-08T07:00:00Z")},
		{"nyc spring hourly", nyc, "0 * * * *", utc(t, "2026-03-08T06:30:00Z"), utc(t, "2026-03-08T07:00:00Z")},
		{"nyc fall 01:00 first", nyc, "0 1 * * *", wall(t, nyc, "2026-10-31 12:00:00"), utc(t, "2026-11-01T05:00:00Z")},
		{"nyc fall 01:00 fires once", nyc, "0 1 * * *", utc(t, "2026-11-01T05:00:00Z"), utc(t, "2026-11-02T06:00:00Z")},
		{"nyc fall 01:30 first", nyc, "30 1 * * *", wall(t, nyc, "2026-10-31 12:00:00"), utc(t, "2026-11-01T05:30:00Z")},
		{"nyc fall 01:30 once", nyc, "30 1 * * *", utc(t, "2026-11-01T05:30:00Z"), utc(t, "2026-11-02T06:30:00Z")},
		{"nyc fall hourly repeats", nyc, "0 * * * *", utc(t, "2026-11-01T05:00:00Z"), utc(t, "2026-11-01T06:00:00Z")},
		{"nyc fall 02:00 once, after the shift", nyc, "0 2 * * *", wall(t, nyc, "2026-10-31 12:00:00"), utc(t, "2026-11-01T07:00:00Z")},

		// Australia/Lord_Howe: +10:30 standard, +11:00 daylight, 30-minute shift.
		// 2026-04-05 02:00 LHDT -> 01:30 LHST; 2026-10-04 02:00 LHST -> 02:30 LHDT.
		{"lhi spring 02:00 skipped", lhi, "0 2 * * *", wall(t, lhi, "2026-10-03 12:00:00"), utc(t, "2026-10-04T15:00:00Z")},
		{"lhi spring 02:15 skipped", lhi, "15 2 * * *", wall(t, lhi, "2026-10-03 12:00:00"), utc(t, "2026-10-04T15:15:00Z")},
		{"lhi spring 02:45 exists", lhi, "45 2 * * *", wall(t, lhi, "2026-10-03 12:00:00"), utc(t, "2026-10-03T15:45:00Z")},
		{"lhi spring 01:00 before gap", lhi, "0 1 * * *", wall(t, lhi, "2026-10-03 12:00:00"), utc(t, "2026-10-03T14:30:00Z")},
		{"lhi fall 01:45 first", lhi, "45 1 * * *", wall(t, lhi, "2026-04-04 12:00:00"), utc(t, "2026-04-04T14:45:00Z")},
		{"lhi fall 01:45 once", lhi, "45 1 * * *", utc(t, "2026-04-04T14:45:00Z"), utc(t, "2026-04-05T15:15:00Z")},
		{"lhi fall 01:30 first", lhi, "30 1 * * *", wall(t, lhi, "2026-04-04 12:00:00"), utc(t, "2026-04-04T14:30:00Z")},
		{"lhi fall 01:30 once", lhi, "30 1 * * *", utc(t, "2026-04-04T14:30:00Z"), utc(t, "2026-04-05T15:00:00Z")},
		{"lhi fall hourly: next wall-clock XX:00 is 02:00 LHST", lhi, "0 * * * *", utc(t, "2026-04-04T14:00:00Z"), utc(t, "2026-04-04T15:30:00Z")},
		{"lhi fall every 30 min runs the repeated half hour", lhi, "*/30 * * * *", utc(t, "2026-04-04T14:30:00Z"), utc(t, "2026-04-04T15:00:00Z")},
		{"lhi fall every 30 min after the repeat", lhi, "*/30 * * * *", utc(t, "2026-04-04T15:00:00Z"), utc(t, "2026-04-04T15:30:00Z")},
		{"lhi fall 02:00 once, after the shift", lhi, "0 2 * * *", wall(t, lhi, "2026-04-04 12:00:00"), utc(t, "2026-04-04T15:30:00Z")},

		// Ordinary calendar behaviour.
		{"nightly 03:00", ams, "0 3 * * *", wall(t, ams, "2026-09-27 22:00:00"), utc(t, "2026-09-28T01:00:00Z")},
		{"weekday range skips weekend", ams, "0 3 * * 1-5", wall(t, ams, "2026-10-02 04:00:00"), utc(t, "2026-10-05T01:00:00Z")},
		{"every 15 minutes", ams, "*/15 * * * *", wall(t, ams, "2026-09-27 22:07:00"), utc(t, "2026-09-27T20:15:00Z")},
		{"first of month", ams, "0 0 1 * *", wall(t, ams, "2026-02-15 00:00:00"), utc(t, "2026-02-28T23:00:00Z")},
		{"leap day", ams, "0 0 29 2 *", wall(t, ams, "2026-03-01 00:00:00"), utc(t, "2028-02-28T23:00:00Z")},
		{"day name", ams, "0 9 * * mon", wall(t, ams, "2026-10-03 12:00:00"), utc(t, "2026-10-05T07:00:00Z")},
		{"weekly sunday 04:00", ams, "0 4 * * sun", wall(t, ams, "2026-09-27 12:00:00"), utc(t, "2026-10-04T02:00:00Z")},
		{"@daily", ams, "@daily", wall(t, ams, "2026-09-27 12:00:00"), utc(t, "2026-09-27T22:00:00Z")},
		{"@hourly", ams, "@hourly", wall(t, ams, "2026-09-27 12:20:00"), utc(t, "2026-09-27T11:00:00Z")},
		{"month names", ams, "0 12 * jan,jul *", wall(t, ams, "2026-02-01 00:00:00"), utc(t, "2026-07-01T10:00:00Z")},
		{"strictly after the exact occurrence", ams, "0 3 * * *", wall(t, ams, "2026-09-28 03:00:00"), utc(t, "2026-09-29T01:00:00Z")},
		{"seconds after the occurrence", ams, "0 3 * * *", wall(t, ams, "2026-09-28 03:00:30"), utc(t, "2026-09-29T01:00:00Z")},
		{"UTC location", time.UTC, "0 3 * * *", utc(t, "2026-09-27T22:00:00Z"), utc(t, "2026-09-28T03:00:00Z")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Next(Schedule{Cron: c.cron}, c.after, c.loc)
			if err != nil {
				t.Fatalf("Next: %v", err)
			}
			if !got.Equal(c.want) {
				t.Fatalf("Next(%q, after %s) = %s (%s), want %s (%s)",
					c.cron, c.after.In(c.loc).Format(time.RFC3339), got.UTC().Format(time.RFC3339), got.In(c.loc).Format("2006-01-02 15:04 MST"),
					c.want.UTC().Format(time.RFC3339), c.want.In(c.loc).Format("2006-01-02 15:04 MST"))
			}
			if got.Location().String() != c.loc.String() {
				t.Errorf("result location = %s, want %s", got.Location(), c.loc)
			}
		})
	}
}

func TestNextPeriod(t *testing.T) {
	ams := loc(t, "Europe/Amsterdam")
	cases := []struct {
		name   string
		period time.Duration
		after  time.Time
		want   time.Time
	}{
		{"60s", 60 * time.Second, utc(t, "2026-09-27T22:00:00Z"), utc(t, "2026-09-27T22:01:00Z")},
		{"1d across spring forward is 24 real hours", 24 * time.Hour, wall(t, ams, "2026-03-28 12:00:00"), utc(t, "2026-03-29T11:00:00Z")},
		{"1d across fall back is 24 real hours", 24 * time.Hour, wall(t, ams, "2026-10-24 12:00:00"), utc(t, "2026-10-25T10:00:00Z")},
		{"1h on Lord Howe fall back", time.Hour, utc(t, "2026-04-04T14:45:00Z"), utc(t, "2026-04-04T15:45:00Z")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Next(Schedule{Period: c.period}, c.after, ams)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Equal(c.want) {
				t.Fatalf("got %s want %s", got.UTC(), c.want.UTC())
			}
		})
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name string
		s    Schedule
		ok   bool
	}{
		{"period 60s", Schedule{Period: 60 * time.Second}, true},
		{"period 30s too short", Schedule{Period: 30 * time.Second}, false},
		{"period 1d", Schedule{Period: 24 * time.Hour}, true},
		{"cron five fields", Schedule{Cron: "*/5 * * * *"}, true},
		{"cron names", Schedule{Cron: "0 3 * * mon-fri"}, true},
		{"cron six fields", Schedule{Cron: "0 0 3 * * *"}, false},
		{"cron minute out of range", Schedule{Cron: "61 * * * *"}, false},
		{"cron garbage", Schedule{Cron: "every day"}, false},
		{"cron empty", Schedule{Cron: "   "}, false},
		{"shorthand hourly", Schedule{Cron: "@hourly"}, true},
		{"shorthand unknown", Schedule{Cron: "@fortnightly"}, false},
		{"both set", Schedule{Period: time.Hour, Cron: "0 * * * *"}, false},
		{"none set", Schedule{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.s.Validate()
			if c.ok && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !c.ok && err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestNextRejectsBadCron(t *testing.T) {
	if _, err := Next(Schedule{Cron: "bad"}, time.Now(), time.UTC); err == nil {
		t.Fatal("expected error")
	}
}
