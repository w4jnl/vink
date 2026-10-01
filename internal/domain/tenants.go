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
	ID string
	// ProjectID is empty for an org key, which may only export and
	// apply the org's projects.
	ProjectID  string
	OrgID      string
	Name       string
	Prefix     string
	Access     Access
	CreatedBy  string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	RevokedAt  *time.Time
}

// IsOrg reports whether the key spans the org rather than one project.
func (k *APIKey) IsOrg() bool { return k.ProjectID == "" }

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

// ChannelSecretFields names the config keys that are write-only in API
// responses: they come back as "***", and "***" on update keeps the
// stored value.
var ChannelSecretFields = map[ChannelKind][]string{
	ChannelSMTP:         {},
	ChannelWebhook:      {"headers"},
	ChannelNtfy:         {"token"},
	ChannelGotify:       {"token"},
	ChannelMatrix:       {"access_token"},
	ChannelSlackhook:    {"url"},
	ChannelAlertmanager: {},
}

// Validate checks the channel's identity fields; config is checked by the
// notifier registry.
func (c *Channel) Validate() error {
	ve := &ValidationError{}
	if c.Name == "" {
		ve.Add("name", "must not be empty")
	} else if len([]rune(c.Name)) > 64 {
		ve.Add("name", "at most 64 characters")
	}
	if !c.Kind.Valid() {
		ve.Addf("kind", "unknown channel kind %q", string(c.Kind))
	}
	if len(c.Config) == 0 || !json.Valid(c.Config) {
		ve.Add("config", "must be a JSON object")
	} else {
		var probe map[string]any
		if err := json.Unmarshal(c.Config, &probe); err != nil {
			ve.Add("config", "must be a JSON object")
		}
	}
	return ve.OrNil()
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

// RouteChannel is one channel a route sends to.
type RouteChannel struct {
	ID      string
	Name    string
	Kind    ChannelKind
	Enabled bool
}

// Route sends events for monitors matching all of MatchTags to its channels.
type Route struct {
	ID        string
	ProjectID string
	MatchTags []string
	// Channels are the targets; ChannelIDs is the input form.
	Channels   []RouteChannel
	ChannelIDs []string
	// On lists the states that trigger a delivery: down, up, late.
	On          []State
	RepeatEvery time.Duration
	Priority    int
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// ChannelNames lists the channel names, for display.
func (r *Route) ChannelNames() []string {
	out := make([]string, 0, len(r.Channels))
	for _, c := range r.Channels {
		out = append(out, c.Name)
	}
	return out
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
	if len(r.ChannelIDs) == 0 {
		ve.Add("channels", "pick at least one channel")
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
