// Package domain holds the types shared by every layer: monitors, specs,
// observations, states, events, incidents, scopes and roles. It depends on
// nothing inside the repository except internal/schedule.
package domain

import (
	"time"

	"github.com/oklog/ulid/v2"
)

// NewID returns a new ULID string. ULIDs sort by creation time.
func NewID() string { return ulid.Make().String() }

// IsID reports whether s is a well-formed ULID.
func IsID(s string) bool {
	_, err := ulid.ParseStrict(s)
	return err == nil
}

// Millis converts a time to the Unix millisecond form stored in SQLite.
func Millis(t time.Time) int64 { return t.UnixMilli() }

// MillisPtr converts an optional time.
func MillisPtr(t *time.Time) *int64 {
	if t == nil {
		return nil
	}
	v := t.UnixMilli()
	return &v
}

// FromMillis converts a stored Unix millisecond value to a UTC time.
func FromMillis(ms int64) time.Time { return time.UnixMilli(ms).UTC() }

// FromMillisPtr converts an optional stored value.
func FromMillisPtr(ms *int64) *time.Time {
	if ms == nil {
		return nil
	}
	t := time.UnixMilli(*ms).UTC()
	return &t
}
