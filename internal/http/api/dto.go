package api

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/service"
)

// MonitorIn is the monitor representation on input; the YAML form is the
// same. Kind-specific fields sit at the top level.
type MonitorIn struct {
	Slug              string           `json:"slug"`
	Name              string           `json:"name"`
	Kind              domain.Kind      `json:"kind"`
	Tags              []string         `json:"tags"`
	Schedule          *domain.Schedule `json:"schedule"`
	Timezone          string           `json:"timezone"`
	Grace             domain.Duration  `json:"grace"`
	MaxRuntime        domain.Duration  `json:"max_runtime"`
	FailureThreshold  int              `json:"failure_threshold"`
	RecoveryThreshold int              `json:"recovery_threshold"`
	Methods           []string         `json:"methods"`
	BodyLimit         int64            `json:"body_limit"`
	// Pull kinds: http, tcp, dns, tls, icmp.
	Interval domain.Duration   `json:"interval"`
	Timeout  domain.Duration   `json:"timeout"`
	Confirm  *domain.Confirm   `json:"confirm"`
	Location string            `json:"location"`
	HTTP     *domain.HTTPCheck `json:"http"`
	TCP      *domain.TCPCheck  `json:"tcp"`
	DNS      *domain.DNSCheck  `json:"dns"`
	TLS      *domain.TLSCheck  `json:"tls"`
	ICMP     *domain.ICMPCheck `json:"icmp"`
}

func (in MonitorIn) toDomain() *domain.Monitor {
	m := &domain.Monitor{Slug: in.Slug, Name: in.Name, Kind: in.Kind, Tags: in.Tags}
	if m.Kind == "" {
		m.Kind = domain.KindHeartbeat
	}
	if m.Kind.IsPull() {
		spec := &domain.PullSpec{
			Interval: in.Interval, Timeout: in.Timeout, FailureThreshold: in.FailureThreshold, RecoveryThreshold: in.RecoveryThreshold,
			Location: in.Location, HTTP: in.HTTP, TCP: in.TCP, DNS: in.DNS, TLS: in.TLS, ICMP: in.ICMP,
		}
		if in.Confirm != nil {
			spec.Confirm = *in.Confirm
		}
		m.Pull = spec
		return m
	}
	spec := &domain.HeartbeatSpec{
		Timezone: in.Timezone, Grace: in.Grace, MaxRuntime: in.MaxRuntime, FailureThreshold: in.FailureThreshold,
		RecoveryThreshold: in.RecoveryThreshold, Methods: in.Methods, BodyLimit: in.BodyLimit,
	}
	if in.Schedule != nil {
		spec.Schedule = *in.Schedule
	}
	m.Heartbeat = spec
	return m
}

func monitorInFrom(m *domain.Monitor) MonitorIn {
	in := MonitorIn{Slug: m.Slug, Name: m.Name, Kind: m.Kind, Tags: m.Tags}
	if s := m.Heartbeat; s != nil {
		sched := s.Schedule
		in.Schedule = &sched
		in.Timezone, in.Grace, in.MaxRuntime = s.Timezone, s.Grace, s.MaxRuntime
		in.FailureThreshold, in.RecoveryThreshold, in.Methods, in.BodyLimit = s.FailureThreshold, s.RecoveryThreshold, s.Methods, s.BodyLimit
	}
	if s := m.Pull; s != nil {
		confirm := s.Confirm
		in.Interval, in.Timeout, in.Confirm, in.Location = s.Interval, s.Timeout, &confirm, s.Location
		in.FailureThreshold, in.RecoveryThreshold = s.FailureThreshold, s.RecoveryThreshold
		in.HTTP, in.TCP, in.DNS, in.TLS, in.ICMP = s.HTTP, s.TCP, s.DNS, s.TLS, s.ICMP
	}
	return in
}

// MonitorOut is the full monitor with state.
type MonitorOut struct {
	ID                string            `json:"id"`
	Slug              string            `json:"slug"`
	Name              string            `json:"name"`
	Kind              domain.Kind       `json:"kind"`
	Tags              []string          `json:"tags"`
	Schedule          *domain.Schedule  `json:"schedule,omitempty"`
	Timezone          string            `json:"timezone,omitempty"`
	Grace             domain.Duration   `json:"grace,omitempty"`
	MaxRuntime        domain.Duration   `json:"max_runtime,omitempty"`
	FailureThreshold  int               `json:"failure_threshold,omitempty"`
	RecoveryThreshold int               `json:"recovery_threshold,omitempty"`
	Methods           []string          `json:"methods,omitempty"`
	BodyLimit         int64             `json:"body_limit,omitempty"`
	Interval          domain.Duration   `json:"interval,omitempty"`
	Timeout           domain.Duration   `json:"timeout,omitempty"`
	Confirm           *domain.Confirm   `json:"confirm,omitempty"`
	Location          string            `json:"location,omitempty"`
	HTTP              *domain.HTTPCheck `json:"http,omitempty"`
	TCP               *domain.TCPCheck  `json:"tcp,omitempty"`
	DNS               *domain.DNSCheck  `json:"dns,omitempty"`
	TLS               *domain.TLSCheck  `json:"tls,omitempty"`
	ICMP              *domain.ICMPCheck `json:"icmp,omitempty"`
	Target            string            `json:"target,omitempty"`
	State             domain.State      `json:"state"`
	StateSince        time.Time         `json:"state_since"`
	LastObsAt         *time.Time        `json:"last_obs_at"`
	LastOkAt          *time.Time        `json:"last_ok_at"`
	NextDueAt         *time.Time        `json:"next_due_at"`
	ExpectedAt        *time.Time        `json:"expected_at"`
	Paused            bool              `json:"paused"`
	PingURL           string            `json:"ping_url,omitempty"`
	CreatedAt         time.Time         `json:"created_at"`
	UpdatedAt         time.Time         `json:"updated_at"`
}

func monitorOut(svc *service.Service, p *domain.Project, m *domain.Monitor, showPingURL bool) MonitorOut {
	out := MonitorOut{
		ID: m.ID, Slug: m.Slug, Name: m.Name, Kind: m.Kind, Tags: m.Tags, State: m.State, StateSince: m.StateSince,
		LastObsAt: m.LastObsAt, LastOkAt: m.LastOkAt, NextDueAt: m.NextDueAt, Paused: m.Paused, CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt,
	}
	if out.Tags == nil {
		out.Tags = []string{}
	}
	if s := m.Heartbeat; s != nil {
		sched := s.Schedule
		out.Schedule = &sched
		out.Timezone, out.Grace, out.MaxRuntime = s.Timezone, s.Grace, s.MaxRuntime
		out.FailureThreshold, out.RecoveryThreshold, out.Methods, out.BodyLimit = s.FailureThreshold, s.RecoveryThreshold, s.Methods, s.BodyLimit
		out.ExpectedAt = svc.ExpectedAt(m, p.Timezone)
	}
	if s := m.Pull; s != nil {
		confirm := s.Confirm
		out.Interval, out.Timeout, out.Confirm, out.Location, out.Target = s.Interval, s.Timeout, &confirm, s.Location, s.Target()
		out.FailureThreshold, out.RecoveryThreshold = s.FailureThreshold, s.RecoveryThreshold
		out.HTTP, out.TCP, out.DNS, out.TLS, out.ICMP = s.HTTP, s.TCP, s.DNS, s.TLS, s.ICMP
	}
	if showPingURL && m.Kind == domain.KindHeartbeat {
		out.PingURL = svc.PingURL(p, m.Slug)
	}
	return out
}

// ObservationOut is one observation.
type ObservationOut struct {
	ID         string         `json:"id"`
	At         time.Time      `json:"at"`
	Source     string         `json:"source"`
	Signal     domain.Signal  `json:"signal"`
	OK         bool           `json:"ok"`
	LatencyMs  *int64         `json:"latency_ms"`
	ExitCode   *int64         `json:"exit_code"`
	DurationMs *int64         `json:"duration_ms"`
	RunID      string         `json:"run_id,omitempty"`
	RemoteAddr string         `json:"remote_addr,omitempty"`
	UserAgent  string         `json:"user_agent,omitempty"`
	HasBody    bool           `json:"has_body"`
	Detail     map[string]any `json:"detail"`
}

func observationOut(o *domain.Observation) ObservationOut {
	return ObservationOut{
		ID: o.ID, At: o.At, Source: o.Source, Signal: o.Signal, OK: o.OK, LatencyMs: o.LatencyMs, ExitCode: o.ExitCode, DurationMs: o.DurationMs,
		RunID: o.RunID, RemoteAddr: o.RemoteAddr, UserAgent: o.UserAgent, HasBody: o.HasBody, Detail: o.Detail,
	}
}

// EventOut is one state flip.
type EventOut struct {
	ID            string       `json:"id"`
	At            time.Time    `json:"at"`
	From          domain.State `json:"from"`
	To            domain.State `json:"to"`
	Reason        string       `json:"reason"`
	ObservationID string       `json:"observation_id,omitempty"`
}

func eventOut(e *domain.Event) EventOut {
	return EventOut{ID: e.ID, At: e.At, From: e.From, To: e.To, Reason: e.Reason, ObservationID: e.ObservationID}
}

// IncidentOut is one incident.
type IncidentOut struct {
	ID          string     `json:"id"`
	Monitor     string     `json:"monitor"`
	MonitorName string     `json:"monitor_name"`
	OpenedAt    time.Time  `json:"opened_at"`
	ResolvedAt  *time.Time `json:"resolved_at"`
	AckedBy     string     `json:"acked_by,omitempty"`
	AckedAt     *time.Time `json:"acked_at"`
	Open        bool       `json:"open"`
	// Reason is the event reason that opened the incident.
	Reason string `json:"reason,omitempty"`
}

func incidentOut(i *domain.Incident) IncidentOut {
	return IncidentOut{ID: i.ID, Monitor: i.MonitorSlug, MonitorName: i.MonitorName, OpenedAt: i.OpenedAt, ResolvedAt: i.ResolvedAt, AckedBy: i.AckedBy, AckedAt: i.AckedAt, Open: i.Open(), Reason: i.Reason}
}

// ChannelIn creates or replaces a channel.
type ChannelIn struct {
	Name    string             `json:"name"`
	Kind    domain.ChannelKind `json:"kind"`
	Config  json.RawMessage    `json:"config"`
	Enabled *bool              `json:"enabled"`
}

func (in ChannelIn) toDomain() *domain.Channel {
	c := &domain.Channel{Name: in.Name, Kind: in.Kind, Config: in.Config, Enabled: true}
	if in.Enabled != nil {
		c.Enabled = *in.Enabled
	}
	return c
}

// ChannelOut is a channel with secrets redacted.
type ChannelOut struct {
	ID        string             `json:"id"`
	Name      string             `json:"name"`
	Kind      domain.ChannelKind `json:"kind"`
	Config    json.RawMessage    `json:"config"`
	Enabled   bool               `json:"enabled"`
	CreatedAt time.Time          `json:"created_at"`
	UpdatedAt time.Time          `json:"updated_at"`
}

func channelOut(c *domain.Channel) ChannelOut {
	return ChannelOut{ID: c.ID, Name: c.Name, Kind: c.Kind, Config: service.RedactConfig(c.Kind, c.Config), Enabled: c.Enabled, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt}
}

// RouteIn creates or replaces a route.
type RouteIn struct {
	MatchTags   []string        `json:"match_tags"`
	Channels    []string        `json:"channels"`
	On          []domain.State  `json:"on"`
	RepeatEvery domain.Duration `json:"repeat_every"`
	Priority    int             `json:"priority"`
}

func (in RouteIn) toDomain() *domain.Route {
	return &domain.Route{MatchTags: in.MatchTags, ChannelIDs: in.Channels, On: in.On, RepeatEvery: in.RepeatEvery.Std(), Priority: in.Priority}
}

// RouteChannelOut names one target of a route.
type RouteChannelOut struct {
	ID      string             `json:"id"`
	Name    string             `json:"name"`
	Kind    domain.ChannelKind `json:"kind"`
	Enabled bool               `json:"enabled"`
}

// RouteOut is a route with its channels.
type RouteOut struct {
	ID          string            `json:"id"`
	MatchTags   []string          `json:"match_tags"`
	Channels    []RouteChannelOut `json:"channels"`
	On          []domain.State    `json:"on"`
	RepeatEvery domain.Duration   `json:"repeat_every"`
	Priority    int               `json:"priority"`
}

func routeOut(r *domain.Route) RouteOut {
	on := r.On
	if on == nil {
		on = []domain.State{}
	}
	chans := make([]RouteChannelOut, 0, len(r.Channels))
	for _, c := range r.Channels {
		chans = append(chans, RouteChannelOut{ID: c.ID, Name: c.Name, Kind: c.Kind, Enabled: c.Enabled})
	}
	return RouteOut{ID: r.ID, MatchTags: r.MatchTags, Channels: chans, On: on, RepeatEvery: domain.Duration(r.RepeatEvery), Priority: r.Priority}
}

// KeyOut is an API key without its secret.
type KeyOut struct {
	ID         string        `json:"id"`
	Name       string        `json:"name"`
	Prefix     string        `json:"prefix"`
	Access     domain.Access `json:"access"`
	CreatedAt  time.Time     `json:"created_at"`
	LastUsedAt *time.Time    `json:"last_used_at"`
	// Key is the plaintext, only on creation.
	Key string `json:"key,omitempty"`
}

func keyOut(k *domain.APIKey) KeyOut {
	return KeyOut{ID: k.ID, Name: k.Name, Prefix: k.Prefix, Access: k.Access, CreatedAt: k.CreatedAt, LastUsedAt: k.LastUsedAt}
}

// StatusOut is the project summary.
type StatusOut struct {
	Counts        map[domain.State]int `json:"counts"`
	Total         int                  `json:"total"`
	OpenIncidents []IncidentOut        `json:"open_incidents"`
	OldestLate    *time.Time           `json:"oldest_late_since"`
	Monitors      []StatusMonitorOut   `json:"monitors"`
	GeneratedAt   time.Time            `json:"generated_at"`
}

// StatusMonitorOut is the short form used by the summary.
type StatusMonitorOut struct {
	Slug       string       `json:"slug"`
	Name       string       `json:"name"`
	State      domain.State `json:"state"`
	StateSince time.Time    `json:"state_since"`
	Tags       []string     `json:"tags"`
}

func statusOut(s *service.StatusSummary) StatusOut {
	out := StatusOut{Counts: s.Counts, Total: s.Total, OpenIncidents: []IncidentOut{}, OldestLate: s.OldestLate, Monitors: []StatusMonitorOut{}, GeneratedAt: s.GeneratedAt}
	for _, i := range s.OpenIncidents {
		out.OpenIncidents = append(out.OpenIncidents, incidentOut(i))
	}
	for _, m := range s.Monitors {
		tags := m.Tags
		if tags == nil {
			tags = []string{}
		}
		out.Monitors = append(out.Monitors, StatusMonitorOut{Slug: m.Slug, Name: m.Name, State: m.State, StateSince: m.StateSince, Tags: tags})
	}
	return out
}

// MaintenanceIn creates or replaces a window: once with starts_at and
// ends_at, or weekly with rrule (FREQ=WEEKLY;BYDAY=SA,SU), from and to.
type MaintenanceIn struct {
	Name      string     `json:"name"`
	MatchTags []string   `json:"match_tags"`
	StartsAt  *time.Time `json:"starts_at"`
	EndsAt    *time.Time `json:"ends_at"`
	RRule     string     `json:"rrule"`
	From      string     `json:"from"`
	To        string     `json:"to"`
	Timezone  string     `json:"timezone"`
}

func (in MaintenanceIn) toDomain() (*domain.Maintenance, error) {
	w := &domain.Maintenance{Name: in.Name, MatchTags: in.MatchTags, StartsAt: in.StartsAt, EndsAt: in.EndsAt, From: in.From, To: in.To, Timezone: in.Timezone}
	if in.RRule != "" {
		days, err := domain.ParseRRule(in.RRule)
		if err != nil {
			return nil, (&domain.ValidationError{Errors: []domain.FieldError{{Field: "rrule", Msg: err.Error()}}}).OrNil()
		}
		w.Weekly, w.Days = true, days
	}
	return w, nil
}

// MaintenanceOut is a window with its state at the time of the request.
type MaintenanceOut struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	MatchTags   []string   `json:"match_tags"`
	StartsAt    *time.Time `json:"starts_at,omitempty"`
	EndsAt      *time.Time `json:"ends_at,omitempty"`
	RRule       string     `json:"rrule,omitempty"`
	From        string     `json:"from,omitempty"`
	To          string     `json:"to,omitempty"`
	Timezone    string     `json:"timezone"`
	Active      bool       `json:"active"`
	ActiveUntil *time.Time `json:"active_until,omitempty"`
	NextStart   *time.Time `json:"next_start,omitempty"`
	NextEnd     *time.Time `json:"next_end,omitempty"`
	EndedUntil  *time.Time `json:"ended_until,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

func maintenanceOut(w *domain.Maintenance, now time.Time) MaintenanceOut {
	out := MaintenanceOut{ID: w.ID, Name: w.Name, MatchTags: w.MatchTags, StartsAt: w.StartsAt, EndsAt: w.EndsAt, RRule: w.RRule(), From: w.From, To: w.To, Timezone: w.Timezone, EndedUntil: w.EndedUntil, CreatedAt: w.CreatedAt, UpdatedAt: w.UpdatedAt}
	if out.MatchTags == nil {
		out.MatchTags = []string{}
	}
	if until, active := w.ActiveAt(now); active {
		out.Active, out.ActiveUntil = true, &until
	}
	if start, end, ok := w.Occurrence(now); ok {
		out.NextStart, out.NextEnd = &start, &end
	}
	return out
}

// StatusPageIn creates or replaces a status page. A password makes the
// page private; an empty one keeps the current password on an update.
type StatusPageIn struct {
	Slug         string   `json:"slug"`
	Title        string   `json:"title"`
	MatchTags    []string `json:"match_tags"`
	Public       *bool    `json:"public"`
	Password     string   `json:"password"`
	CustomDomain string   `json:"custom_domain"`
}

func (in StatusPageIn) toDomain() *domain.StatusPage {
	p := &domain.StatusPage{Slug: in.Slug, Title: in.Title, MatchTags: in.MatchTags, CustomDomain: in.CustomDomain, Public: true}
	if in.Public != nil {
		p.Public = *in.Public
	}
	if in.Password != "" {
		p.Public = false
	}
	return p
}

// StatusPageOut is a page as the API shows it; the password never comes back.
type StatusPageOut struct {
	ID           string    `json:"id"`
	Slug         string    `json:"slug"`
	Title        string    `json:"title"`
	MatchTags    []string  `json:"match_tags"`
	Public       bool      `json:"public"`
	HasPassword  bool      `json:"has_password"`
	CustomDomain string    `json:"custom_domain,omitempty"`
	URL          string    `json:"url"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func statusPageOut(svc *service.Service, p *domain.StatusPage) StatusPageOut {
	tags := p.MatchTags
	if tags == nil {
		tags = []string{}
	}
	return StatusPageOut{
		ID: p.ID, Slug: p.Slug, Title: p.Title, MatchTags: tags, Public: p.Public, HasPassword: p.HasPassword(), CustomDomain: p.CustomDomain,
		URL: strings.TrimRight(svc.Config().BaseURL, "/") + "/s/" + p.Slug, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
}

// AgentIn creates an agent or replaces its labels.
type AgentIn struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels"`
}

// AgentOut is an agent with its derived state.
type AgentOut struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Labels      map[string]string `json:"labels"`
	State       domain.AgentState `json:"state"`
	Version     string            `json:"version,omitempty"`
	LastSeenAt  *time.Time        `json:"last_seen_at"`
	LastAddr    string            `json:"last_addr,omitempty"`
	TokenPrefix string            `json:"token_prefix"`
	CreatedAt   time.Time         `json:"created_at"`
}

// AgentCreated carries the token once, with the command to run.
type AgentCreated struct {
	AgentOut
	Token   string `json:"token"`
	Command string `json:"command"`
}

func agentOut(svc *service.Service, a *domain.Agent) AgentOut {
	labels := a.Labels
	if labels == nil {
		labels = map[string]string{}
	}
	return AgentOut{ID: a.ID, Name: a.Name, Labels: labels, State: a.State(svc.AgentConnected(a.ID)), Version: a.Version, LastSeenAt: a.LastSeenAt, LastAddr: a.LastAddr, TokenPrefix: a.TokenPrefix, CreatedAt: a.CreatedAt}
}
