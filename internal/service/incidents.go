package service

import (
	"context"
	"sort"
	"time"

	"github.com/w4jnl/vink/internal/db"
	"github.com/w4jnl/vink/internal/domain"
)

type incidentRow struct {
	ID, MonitorID, ProjectID string
	OpenedAt                 int64
	ResolvedAt               *int64
	AckedBy                  *string
	AckedAt                  *int64
	OpenEventID              string
	CloseEventID             *string
	Slug, Name, Tags, Reason string
}

func incidentFrom(r incidentRow) *domain.Incident {
	inc := &domain.Incident{
		ID: r.ID, MonitorID: r.MonitorID, MonitorSlug: r.Slug, MonitorName: r.Name, MonitorTags: domain.ParseTags(r.Tags), Reason: r.Reason,
		ProjectID: r.ProjectID, OpenedAt: domain.FromMillis(r.OpenedAt), ResolvedAt: domain.FromMillisPtr(r.ResolvedAt),
		AckedBy: strp(r.AckedBy), AckedAt: domain.FromMillisPtr(r.AckedAt), OpenEventID: r.OpenEventID, CloseEventID: strp(r.CloseEventID),
	}
	if inc.MonitorTags == nil {
		inc.MonitorTags = []string{}
	}
	return inc
}

// ListIncidents returns incidents, open first then newest first. Resolved
// incidents older than since are left out; a zero since keeps them all.
func (s *Service) ListIncidents(ctx context.Context, sc domain.Scope, openOnly bool, limit int, since time.Time) ([]*domain.Incident, error) {
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
			out = append(out, incidentFrom(incidentRow{r.ID, r.MonitorID, r.ProjectID, r.OpenedAt, r.ResolvedAt, r.AckedBy, r.AckedAt, r.OpenEventID, r.CloseEventID, r.MonitorSlug, r.MonitorName, r.MonitorTags, r.Reason}))
		}
		return out, nil
	}
	rows, err := s.db.Read().ListIncidents(ctx, db.ListIncidentsParams{ProjectID: sc.ProjectID, Since: ptri(domain.Millis(since)), Limit: int64(limit)})
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out = append(out, incidentFrom(incidentRow{r.ID, r.MonitorID, r.ProjectID, r.OpenedAt, r.ResolvedAt, r.AckedBy, r.AckedAt, r.OpenEventID, r.CloseEventID, r.MonitorSlug, r.MonitorName, r.MonitorTags, r.Reason}))
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
	return incidentFrom(incidentRow{row.ID, row.MonitorID, row.ProjectID, row.OpenedAt, row.ResolvedAt, row.AckedBy, row.AckedAt, row.OpenEventID, row.CloseEventID, m.Slug, m.Name, m.Tags, row.Reason}), nil
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
	err = s.db.Tx(ctx, func(q *db.Queries) error {
		n, err := q.AckIncident(ctx, db.AckIncidentParams{AckedBy: ptrs(sc.Actor), AckedAt: ptri(domain.Millis(s.now())), ProjectID: sc.ProjectID, ID: id})
		if err != nil {
			return err
		}
		if n == 0 {
			return domain.NotFound("incident")
		}
		e := projectEntry(sc, "incident.ack", inc.MonitorSlug, inc.ID)
		e.Detail = map[string]any{"incident": inc.ID}
		return s.record(ctx, q, sc, e)
	})
	if err != nil {
		return nil, err
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
	open, err := s.ListIncidents(ctx, sc, true, 0, time.Time{})
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
