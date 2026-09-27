package domain

import (
	"encoding/json"
	"time"
)

// Org is a tenant. Quotas are nil for unlimited.
type Org struct {
	ID            string
	Slug          string
	Name          string
	QuotaMonitors *int64
	QuotaAgents   *int64
	CreatedAt     time.Time
}

// Project owns monitors, keys, channels and routes.
type Project struct {
	ID               string
	OrgID            string
	OrgSlug          string
	Slug             string
	Name             string
	Timezone         string
	PingKey          string
	PingKeyPrev      string
	PingKeyPrevUntil *time.Time
	CreatedAt        time.Time
}

// User is a local or proxy-authenticated person.
type User struct {
	ID            string
	Subject       string
	Email         string
	DisplayName   string
	HasPassword   bool
	InstanceAdmin bool
	CreatedAt     time.Time
}

// Membership ties a user to an org with a role.
type Membership struct {
	UserID  string
	OrgID   string
	OrgSlug string
	OrgName string
	Role    Role
	Source  string
}

// APIKey is a project-scoped bearer token. Plaintext is only ever
// returned once, on creation.
type APIKey struct {
	ID         string
	ProjectID  string
	Name       string
	Prefix     string
	Access     Access
	CreatedBy  string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	RevokedAt  *time.Time
}

// ChannelKind is a notifier kind.
type ChannelKind string

const (
	ChannelSMTP         ChannelKind = "smtp"
	ChannelWebhook      ChannelKind = "webhook"
	ChannelNtfy         ChannelKind = "ntfy"
	ChannelGotify       ChannelKind = "gotify"
	ChannelMatrix       ChannelKind = "matrix"
	ChannelSlackhook    ChannelKind = "slackhook"
	ChannelAlertmanager ChannelKind = "alertmanager"
)

// Valid reports whether k is a known channel kind.
func (k ChannelKind) Valid() bool {
	switch k {
	case ChannelSMTP, ChannelWebhook, ChannelNtfy, ChannelGotify, ChannelMatrix, ChannelSlackhook, ChannelAlertmanager:
		return true
	}
	return false
}

// Channel is a configured notifier. Config is the decrypted JSON.
type Channel struct {
	ID        string
	ProjectID string
	OrgID     string
	Name      string
	Kind      ChannelKind
	Config    json.RawMessage
	Enabled   bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Route sends events for monitors matching all of MatchTags to a channel.
type Route struct {
	ID          string
	ProjectID   string
	MatchTags   []string
	ChannelID   string
	ChannelName string
	ChannelKind ChannelKind
	// On lists the states that trigger a delivery: down, up, late.
	On          []State
	RepeatEvery time.Duration
	Priority    int
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Fires reports whether the route wants deliveries for a flip to state.
func (r *Route) Fires(to State) bool {
	for _, s := range r.On {
		if s == to {
			return true
		}
	}
	return false
}

// Validate checks the route fields.
func (r *Route) Validate() error {
	ve := &ValidationError{}
	ValidateTags(ve, "match_tags", r.MatchTags)
	if r.ChannelID == "" {
		ve.Add("channel_id", "must name a channel")
	}
	if len(r.On) == 0 {
		ve.Add("on", "list at least one of down, up, late")
	}
	for _, s := range r.On {
		if s != StateDown && s != StateUp && s != StateLate {
			ve.Addf("on", "%q is not one of down, up, late", string(s))
		}
	}
	if r.RepeatEvery < 0 {
		ve.Add("repeat_every", "must not be negative")
	} else if r.RepeatEvery > 0 && r.RepeatEvery < 5*time.Minute {
		ve.Add("repeat_every", "must be at least 5m")
	}
	return ve.OrNil()
}

// Delivery is one outbox row: an event to a channel, with attempts.
type Delivery struct {
	ID            string
	EventID       string
	ChannelID     string
	ProjectID     string
	MonitorID     string
	RouteID       string
	Kind          string
	Repeat        bool
	Attempt       int
	NextAttemptAt time.Time
	DeliveredAt   *time.Time
	FailedAt      *time.Time
	Error         string
	CreatedAt     time.Time
}
