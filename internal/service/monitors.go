package service

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/w4jnl/vink/internal/db"
	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/engine"
)

// MonitorFilter narrows a list. Empty fields match everything.
type MonitorFilter struct {
	Tag   string
	State domain.State
	Kind  domain.Kind
	Query string
}

func (f MonitorFilter) matches(m *domain.Monitor) bool {
	if f.Tag != "" && !m.HasAllTags([]string{f.Tag}) {
		return false
	}
	if f.State != "" && m.State != f.State {
		return false
	}
	if f.Kind != "" && m.Kind != f.Kind {
		return false
	}
	if q := strings.ToLower(strings.TrimSpace(f.Query)); q != "" {
		if !strings.Contains(strings.ToLower(m.Name), q) && !strings.Contains(m.Slug, q) {
			return false
		}
	}
	return true
}

// CreateMonitor validates and stores m in the scope's project. The
// returned monitor carries its id, state and next due time.
func (s *Service) CreateMonitor(ctx context.Context, sc domain.Scope, m *domain.Monitor) (*domain.Monitor, error) {
	if err := requireEdit(sc); err != nil {
		return nil, err
	}
	m.Slug = strings.TrimSpace(m.Slug)
	m.Name = strings.TrimSpace(m.Name)
	if m.Name == "" {
		m.Name = m.Slug
	}
	if m.Kind == "" {
		m.Kind = domain.KindHeartbeat
	}
	m.Tags = domain.NormalizeTags(m.Tags)
	m.Normalize()
	if err := m.Validate(); err != nil {
		return nil, err
	}
	if err := s.checkPull(m); err != nil {
		return nil, err
	}
	project, err := s.Project(ctx, sc)
	if err != nil {
		return nil, err
	}
	if err := s.checkQuota(ctx, project.OrgID); err != nil {
		return nil, err
	}
	if err := s.checkLocation(ctx, project.OrgID, m); err != nil {
		return nil, err
	}
	now := s.now()
	m.ID = domain.NewID()
	m.ProjectID, m.OrgID = project.ID, project.OrgID
	m.State, m.StateSince, m.BaseAt = domain.StateNew, now, now
	m.Paused = false
	_, next, err := s.plan(m, project.Timezone, now)
	if err != nil {
		return nil, err
	}
	spec, err := m.SpecJSON()
	if err != nil {
		return nil, err
	}
	row, err := s.db.Write().CreateMonitor(ctx, db.CreateMonitorParams{
		ID: m.ID, ProjectID: project.ID, OrgID: project.OrgID, Slug: m.Slug, Name: m.Name, Kind: string(m.Kind),
		Spec: string(spec), Tags: m.TagsJSON(), State: string(domain.StateNew), StateSince: domain.Millis(now), BaseAt: domain.Millis(now),
		NextDueAt: domain.MillisPtr(next), CreatedAt: domain.Millis(now), UpdatedAt: domain.Millis(now),
	})
	if err != nil {
		return nil, conflictIfUnique(err, "a monitor with slug "+m.Slug+" exists")
	}
	out, err := monitorFromRow(row)
	if err != nil {
		return nil, err
	}
	s.bus.Publish(engine.MonitorChanged{ProjectID: project.ID, MonitorID: out.ID})
	s.log.Info("monitor created", "project_id", project.ID, "monitor", out.Slug, "actor", sc.Actor)
	return out, nil
}

func (s *Service) checkQuota(ctx context.Context, orgID string) error {
	org, err := s.db.Read().GetOrg(ctx, orgID)
	if err != nil {
		return notFoundIfNoRows(err, "org")
	}
	if org.QuotaMonitors == nil {
		return nil
	}
	n, err := s.db.Read().CountMonitorsInOrg(ctx, orgID)
	if err != nil {
		return err
	}
	if n >= *org.QuotaMonitors {
		return fmt.Errorf("%w: the org's monitor quota (%d) is reached", domain.ErrForbidden, *org.QuotaMonitors)
	}
	return nil
}

// MonitorBySlug returns one monitor of the scope's project.
func (s *Service) MonitorBySlug(ctx context.Context, sc domain.Scope, slug string) (*domain.Monitor, error) {
	if err := requireProject(sc); err != nil {
		return nil, err
	}
	row, err := s.db.Read().GetMonitorBySlug(ctx, db.GetMonitorBySlugParams{ProjectID: sc.ProjectID, Slug: slug})
	if err != nil {
		return nil, notFoundIfNoRows(err, "monitor")
	}
	return monitorFromRow(row)
}

// ListMonitors returns the project's monitors matching f, sorted by name.
func (s *Service) ListMonitors(ctx context.Context, sc domain.Scope, f MonitorFilter) ([]*domain.Monitor, error) {
	if err := requireProject(sc); err != nil {
		return nil, err
	}
	rows, err := s.db.Read().ListMonitors(ctx, sc.ProjectID)
	if err != nil {
		return nil, err
	}
	out := make([]*domain.Monitor, 0, len(rows))
	for _, r := range rows {
		m, err := monitorFromRow(r)
		if err != nil {
			return nil, err
		}
		if f.matches(m) {
			out = append(out, m)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}

// MonitorCounts returns how many monitors are in each state.
func (s *Service) MonitorCounts(ctx context.Context, sc domain.Scope) (map[domain.State]int, error) {
	if err := requireProject(sc); err != nil {
		return nil, err
	}
	rows, err := s.db.Read().CountMonitorsByState(ctx, sc.ProjectID)
	if err != nil {
		return nil, err
	}
	out := map[domain.State]int{}
	for _, r := range rows {
		out[domain.State(r.State)] = int(r.N)
	}
	return out, nil
}

// UpdateMonitor replaces name, tags and spec. The slug is immutable. A
// schedule change recomputes the next due time from the current base.
func (s *Service) UpdateMonitor(ctx context.Context, sc domain.Scope, slug string, upd *domain.Monitor) (*domain.Monitor, error) {
	if err := requireEdit(sc); err != nil {
		return nil, err
	}
	project, err := s.Project(ctx, sc)
	if err != nil {
		return nil, err
	}
	var out *domain.Monitor
	err = s.db.Tx(ctx, func(q *db.Queries) error {
		row, err := q.GetMonitorBySlug(ctx, db.GetMonitorBySlugParams{ProjectID: sc.ProjectID, Slug: slug})
		if err != nil {
			return notFoundIfNoRows(err, "monitor")
		}
		cur, err := monitorFromRow(row)
		if err != nil {
			return err
		}
		if upd.Slug != "" && upd.Slug != cur.Slug {
			return (&domain.ValidationError{Errors: []domain.FieldError{{Field: "slug", Msg: "cannot be changed; delete and recreate the monitor"}}}).OrNil()
		}
		if upd.Kind != "" && upd.Kind != cur.Kind {
			return (&domain.ValidationError{Errors: []domain.FieldError{{Field: "kind", Msg: "cannot be changed; delete and recreate the monitor"}}}).OrNil()
		}
		next := *cur
		next.Name = strings.TrimSpace(upd.Name)
		if next.Name == "" {
			next.Name = cur.Slug
		}
		next.Tags = domain.NormalizeTags(upd.Tags)
		if upd.Heartbeat != nil {
			spec := *upd.Heartbeat
			spec.Normalize()
			next.Heartbeat = &spec
		}
		if upd.Pull != nil {
			spec := *upd.Pull
			spec.Normalize()
			next.Pull = &spec
		}
		if err := next.Validate(); err != nil {
			return err
		}
		if err := s.checkPull(&next); err != nil {
			return err
		}
		if err := s.checkLocation(ctx, project.OrgID, &next); err != nil {
			return err
		}
		_, due, err := s.plan(&next, project.Timezone, s.now())
		if err != nil {
			return err
		}
		spec, err := next.SpecJSON()
		if err != nil {
			return err
		}
		now := s.now()
		saved, err := q.UpdateMonitor(ctx, db.UpdateMonitorParams{
			Name: next.Name, Spec: string(spec), Tags: next.TagsJSON(), NextDueAt: domain.MillisPtr(due), UpdatedAt: domain.Millis(now),
			ProjectID: sc.ProjectID, ID: cur.ID,
		})
		if err != nil {
			return err
		}
		if cur.AgentID != "" && (next.Pull == nil || !next.Pull.Remote()) {
			// Back to this server: the agent is told to drop it.
			if err := q.SetMonitorAgent(ctx, db.SetMonitorAgentParams{AgentID: nil, ID: cur.ID}); err != nil {
				return err
			}
			saved.AgentID = nil
		}
		out, err = monitorFromRow(saved)
		return err
	})
	if err != nil {
		return nil, err
	}
	s.bus.Publish(engine.MonitorChanged{ProjectID: sc.ProjectID, MonitorID: out.ID})
	s.log.Info("monitor updated", "project_id", sc.ProjectID, "monitor", out.Slug, "actor", sc.Actor)
	return out, nil
}

// DeleteMonitor removes a monitor and, through cascades, its history.
func (s *Service) DeleteMonitor(ctx context.Context, sc domain.Scope, slug string) error {
	if err := requireEdit(sc); err != nil {
		return err
	}
	m, err := s.MonitorBySlug(ctx, sc, slug)
	if err != nil {
		return err
	}
	n, err := s.db.Write().DeleteMonitor(ctx, db.DeleteMonitorParams{ProjectID: sc.ProjectID, ID: m.ID})
	if err != nil {
		return err
	}
	if n == 0 {
		return domain.NotFound("monitor")
	}
	s.bus.Publish(engine.MonitorChanged{ProjectID: sc.ProjectID, MonitorID: m.ID, Deleted: true})
	s.log.Info("monitor deleted", "project_id", sc.ProjectID, "monitor", slug, "actor", sc.Actor)
	return nil
}

// PauseMonitor stops deadlines and alerts. Observations are still stored.
func (s *Service) PauseMonitor(ctx context.Context, sc domain.Scope, slug string) (*domain.Monitor, error) {
	return s.setPaused(ctx, sc, slug, true)
}

// ResumeMonitor puts a paused monitor back to new with a fresh deadline.
func (s *Service) ResumeMonitor(ctx context.Context, sc domain.Scope, slug string) (*domain.Monitor, error) {
	return s.setPaused(ctx, sc, slug, false)
}

func (s *Service) setPaused(ctx context.Context, sc domain.Scope, slug string, paused bool) (*domain.Monitor, error) {
	if err := requireOperate(sc); err != nil {
		return nil, err
	}
	project, err := s.Project(ctx, sc)
	if err != nil {
		return nil, err
	}
	var out *domain.Monitor
	err = s.db.Tx(ctx, func(q *db.Queries) error {
		row, err := q.GetMonitorBySlug(ctx, db.GetMonitorBySlugParams{ProjectID: sc.ProjectID, Slug: slug})
		if err != nil {
			return notFoundIfNoRows(err, "monitor")
		}
		m, err := monitorFromRow(row)
		if err != nil {
			return err
		}
		if m.Paused == paused {
			out = m
			return nil
		}
		now := s.now()
		from := m.State
		next := *m
		next.Paused = paused
		next.StateSince = now
		reason := "paused"
		if paused {
			next.State = domain.StatePaused
		} else {
			next.State = domain.StateNew
			next.BaseAt = now
			reason = "resumed"
		}
		_, due, err := s.plan(&next, project.Timezone, now)
		if err != nil {
			return err
		}
		if err := q.SetMonitorPaused(ctx, db.SetMonitorPausedParams{
			Paused: paused, State: string(next.State), StateSince: domain.Millis(now), BaseAt: domain.Millis(next.BaseAt),
			NextDueAt: domain.MillisPtr(due), UpdatedAt: domain.Millis(now), ProjectID: sc.ProjectID, ID: m.ID,
		}); err != nil {
			return err
		}
		if err := q.InsertEvent(ctx, db.InsertEventParams{
			ID: domain.NewID(), MonitorID: m.ID, ProjectID: m.ProjectID, At: domain.Millis(now),
			FromState: string(from), ToState: string(next.State), Reason: reason,
		}); err != nil {
			return err
		}
		next.NextDueAt, next.FailStreak, next.OkStreak, next.RunStartedAt, next.RunID = due, 0, 0, nil, ""
		next.UpdatedAt = now
		out = &next
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.bus.Publish(engine.MonitorChanged{ProjectID: sc.ProjectID, MonitorID: out.ID})
	s.log.Info("monitor "+map[bool]string{true: "paused", false: "resumed"}[paused], "project_id", sc.ProjectID, "monitor", slug, "actor", sc.Actor)
	return out, nil
}

// plan computes the expected and next due times. Heartbeats take their
// deadline from the schedule; a pull monitor is due at once and the pool
// sets its cadence from the first attempt.
func (s *Service) plan(m *domain.Monitor, projectTZ string, now time.Time) (expected, due *time.Time, err error) {
	if m.Kind != domain.KindHeartbeat {
		if m.Paused {
			return nil, nil, nil
		}
		return &now, &now, nil
	}
	loc, err := m.Heartbeat.Location(projectTZ)
	if err != nil {
		return nil, nil, err
	}
	return engine.Plan(m, loc)
}

// ExpectedAt returns the next expected ping for display, or nil.
func (s *Service) ExpectedAt(m *domain.Monitor, projectTZ string) *time.Time {
	if m.Heartbeat == nil {
		return nil
	}
	loc, err := m.Heartbeat.Location(projectTZ)
	if err != nil {
		return nil
	}
	exp, _, err := engine.Plan(m, loc)
	if err != nil {
		return nil
	}
	return exp
}
