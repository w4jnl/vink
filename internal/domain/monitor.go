package domain

import (
	"encoding/json"
	"fmt"
	"time"
)

// Monitor is one heartbeat or pull check with its current state.
type Monitor struct {
	ID        string
	ProjectID string
	OrgID     string
	Slug      string
	Name      string
	Kind      Kind
	Tags      []string
	// Heartbeat is set when Kind is heartbeat.
	Heartbeat *HeartbeatSpec

	State      State
	StateSince time.Time
	// BaseAt is the instant the next expected ping is computed from: the
	// creation time, the last ok, or the resume time.
	BaseAt     time.Time
	LastObsAt  *time.Time
	LastOkAt   *time.Time
	NextDueAt  *time.Time
	Paused     bool
	FailStreak int
	OkStreak   int
	// RunStartedAt and RunID track an open start signal for max_runtime.
	RunStartedAt *time.Time
	RunID        string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Validate checks the identity fields and the kind-specific spec.
func (m *Monitor) Validate() error {
	ve := &ValidationError{}
	if !ValidSlug(m.Slug) {
		ve.Add("slug", "must be lowercase letters, digits and dashes, at most 64 characters")
	}
	if m.Name == "" {
		ve.Add("name", "must not be empty")
	} else if len([]rune(m.Name)) > MaxNameLen {
		ve.Addf("name", "at most %d characters", MaxNameLen)
	}
	if !m.Kind.Valid() {
		ve.Addf("kind", "unknown kind %q", string(m.Kind))
	} else if m.Kind != KindHeartbeat {
		ve.Addf("kind", "%s monitors arrive in phase 1", string(m.Kind))
	}
	ValidateTags(ve, "tags", m.Tags)
	if m.Kind == KindHeartbeat {
		if m.Heartbeat == nil {
			ve.Add("schedule", "set period or cron")
		} else if err := m.Heartbeat.Validate(); err != nil {
			if sub, ok := AsValidation(err); ok {
				ve.Errors = append(ve.Errors, sub.Errors...)
			} else {
				ve.Add("spec", err.Error())
			}
		}
	}
	return ve.OrNil()
}

// SpecJSON encodes the kind-specific spec for storage.
func (m *Monitor) SpecJSON() ([]byte, error) {
	switch m.Kind {
	case KindHeartbeat:
		if m.Heartbeat == nil {
			return nil, fmt.Errorf("heartbeat monitor %s has no spec", m.Slug)
		}
		return json.Marshal(m.Heartbeat)
	default:
		return nil, fmt.Errorf("kind %s not supported yet", m.Kind)
	}
}

// SetSpecJSON decodes a stored spec according to the kind.
func (m *Monitor) SetSpecJSON(raw []byte) error {
	switch m.Kind {
	case KindHeartbeat:
		s, err := ParseHeartbeatSpec(raw)
		if err != nil {
			return err
		}
		m.Heartbeat = s
		return nil
	default:
		return fmt.Errorf("kind %s not supported yet", m.Kind)
	}
}

// TagsJSON encodes tags for storage.
func (m *Monitor) TagsJSON() string {
	b, _ := json.Marshal(NormalizeTags(m.Tags))
	if b == nil {
		return "[]"
	}
	return string(b)
}

// ParseTags decodes a stored tag list.
func ParseTags(raw string) []string {
	var tags []string
	if err := json.Unmarshal([]byte(raw), &tags); err != nil {
		return nil
	}
	return tags
}

// HasAllTags reports whether the monitor carries every tag in want. An
// empty want matches everything.
func (m *Monitor) HasAllTags(want []string) bool {
	for _, w := range want {
		found := false
		for _, t := range m.Tags {
			if t == w {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// Observation is one ping, local check result or agent result.
type Observation struct {
	ID         string
	MonitorID  string
	ProjectID  string
	At         time.Time
	Source     string
	Signal     Signal
	OK         bool
	LatencyMs  *int64
	ExitCode   *int64
	DurationMs *int64
	RunID      string
	RemoteAddr string
	UserAgent  string
	HasBody    bool
	Detail     map[string]any
}

// Event is one state flip.
type Event struct {
	ID            string
	MonitorID     string
	ProjectID     string
	At            time.Time
	From          State
	To            State
	Reason        string
	ObservationID string
}

// Incident is opened on down and closed on up.
type Incident struct {
	ID          string
	MonitorID   string
	MonitorSlug string
	MonitorName string
	MonitorTags []string
	// Reason is the opening event's reason.
	Reason       string
	ProjectID    string
	OpenedAt     time.Time
	ResolvedAt   *time.Time
	AckedBy      string
	AckedAt      *time.Time
	OpenEventID  string
	CloseEventID string
}

// Open reports whether the incident is unresolved.
func (i *Incident) Open() bool { return i.ResolvedAt == nil }
