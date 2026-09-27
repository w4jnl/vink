package api

import (
	"encoding/json"
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
}

func (in MonitorIn) toDomain() *domain.Monitor {
	m := &domain.Monitor{Slug: in.Slug, Name: in.Name, Kind: in.Kind, Tags: in.Tags}
	if m.Kind == "" {
		m.Kind = domain.KindHeartbeat
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
	return in
}

// MonitorOut is the full monitor with state.
type MonitorOut struct {
	ID                string           `json:"id"`
	Slug              string           `json:"slug"`
	Name              string           `json:"name"`
	Kind              domain.Kind      `json:"kind"`
	Tags              []string         `json:"tags"`
	Schedule          *domain.Schedule `json:"schedule,omitempty"`
	Timezone          string           `json:"timezone,omitempty"`
	Grace             domain.Duration  `json:"grace,omitempty"`
	MaxRuntime        domain.Duration  `json:"max_runtime,omitempty"`
	FailureThreshold  int              `json:"failure_threshold,omitempty"`
	RecoveryThreshold int              `json:"recovery_threshold,omitempty"`
	Methods           []string         `json:"methods,omitempty"`
	BodyLimit         int64            `json:"body_limit,omitempty"`
	State             domain.State     `json:"state"`
	StateSince        time.Time        `json:"state_since"`
	LastObsAt         *time.Time       `json:"last_obs_at"`
	LastOkAt          *time.Time       `json:"last_ok_at"`
	NextDueAt         *time.Time       `json:"next_due_at"`
	ExpectedAt        *time.Time       `json:"expected_at"`
	Paused            bool             `json:"paused"`
	PingURL           string           `json:"ping_url,omitempty"`
	CreatedAt         time.Time        `json:"created_at"`
	UpdatedAt         time.Time        `json:"updated_at"`
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
	if showPingURL {
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
}

func incidentOut(i *domain.Incident) IncidentOut {
	return IncidentOut{ID: i.ID, Monitor: i.MonitorSlug, MonitorName: i.MonitorName, OpenedAt: i.OpenedAt, ResolvedAt: i.ResolvedAt, AckedBy: i.AckedBy, AckedAt: i.AckedAt, Open: i.Open()}
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
	ChannelID   string          `json:"channel_id"`
	On          []domain.State  `json:"on"`
	RepeatEvery domain.Duration `json:"repeat_every"`
	Priority    int             `json:"priority"`
}

func (in RouteIn) toDomain() *domain.Route {
	return &domain.Route{MatchTags: in.MatchTags, ChannelID: in.ChannelID, On: in.On, RepeatEvery: in.RepeatEvery.Std(), Priority: in.Priority}
}

// RouteOut is a route with its channel name.
type RouteOut struct {
	ID          string          `json:"id"`
	MatchTags   []string        `json:"match_tags"`
	ChannelID   string          `json:"channel_id"`
	Channel     string          `json:"channel"`
	On          []domain.State  `json:"on"`
	RepeatEvery domain.Duration `json:"repeat_every"`
	Priority    int             `json:"priority"`
}

func routeOut(r *domain.Route) RouteOut {
	on := r.On
	if on == nil {
		on = []domain.State{}
	}
	return RouteOut{ID: r.ID, MatchTags: r.MatchTags, ChannelID: r.ChannelID, Channel: r.ChannelName, On: on, RepeatEvery: domain.Duration(r.RepeatEvery), Priority: r.Priority}
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
