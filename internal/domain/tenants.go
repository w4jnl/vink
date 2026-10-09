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
	ID          string
	Subject     string
	Email       string
	DisplayName string
	HasPassword bool
	// Source is local, proxy or oidc: where the account came from.
	Source        string
	InstanceAdmin bool
	DisabledAt    *time.Time
	DisabledBy    string
	// TOTPEnabledAt is set while two-factor is on.
	TOTPEnabledAt *time.Time
	CreatedAt     time.Time
}

// TOTPOn reports whether two-factor sign-in is on.
func (u *User) TOTPOn() bool { return u.TOTPEnabledAt != nil }

// Disabled reports whether sign-in is blocked.
func (u *User) Disabled() bool { return u.DisabledAt != nil }

// Name is what other people see: the display name, else the subject.
func (u *User) Name() string {
	if u.DisplayName != "" {
		return u.DisplayName
	}
	return u.Subject
}

// Member is one person's role in an org, with what the members list shows.
type Member struct {
	UserID      string
	Subject     string
	Email       string
	DisplayName string
	Role        Role
	// Source is local, header or oidc: how the membership came about.
	Source string
	// UserSource is where the account itself came from.
	UserSource string
	Disabled   bool
	LastSeenAt *time.Time
}

// Name is what the row shows.
func (m Member) Name() string {
	if m.DisplayName != "" {
		return m.DisplayName
	}
	return m.Subject
}

// Invite states.
const (
	InviteOpen    = "open"
	InviteExpired = "expired"
	InviteUsed    = "used"
	InviteRevoked = "revoked"
)

// InviteTTL is how long a link works.
const InviteTTL = 7 * 24 * time.Hour

// Invite is a one-time link into an org as a new local account.
type Invite struct {
	ID        string
	OrgID     string
	OrgSlug   string
	OrgName   string
	Role      Role
	Note      string
	CreatedBy string // the inviter's name for display
	CreatedAt time.Time
	ExpiresAt time.Time
	UsedAt    *time.Time
	UsedBy    string // the subject that joined
	RevokedAt *time.Time
}

// State says whether the link still works at now.
func (i *Invite) State(now time.Time) string {
	switch {
	case i.UsedAt != nil:
		return InviteUsed
	case i.RevokedAt != nil:
		return InviteRevoked
	case !now.Before(i.ExpiresAt):
		return InviteExpired
	}
	return InviteOpen
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

// AdminKey is an instance admin API key (vka_…). It acts as an instance
// admin on /api/v1/admin and nothing else, always expires, and is made
// only in the web UI or on the server host. Plaintext is returned once.
type AdminKey struct {
	ID         string
	Name       string
	Prefix     string
	Access     Access
	CreatedBy  string // the user who made it; empty for vink admin on the host
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastUsedAt *time.Time
	LastUsedIP string
	RevokedAt  *time.Time
}

// Expired reports whether the key's time is up.
func (k *AdminKey) Expired(now time.Time) bool { return !now.Before(k.ExpiresAt) }

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

// Channel is a configured notifier. Config is the decrypted JSON. An org
// channel has no ProjectID; only the org's routes send to it.
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

// IsOrg reports whether the channel belongs to the org rather than a project.
func (c *Channel) IsOrg() bool { return c.ProjectID == "" }

// RouteChannel is one channel a route sends to.
type RouteChannel struct {
	ID      string
	Name    string
	Kind    ChannelKind
	Enabled bool
}

// Route sends events for monitors matching all of MatchTags to its channels.
// An org route has OrgID and no ProjectID: it sends to the org's channels
// for monitors of the projects in Projects, or of every project when
// Projects is empty.
type Route struct {
	ID        string
	ProjectID string
	OrgID     string
	Projects  []string
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

// IsOrg reports whether the route belongs to the org rather than a project.
func (r *Route) IsOrg() bool { return r.ProjectID == "" && r.OrgID != "" }

// Covers reports whether the route applies to monitors of projectID: a
// project route to its own project, an org route to the projects it
// lists or, listing none, to all of them.
func (r *Route) Covers(projectID string) bool {
	if !r.IsOrg() {
		return r.ProjectID == projectID
	}
	if len(r.Projects) == 0 {
		return true
	}
	for _, id := range r.Projects {
		if id == projectID {
			return true
		}
	}
	return false
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
