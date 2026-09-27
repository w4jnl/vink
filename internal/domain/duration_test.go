package domain

import (
	"testing"
	"time"
)

func TestParseDuration(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
		err  bool
	}{
		{"60s", 60 * time.Second, false},
		{"5m", 5 * time.Minute, false},
		{"2h", 2 * time.Hour, false},
		{"1d", 24 * time.Hour, false},
		{"365d", 365 * 24 * time.Hour, false},
		{"1d12h", 36 * time.Hour, false},
		{"1h30m", 90 * time.Minute, false},
		{"500ms", 500 * time.Millisecond, false},
		{" 3m ", 3 * time.Minute, false},
		{"", 0, true},
		{"abc", 0, true},
		{"-5m", 0, true},
		{"d", 0, true},
		{"1.5d", 0, true},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, err := ParseDuration(c.in)
			if c.err {
				if err == nil {
					t.Fatalf("ParseDuration(%q) = %v, want error", c.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseDuration(%q): %v", c.in, err)
			}
			if got.Std() != c.want {
				t.Fatalf("ParseDuration(%q) = %v, want %v", c.in, got.Std(), c.want)
			}
		})
	}
}

func TestDurationString(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{0, "0s"},
		{45 * time.Second, "45s"},
		{60 * time.Second, "1m"},
		{90 * time.Second, "90s"},
		{90 * time.Minute, "90m"},
		{36 * time.Hour, "36h"},
		{48 * time.Hour, "2d"},
		{250 * time.Millisecond, "250ms"},
	}
	for _, c := range cases {
		if got := Duration(c.in).String(); got != c.want {
			t.Errorf("Duration(%v).String() = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDurationRoundTrip(t *testing.T) {
	for _, s := range []string{"45s", "5m", "1d", "36h", "90m"} {
		var d Duration
		if err := d.UnmarshalText([]byte(s)); err != nil {
			t.Fatal(err)
		}
		out, _ := d.MarshalText()
		if string(out) != s {
			t.Errorf("round trip %q -> %q", s, out)
		}
	}
}
