package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/w4jnl/vink/internal/db"
	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/engine"
)

// PingTarget is what a ping URL resolved to.
type PingTarget struct {
	Project *domain.Project
	Monitor *domain.Monitor
	// Created is true when ?create=1 made the monitor.
	Created bool
}

// ResolvePing maps a ping key and a monitor slug, or a bare monitor id,
// to a monitor. Unknown key and unknown monitor both return ErrNotFound
// so nothing can be enumerated. With create set, an unknown slug creates
// a heartbeat monitor with the instance defaults. The id form needs no
// key: a ULID's 80 random bits are the capability.
func (s *Service) ResolvePing(ctx context.Context, key, slug, monitorID string, create bool) (*PingTarget, error) {
	if monitorID != "" {
		row, err := s.db.Read().GetMonitorByID(ctx, monitorID)
		if err != nil {
			return nil, notFoundIfNoRows(err, "monitor")
		}
		m, err := monitorFromRow(row)
		if err != nil {
			return nil, err
		}
		project, err := s.ProjectByID(ctx, m.ProjectID)
		if err != nil {
			return nil, err
		}
		return &PingTarget{Project: project, Monitor: m}, nil
	}
	project, err := s.ProjectByPingKey(ctx, key)
	if err != nil {
		return nil, err
	}
	row, err := s.db.Read().GetMonitorBySlug(ctx, db.GetMonitorBySlugParams{ProjectID: project.ID, Slug: slug})
	if err == nil {
		m, err := monitorFromRow(row)
		if err != nil {
			return nil, err
		}
		return &PingTarget{Project: project, Monitor: m}, nil
	}
	if !db.IsNotFound(err) {
		return nil, err
	}
	if !create || !domain.ValidSlug(slug) {
		return nil, domain.NotFound("monitor")
	}
	sc := domain.Scope{OrgID: project.OrgID, ProjectID: project.ID, Role: domain.RoleMember, Actor: "ping:create"}
	m, err := s.CreateMonitor(ctx, sc, &domain.Monitor{
		Slug: slug, Name: slug, Kind: domain.KindHeartbeat,
		Heartbeat: &domain.HeartbeatSpec{Schedule: domain.Schedule{Period: s.cfg.AutoCreatePeriod}, Grace: s.cfg.AutoCreateGrace},
	})
	if err != nil {
		if errors.Is(err, domain.ErrConflict) {
			// Raced with another auto-create; read it back.
			return s.ResolvePing(ctx, key, slug, "", false)
		}
		return nil, err
	}
	return &PingTarget{Project: project, Monitor: m, Created: true}, nil
}

// PingObservation is what the ingress hands to RecordPing.
type PingObservation struct {
	At          time.Time
	Signal      domain.Signal
	ExitCode    *int64
	RunID       string
	Method      string
	RemoteAddr  string
	UserAgent   string
	Body        []byte
	ContentType string
	Msg         string
	Truncated   bool
}

// RecordPing stores the observation and applies the state machine in one
// transaction, writing the event, incident and deliveries that follow.
func (s *Service) RecordPing(ctx context.Context, target *PingTarget, in PingObservation) (*domain.Observation, engine.Decision, error) {
	if in.At.IsZero() {
		in.At = s.now()
	}
	obs := &domain.Observation{
		ID: domain.NewID(), MonitorID: target.Monitor.ID, ProjectID: target.Project.ID, At: in.At, Source: "ping",
		Signal: in.Signal, ExitCode: in.ExitCode, RunID: in.RunID, RemoteAddr: in.RemoteAddr, UserAgent: in.UserAgent,
		HasBody: len(in.Body) > 0, Detail: map[string]any{},
	}
	switch in.Signal {
	case domain.SignalOK:
		obs.OK = true
	case domain.SignalExit:
		obs.OK = in.ExitCode != nil && *in.ExitCode == 0
	default:
		obs.OK = false
	}
	if in.Method != "" {
		obs.Detail["method"] = in.Method
	}
	if in.Msg != "" {
		obs.Detail["msg"] = in.Msg
	}
	if in.Truncated {
		obs.Detail["truncated"] = true
	}
	var decision engine.Decision
	err := s.db.Tx(ctx, func(q *db.Queries) error {
		row, err := q.GetMonitor(ctx, db.GetMonitorParams{ProjectID: target.Project.ID, ID: target.Monitor.ID})
		if err != nil {
			return notFoundIfNoRows(err, "monitor")
		}
		m, err := monitorFromRow(row)
		if err != nil {
			return err
		}
		loc, err := m.Heartbeat.Location(target.Project.Timezone)
		if err != nil {
			return err
		}
		decision, err = engine.Apply(m, obs, in.At, loc)
		if err != nil {
			return err
		}
		obs.DurationMs = decision.DurationMs
		if err := insertObservation(ctx, q, obs, in.Body, in.ContentType); err != nil {
			return err
		}
		return s.persistDecision(ctx, q, m, obs, decision, in.At)
	})
	if err != nil {
		return nil, decision, err
	}
	s.bus.Publish(engine.MonitorChanged{ProjectID: target.Project.ID, MonitorID: target.Monitor.ID})
	return obs, decision, nil
}

// Tick applies deadlines and run timeouts for one monitor. It is the
// scheduler's entry point.
func (s *Service) Tick(ctx context.Context, monitorID string, now time.Time) error {
	var (
		decision engine.Decision
		project  string
	)
	err := s.db.Tx(ctx, func(q *db.Queries) error {
		row, err := q.GetMonitorByID(ctx, monitorID)
		if err != nil {
			if db.IsNotFound(err) {
				return nil // deleted meanwhile
			}
			return err
		}
		m, err := monitorFromRow(row)
		if err != nil {
			return err
		}
		project = m.ProjectID
		p, err := q.GetProjectByID(ctx, m.ProjectID)
		if err != nil {
			return err
		}
		loc, err := m.Heartbeat.Location(p.Timezone)
		if err != nil {
			return err
		}
		decision, err = engine.Apply(m, nil, now, loc)
		if err != nil {
			return err
		}
		var obs *domain.Observation
		if decision.Synthetic != nil {
			obs = decision.Synthetic
			obs.ID = domain.NewID()
			if err := insertObservation(ctx, q, obs, nil, ""); err != nil {
				return err
			}
		}
		return s.persistDecision(ctx, q, m, obs, decision, now)
	})
	if err != nil {
		return err
	}
	if decision.Changed {
		s.bus.Publish(engine.MonitorChanged{ProjectID: project, MonitorID: monitorID})
	}
	return nil
}

// ListDue returns monitors whose wake-up time has passed.
func (s *Service) ListDue(ctx context.Context, now time.Time, limit int) ([]string, error) {
	rows, err := s.db.Read().ListDueMonitors(ctx, db.ListDueMonitorsParams{NextDueAt: ptri(domain.Millis(now)), Limit: int64(limit)})
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	return ids, nil
}

// NextDueAt returns the earliest pending wake-up.
func (s *Service) NextDueAt(ctx context.Context) (time.Time, bool, error) {
	ms, err := s.db.Read().NextDueAt(ctx)
	if err != nil || ms == 0 {
		return time.Time{}, false, err
	}
	return domain.FromMillis(ms), true, nil
}

func insertObservation(ctx context.Context, q *db.Queries, obs *domain.Observation, body []byte, contentType string) error {
	detail, err := json.Marshal(obs.Detail)
	if err != nil {
		return err
	}
	var bodyRef *string
	if len(body) > 0 {
		bodyRef = &obs.ID
		obs.HasBody = true
	}
	if err := q.InsertObservation(ctx, db.InsertObservationParams{
		ID: obs.ID, MonitorID: obs.MonitorID, ProjectID: obs.ProjectID, At: domain.Millis(obs.At), Source: obs.Source,
		Signal: string(obs.Signal), Ok: obs.OK, LatencyMs: obs.LatencyMs, ExitCode: obs.ExitCode, RunID: ptrs(obs.RunID),
		DurationMs: obs.DurationMs, RemoteAddr: ptrs(obs.RemoteAddr), UserAgent: ptrs(obs.UserAgent), BodyRef: bodyRef, Detail: string(detail),
	}); err != nil {
		return fmt.Errorf("insert observation: %w", err)
	}
	if len(body) > 0 {
		if err := q.InsertBody(ctx, db.InsertBodyParams{ObservationID: obs.ID, ProjectID: obs.ProjectID, Content: body, ContentType: contentType, CreatedAt: domain.Millis(obs.At)}); err != nil {
			return fmt.Errorf("insert body: %w", err)
		}
	}
	return nil
}

// persistDecision writes the monitor row, the event, the incident change
// and the deliveries for one decision.
func (s *Service) persistDecision(ctx context.Context, q *db.Queries, m *domain.Monitor, obs *domain.Observation, d engine.Decision, now time.Time) error {
	if q == nil {
		return errNoTx
	}
	stateSince := m.StateSince
	if d.Changed {
		stateSince = now
	}
	lastObs := m.LastObsAt
	if obs != nil {
		t := obs.At
		lastObs = &t
	}
	if err := q.UpdateMonitorState(ctx, db.UpdateMonitorStateParams{
		State: string(d.To), StateSince: domain.Millis(stateSince), BaseAt: domain.Millis(d.BaseAt), LastObsAt: domain.MillisPtr(lastObs),
		LastOkAt: domain.MillisPtr(d.LastOkAt), NextDueAt: domain.MillisPtr(d.NextDueAt), FailStreak: int64(d.FailStreak), OkStreak: int64(d.OkStreak),
		RunStartedAt: domain.MillisPtr(d.RunStartedAt), RunID: ptrs(d.RunID), UpdatedAt: domain.Millis(now), ProjectID: m.ProjectID, ID: m.ID,
	}); err != nil {
		return fmt.Errorf("update monitor state: %w", err)
	}
	if !d.Changed {
		return nil
	}
	eventID := domain.NewID()
	var obsID *string
	if obs != nil {
		obsID = &obs.ID
	}
	if err := q.InsertEvent(ctx, db.InsertEventParams{
		ID: eventID, MonitorID: m.ID, ProjectID: m.ProjectID, At: domain.Millis(now),
		FromState: string(d.From), ToState: string(d.To), Reason: d.Reason, ObservationID: obsID,
	}); err != nil {
		return fmt.Errorf("insert event: %w", err)
	}
	s.log.Info("monitor state changed", "project_id", m.ProjectID, "monitor", m.Slug, "from", d.From, "to", d.To, "reason", d.Reason)

	notify := false
	switch d.To {
	case domain.StateDown:
		_, err := q.GetOpenIncidentForMonitor(ctx, db.GetOpenIncidentForMonitorParams{ProjectID: m.ProjectID, MonitorID: m.ID})
		if db.IsNotFound(err) {
			if _, err := q.OpenIncident(ctx, db.OpenIncidentParams{ID: domain.NewID(), MonitorID: m.ID, ProjectID: m.ProjectID, OpenedAt: domain.Millis(now), OpenEventID: eventID}); err != nil {
				return fmt.Errorf("open incident: %w", err)
			}
		} else if err != nil {
			return err
		}
		notify = true
	case domain.StateUp:
		inc, err := q.GetOpenIncidentForMonitor(ctx, db.GetOpenIncidentForMonitorParams{ProjectID: m.ProjectID, MonitorID: m.ID})
		if err == nil {
			if err := q.ResolveIncident(ctx, db.ResolveIncidentParams{ResolvedAt: ptri(domain.Millis(now)), CloseEventID: &eventID, ProjectID: m.ProjectID, ID: inc.ID}); err != nil {
				return fmt.Errorf("resolve incident: %w", err)
			}
			notify = true
		} else if !db.IsNotFound(err) {
			return err
		}
	case domain.StateLate:
		notify = true
	}
	if !notify {
		return nil
	}
	return s.enqueueDeliveries(ctx, q, m, eventID, d.To, now)
}

// enqueueDeliveries inserts one outbox row per matching, enabled route.
func (s *Service) enqueueDeliveries(ctx context.Context, q *db.Queries, m *domain.Monitor, eventID string, to domain.State, now time.Time) error {
	routes, err := q.ListRoutes(ctx, m.ProjectID)
	if err != nil {
		return err
	}
	for _, r := range routes {
		rt := routeFromListRow(r)
		if !r.ChannelEnabled || !rt.Fires(to) || !m.HasAllTags(rt.MatchTags) {
			continue
		}
		if err := q.InsertDelivery(ctx, db.InsertDeliveryParams{
			ID: domain.NewID(), EventID: eventID, ChannelID: rt.ChannelID, ProjectID: m.ProjectID, MonitorID: m.ID, RouteID: &rt.ID,
			Kind: string(to), Repeat: false, Attempt: 0, NextAttemptAt: domain.Millis(now), CreatedAt: domain.Millis(now),
		}); err != nil {
			return fmt.Errorf("enqueue delivery: %w", err)
		}
	}
	return nil
}
