package service

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/w4jnl/vink/internal/db"
	"github.com/w4jnl/vink/internal/domain"
)

func orgFromRow(r db.Org) *domain.Org {
	return &domain.Org{ID: r.ID, Slug: r.Slug, Name: r.Name, QuotaMonitors: r.QuotaMonitors, QuotaAgents: r.QuotaAgents, CreatedAt: domain.FromMillis(r.CreatedAt)}
}

func projectFromRow(r db.Project) *domain.Project {
	return &domain.Project{
		ID: r.ID, OrgID: r.OrgID, Slug: r.Slug, Name: r.Name, Timezone: r.Timezone,
		PingKey: r.PingKey, PingKeyPrev: strp(r.PingKeyPrev), PingKeyPrevUntil: domain.FromMillisPtr(r.PingKeyPrevUntil),
		CreatedAt: domain.FromMillis(r.CreatedAt),
	}
}

func monitorFromRow(r db.Monitor) (*domain.Monitor, error) {
	m := &domain.Monitor{
		ID: r.ID, ProjectID: r.ProjectID, OrgID: r.OrgID, Slug: r.Slug, Name: r.Name, Kind: domain.Kind(r.Kind),
		Tags: domain.ParseTags(r.Tags), State: domain.State(r.State), StateSince: domain.FromMillis(r.StateSince),
		BaseAt: domain.FromMillis(r.BaseAt), LastObsAt: domain.FromMillisPtr(r.LastObsAt), LastOkAt: domain.FromMillisPtr(r.LastOkAt),
		NextDueAt: domain.FromMillisPtr(r.NextDueAt), Paused: r.Paused, FailStreak: int(r.FailStreak), OkStreak: int(r.OkStreak),
		RunStartedAt: domain.FromMillisPtr(r.RunStartedAt), RunID: strp(r.RunID),
		CreatedAt: domain.FromMillis(r.CreatedAt), UpdatedAt: domain.FromMillis(r.UpdatedAt),
	}
	if m.Tags == nil {
		m.Tags = []string{}
	}
	if err := m.SetSpecJSON([]byte(r.Spec)); err != nil {
		return nil, fmt.Errorf("monitor %s: %w", r.Slug, err)
	}
	return m, nil
}

func observationFromRow(r db.Observation) *domain.Observation {
	o := &domain.Observation{
		ID: r.ID, MonitorID: r.MonitorID, ProjectID: r.ProjectID, At: domain.FromMillis(r.At), Source: r.Source,
		Signal: domain.Signal(r.Signal), OK: r.Ok, LatencyMs: r.LatencyMs, ExitCode: r.ExitCode, DurationMs: r.DurationMs,
		RunID: strp(r.RunID), RemoteAddr: strp(r.RemoteAddr), UserAgent: strp(r.UserAgent), HasBody: r.BodyRef != nil,
	}
	if r.Detail != "" {
		_ = json.Unmarshal([]byte(r.Detail), &o.Detail)
	}
	if o.Detail == nil {
		o.Detail = map[string]any{}
	}
	return o
}

func eventFromRow(r db.Event) *domain.Event {
	return &domain.Event{
		ID: r.ID, MonitorID: r.MonitorID, ProjectID: r.ProjectID, At: domain.FromMillis(r.At),
		From: domain.State(r.FromState), To: domain.State(r.ToState), Reason: r.Reason, ObservationID: strp(r.ObservationID),
	}
}

func routeFromRow(r db.Route) *domain.Route {
	rt := &domain.Route{
		ID: r.ID, ProjectID: r.ProjectID, MatchTags: domain.ParseTags(r.MatchTags),
		RepeatEvery: time.Duration(r.RepeatEveryS) * time.Second, Priority: int(r.Priority),
		CreatedAt: domain.FromMillis(r.CreatedAt), UpdatedAt: domain.FromMillis(r.UpdatedAt),
	}
	for _, s := range domain.ParseTags(r.OnStates) {
		rt.On = append(rt.On, domain.State(s))
	}
	if rt.MatchTags == nil {
		rt.MatchTags = []string{}
	}
	return rt
}

// attachChannels fills Channels and ChannelIDs from the join rows.
func attachChannels(routes []*domain.Route, rows []db.ListRouteChannelsRow) {
	byRoute := map[string][]domain.RouteChannel{}
	for _, rc := range rows {
		byRoute[rc.RouteID] = append(byRoute[rc.RouteID], domain.RouteChannel{ID: rc.ChannelID, Name: rc.ChannelName, Kind: domain.ChannelKind(rc.ChannelKind), Enabled: rc.ChannelEnabled})
	}
	for _, rt := range routes {
		rt.Channels = byRoute[rt.ID]
		rt.ChannelIDs = rt.ChannelIDs[:0]
		for _, c := range rt.Channels {
			rt.ChannelIDs = append(rt.ChannelIDs, c.ID)
		}
	}
}

func statesJSON(states []domain.State) string {
	out := make([]string, 0, len(states))
	for _, s := range states {
		out = append(out, string(s))
	}
	b, _ := json.Marshal(out)
	return string(b)
}

func tagsJSON(tags []string) string {
	b, _ := json.Marshal(domain.NormalizeTags(tags))
	if b == nil {
		return "[]"
	}
	return string(b)
}
