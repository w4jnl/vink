package domain

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Duration is a time.Duration that reads and writes the short forms used in
// specs and config: "90s", "5m", "2h", "1d", "1d12h". A plain Go duration is
// accepted too. It marshals back to the shortest exact form.
type Duration time.Duration

// ParseDuration parses s into a Duration. Days are 24 hours.
func ParseDuration(s string) (Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty duration")
	}
	var days int64
	if i := strings.IndexByte(s, 'd'); i > 0 {
		n, err := strconv.ParseInt(s[:i], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		days = n
		s = s[i+1:]
	}
	var rest time.Duration
	if s != "" {
		d, err := time.ParseDuration(s)
		if err != nil {
			return 0, fmt.Errorf("invalid duration %q: %w", s, err)
		}
		rest = d
	}
	total := time.Duration(days)*24*time.Hour + rest
	if total < 0 {
		return 0, fmt.Errorf("negative duration %q", s)
	}
	return Duration(total), nil
}

// MustDuration parses s or panics. For constants and tests.
func MustDuration(s string) Duration {
	d, err := ParseDuration(s)
	if err != nil {
		panic(err)
	}
	return d
}

// Std returns the underlying time.Duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }

// String renders the shortest exact form: 2d, 36h, 90m, 45s, or the Go form
// for sub-second values.
func (d Duration) String() string {
	v := time.Duration(d)
	switch {
	case v == 0:
		return "0s"
	case v%(24*time.Hour) == 0:
		return strconv.FormatInt(int64(v/(24*time.Hour)), 10) + "d"
	case v%time.Hour == 0:
		return strconv.FormatInt(int64(v/time.Hour), 10) + "h"
	case v%time.Minute == 0:
		return strconv.FormatInt(int64(v/time.Minute), 10) + "m"
	case v%time.Second == 0:
		return strconv.FormatInt(int64(v/time.Second), 10) + "s"
	default:
		return v.String()
	}
}

// MarshalText implements encoding.TextMarshaler.
func (d Duration) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler.
func (d *Duration) UnmarshalText(b []byte) error {
	v, err := ParseDuration(string(b))
	if err != nil {
		return err
	}
	*d = v
	return nil
}
