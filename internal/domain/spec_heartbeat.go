package domain

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/w4jnl/vink/internal/schedule"
)

// Schedule is exactly one of period or cron. OnCalendar is phase 1.
type Schedule struct {
	Period Duration `json:"period,omitempty" yaml:"period,omitempty"`
	Cron   string   `json:"cron,omitempty" yaml:"cron,omitempty"`
}

// IsZero reports whether neither form is set.
func (s Schedule) IsZero() bool { return s.Period == 0 && s.Cron == "" }

func (s Schedule) toSchedule() schedule.Schedule {
	return schedule.Schedule{Period: s.Period.Std(), Cron: strings.TrimSpace(s.Cron)}
}

// String renders the schedule for lists and logs: "every 1h" or "cron 0 3 * * *".
func (s Schedule) String() string {
	if s.Period != 0 {
		return "every " + s.Period.String()
	}
	return "cron " + s.Cron
}

// Defaults for heartbeat specs, from the design document.
const (
	DefaultTolerance  = Duration(30 * time.Second)
	DefaultGrace      = Duration(5 * time.Minute)
	MinGrace          = Duration(60 * time.Second)
	MaxGrace          = Duration(365 * 24 * time.Hour)
	DefaultThreshold  = 1
	MaxThreshold      = 100
	MaxRuntimeCeiling = Duration(30 * 24 * time.Hour)
)

// AllowedMethods are the HTTP methods a ping may use.
var AllowedMethods = []string{"GET", "POST", "HEAD", "PUT"}

// HeartbeatSpec is the kind-specific part of a heartbeat monitor.
type HeartbeatSpec struct {
	Schedule Schedule `json:"schedule" yaml:"schedule"`
	// Timezone is an IANA name; empty means the project timezone.
	Timezone string `json:"timezone,omitempty" yaml:"timezone,omitempty"`
	// Tolerance is how long after the expected time a ping still counts as
	// on time; `late` starts when it runs out. Never more than Grace.
	Tolerance Duration `json:"tolerance,omitempty" yaml:"tolerance,omitempty"`
	// Grace is how long after the expected time `late` becomes `down`.
	Grace Duration `json:"grace" yaml:"grace"`
	// MaxRuntime turns a start without a finish into a synthetic fail.
	MaxRuntime Duration `json:"max_runtime,omitempty" yaml:"max_runtime,omitempty"`
	// FailureThreshold is the number of consecutive fail signals before down.
	FailureThreshold int `json:"failure_threshold" yaml:"failure_threshold"`
	// RecoveryThreshold is the number of consecutive ok signals to leave down.
	RecoveryThreshold int `json:"recovery_threshold" yaml:"recovery_threshold"`
	// Methods restricts ping methods; empty allows all of AllowedMethods.
	Methods []string `json:"methods,omitempty" yaml:"methods,omitempty"`
	// BodyLimit caps the captured body; 0 means the instance default.
	BodyLimit int64 `json:"body_limit,omitempty" yaml:"body_limit,omitempty"`
}

// Normalize fills defaults and canonicalises fields. Call before Validate.
func (s *HeartbeatSpec) Normalize() {
	if s.Grace == 0 {
		s.Grace = DefaultGrace
	}
	if s.Tolerance == 0 {
		s.Tolerance = DefaultTolerance
	}
	if s.FailureThreshold == 0 {
		s.FailureThreshold = DefaultThreshold
	}
	if s.RecoveryThreshold == 0 {
		s.RecoveryThreshold = DefaultThreshold
	}
	s.Schedule.Cron = strings.TrimSpace(s.Schedule.Cron)
	s.Timezone = strings.TrimSpace(s.Timezone)
	for i, m := range s.Methods {
		s.Methods[i] = strings.ToUpper(strings.TrimSpace(m))
	}
}

// Validate checks the spec. Field names match the JSON representation.
func (s HeartbeatSpec) Validate() error {
	ve := &ValidationError{}
	if err := s.Schedule.toSchedule().Validate(); err != nil {
		ve.Add("schedule", err.Error())
	}
	if s.Timezone != "" && !ValidTimezone(s.Timezone) {
		ve.Addf("timezone", "unknown timezone %q", s.Timezone)
	}
	if s.Grace < MinGrace {
		ve.Addf("grace", "must be at least %s", MinGrace)
	} else if s.Grace > MaxGrace {
		ve.Addf("grace", "must be at most %s", MaxGrace)
	}
	if s.Tolerance < 0 {
		ve.Add("tolerance", "must not be negative")
	} else if s.Tolerance > s.Grace {
		ve.Addf("tolerance", "must be at most the grace (%s)", s.Grace)
	}
	if s.MaxRuntime < 0 || s.MaxRuntime > MaxRuntimeCeiling {
		ve.Addf("max_runtime", "must be between 0 and %s", MaxRuntimeCeiling)
	}
	if s.FailureThreshold < 1 || s.FailureThreshold > MaxThreshold {
		ve.Addf("failure_threshold", "must be between 1 and %d", MaxThreshold)
	}
	if s.RecoveryThreshold < 1 || s.RecoveryThreshold > MaxThreshold {
		ve.Addf("recovery_threshold", "must be between 1 and %d", MaxThreshold)
	}
	for _, m := range s.Methods {
		if !s.methodAllowed(m) {
			ve.Addf("methods", "%q is not one of %s", m, strings.Join(AllowedMethods, ", "))
		}
	}
	if s.BodyLimit < 0 {
		ve.Add("body_limit", "must not be negative")
	}
	return ve.OrNil()
}

func (s HeartbeatSpec) methodAllowed(m string) bool {
	for _, a := range AllowedMethods {
		if a == m {
			return true
		}
	}
	return false
}

// AcceptsMethod reports whether a ping with this HTTP method is accepted.
func (s HeartbeatSpec) AcceptsMethod(m string) bool {
	if len(s.Methods) == 0 {
		return s.methodAllowed(m)
	}
	for _, allowed := range s.Methods {
		if allowed == m {
			return true
		}
	}
	return false
}

// Location resolves the spec's timezone, falling back to the project's.
func (s HeartbeatSpec) Location(projectTZ string) (*time.Location, error) {
	name := s.Timezone
	if name == "" {
		name = projectTZ
	}
	if name == "" {
		return time.UTC, nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("timezone %q: %w", name, err)
	}
	return loc, nil
}

// ExpectedAfter returns the next expected ping strictly after `after`.
func (s HeartbeatSpec) ExpectedAfter(after time.Time, loc *time.Location) (time.Time, error) {
	return schedule.Next(s.Schedule.toSchedule(), after, loc)
}

// ParseHeartbeatSpec decodes, normalises and validates a stored spec.
func ParseHeartbeatSpec(raw []byte) (*HeartbeatSpec, error) {
	var s HeartbeatSpec
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("decode heartbeat spec: %w", err)
	}
	s.Normalize()
	return &s, nil
}
