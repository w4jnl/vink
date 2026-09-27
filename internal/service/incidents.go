package service

import (
	"context"
	"sort"
	"time"

	"github.com/w4jnl/vink/internal/db"
	"github.com/w4jnl/vink/internal/domain"
)

func incidentFromListRow(id, monitorID, projectID string, openedAt int64, resolvedAt *int64, ackedBy *string, ackedAt *int64, openEvent string, closeEvent *string, slug, name string) *domain.Incident {
	return &domain.Incident{
		ID: id, MonitorID: monitorID, MonitorSlug: slug, MonitorName: name, ProjectID: projectID, OpenedAt: domain.FromMillis(openedAt),
		ResolvedAt: domain.FromMillisPtr(resolvedAt), AckedBy: strp(ackedBy), AckedAt: domain.FromMillisPtr(ackedAt),
		OpenEventID: openEvent, CloseEventID: strp(closeEvent),
	}
}

// ListIncidents returns incidents, open first then newest first.
func (s *Service) ListIncidents(ctx context.Context, sc domain.Scope, openOnly bool, limit int) ([]*domain.Incident, error) {
	if err := requireProject(sc); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}
	out := []*domain.Incident{}
	if openOnly {
		rows, err := s.db.Read().ListOpenIncidents(ctx, sc.ProjectID)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			out = append(out, incidentFromListRow(r.ID, r.MonitorID, r.ProjectID, r.OpenedAt, r.ResolvedAt, r.AckedBy, r.AckedAt, r.OpenEventID, r.CloseEventID, r.MonitorSlug, r.MonitorName))
		}
		return out, nil
	}
	rows, err := s.db.Read().ListIncidents(ctx, db.ListIncidentsParams{ProjectID: sc.ProjectID, Limit: int64(limit)})
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out = append(out, incidentFromListRow(r.ID, r.MonitorID, r.ProjectID, r.OpenedAt, r.ResolvedAt, r.AckedBy, r.AckedAt, r.OpenEventID, r.CloseEventID, r.MonitorSlug, r.MonitorName))
	}
	return out, nil
}

// Incident returns one incident.
func (s *Service) Incident(ctx context.Context, sc domain.Scope, id string) (*domain.Incident, error) {
	if err := requireProject(sc); err != nil {
		return nil, err
	}
	row, err := s.db.Read().GetIncident(ctx, db.GetIncidentParams{ProjectID: sc.ProjectID, ID: id})
	if err != nil {
		return nil, notFoundIfNoRows(err, "incident")
	}
	m, err := s.db.Read().GetMonitor(ctx, db.GetMonitorParams{ProjectID: sc.ProjectID, ID: row.MonitorID})
	if err != nil {
		return nil, notFoundIfNoRows(err, "incident")
	}
	return incidentFromListRow(row.ID, row.MonitorID, row.ProjectID, row.OpenedAt, row.ResolvedAt, row.AckedBy, row.AckedAt, row.OpenEventID, row.CloseEventID, m.Slug, m.Name), nil
}

// AckIncident silences repeats for an open incident.
func (s *Service) AckIncident(ctx context.Context, sc domain.Scope, id string) (*domain.Incident, error) {
	if err := requireOperate(sc); err != nil {
		return nil, err
	}
	inc, err := s.Incident(ctx, sc, id)
	if err != nil {
		return nil, err
	}
	if !inc.Open() {
		return nil, domain.Conflict("incident is already resolved")
	}
	if inc.AckedAt != nil {
		return inc, nil
	}
	n, err := s.db.Write().AckIncident(ctx, db.AckIncidentParams{AckedBy: ptrs(sc.Actor), AckedAt: ptri(domain.Millis(s.now())), ProjectID: sc.ProjectID, ID: id})
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, domain.NotFound("incident")
	}
	s.log.Info("incident acknowledged", "project_id", sc.ProjectID, "incident", id, "actor", sc.Actor)
	return s.Incident(ctx, sc, id)
}

// AckIncidentByToken acknowledges from a signed one-click link.
func (s *Service) AckIncidentByToken(ctx context.Context, token string) (*domain.Incident, error) {
	payload, err := s.keyring.Verify("ack", token, s.now())
	if err != nil {
		return nil, domain.ErrUnauthorized
	}
	// payload is "<project_id>/<incident_id>"
	var projectID, incidentID string
	for i := 0; i < len(payload); i++ {
		if payload[i] == '/' {
			projectID, incidentID = payload[:i], payload[i+1:]
			break
		}
	}
	if projectID == "" || incidentID == "" {
		return nil, domain.ErrUnauthorized
	}
	p, err := s.ProjectByID(ctx, projectID)
	if err != nil {
		return nil, domain.ErrUnauthorized
	}
	sc := domain.Scope{OrgID: p.OrgID, ProjectID: p.ID, Role: domain.RoleMember, Actor: "link:ack"}
	return s.AckIncident(ctx, sc, incidentID)
}

// AckToken signs a seven-day one-click ack link for an incident.
func (s *Service) AckToken(projectID, incidentID string) string {
	return s.keyring.Sign("ack", projectID+"/"+incidentID, s.now().Add(7*24*time.Hour))
}

// StatusSummary is the project summary the API and status page share.
type StatusSummary struct {
	Counts        map[domain.State]int
	Total         int
	OpenIncidents []*domain.Incident
	OldestLate    *time.Time
	Monitors      []*domain.Monitor
	GeneratedAt   time.Time
}

// Status computes the project summary.
func (s *Service) Status(ctx context.Context, sc domain.Scope) (*StatusSummary, error) {
	monitors, err := s.ListMonitors(ctx, sc, MonitorFilter{})
	if err != nil {
		return nil, err
	}
	open, err := s.ListIncidents(ctx, sc, true, 0)
	if err != nil {
		return nil, err
	}
	sum := &StatusSummary{Counts: map[domain.State]int{}, Total: len(monitors), OpenIncidents: open, Monitors: monitors, GeneratedAt: s.now()}
	for _, st := range domain.States {
		sum.Counts[st] = 0
	}
	for _, m := range monitors {
		sum.Counts[m.State]++
		if m.State == domain.StateLate && (sum.OldestLate == nil || m.StateSince.Before(*sum.OldestLate)) {
			t := m.StateSince
			sum.OldestLate = &t
		}
	}
	sort.SliceStable(sum.Monitors, func(i, j int) bool {
		a, b := sum.Monitors[i], sum.Monitors[j]
		if a.State.SortRank() != b.State.SortRank() {
			return a.State.SortRank() < b.State.SortRank()
		}
		return a.Name < b.Name
	})
	return sum, nil
}
