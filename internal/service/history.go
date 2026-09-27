package service

import (
	"context"
	"math"
	"time"

	"github.com/w4jnl/vink/internal/db"
	"github.com/w4jnl/vink/internal/domain"
)

// ObservationPage is a cursor over observations, newest first.
type ObservationPage struct {
	Since, Until time.Time
	CursorAt     time.Time
	CursorID     string
	Limit        int
}

// ListObservations returns a page of observations for a monitor.
func (s *Service) ListObservations(ctx context.Context, sc domain.Scope, slug string, p ObservationPage) ([]*domain.Observation, error) {
	m, err := s.MonitorBySlug(ctx, sc, slug)
	if err != nil {
		return nil, err
	}
	if p.Limit <= 0 {
		p.Limit = 50
	}
	if p.Limit > 200 {
		p.Limit = 200
	}
	until := int64(math.MaxInt64)
	if !p.Until.IsZero() {
		until = domain.Millis(p.Until)
	}
	cursorAt := int64(math.MaxInt64)
	cursorID := "~"
	if !p.CursorAt.IsZero() {
		cursorAt = domain.Millis(p.CursorAt)
		cursorID = p.CursorID
	}
	rows, err := s.db.Read().ListObservations(ctx, db.ListObservationsParams{
		ProjectID: sc.ProjectID, MonitorID: m.ID, Since: domain.Millis(p.Since), Until: until,
		CursorAt: cursorAt, CursorID: cursorID, PageSize: int64(p.Limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]*domain.Observation, 0, len(rows))
	for _, r := range rows {
		out = append(out, observationFromRow(r))
	}
	return out, nil
}

// ObservationsSince returns observations from since on, oldest first, for
// timelines.
func (s *Service) ObservationsSince(ctx context.Context, sc domain.Scope, m *domain.Monitor, since time.Time) ([]*domain.Observation, error) {
	if err := requireProject(sc); err != nil {
		return nil, err
	}
	rows, err := s.db.Read().ListObservationsSince(ctx, db.ListObservationsSinceParams{ProjectID: sc.ProjectID, MonitorID: m.ID, At: domain.Millis(since)})
	if err != nil {
		return nil, err
	}
	out := make([]*domain.Observation, 0, len(rows))
	for _, r := range rows {
		out = append(out, observationFromRow(r))
	}
	return out, nil
}

// Observation returns one observation of the scope's project.
func (s *Service) Observation(ctx context.Context, sc domain.Scope, id string) (*domain.Observation, error) {
	if err := requireProject(sc); err != nil {
		return nil, err
	}
	row, err := s.db.Read().GetObservation(ctx, db.GetObservationParams{ProjectID: sc.ProjectID, ID: id})
	if err != nil {
		return nil, notFoundIfNoRows(err, "observation")
	}
	return observationFromRow(row), nil
}

// ObservationBody returns the stored body and content type.
func (s *Service) ObservationBody(ctx context.Context, sc domain.Scope, id string) ([]byte, string, error) {
	if err := requireProject(sc); err != nil {
		return nil, "", err
	}
	row, err := s.db.Read().GetBody(ctx, db.GetBodyParams{ProjectID: sc.ProjectID, ObservationID: id})
	if err != nil {
		return nil, "", notFoundIfNoRows(err, "body")
	}
	return row.Content, row.ContentType, nil
}

// ListEvents returns a monitor's state flips, newest first.
func (s *Service) ListEvents(ctx context.Context, sc domain.Scope, slug string, limit int) ([]*domain.Event, error) {
	m, err := s.MonitorBySlug(ctx, sc, slug)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Read().ListEvents(ctx, db.ListEventsParams{ProjectID: sc.ProjectID, MonitorID: m.ID, Limit: int64(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]*domain.Event, 0, len(rows))
	for _, r := range rows {
		out = append(out, eventFromRow(r))
	}
	return out, nil
}

// EventsSince returns a monitor's flips from since on, oldest first.
func (s *Service) EventsSince(ctx context.Context, sc domain.Scope, m *domain.Monitor, since time.Time) ([]*domain.Event, error) {
	if err := requireProject(sc); err != nil {
		return nil, err
	}
	rows, err := s.db.Read().ListEventsSince(ctx, db.ListEventsSinceParams{ProjectID: sc.ProjectID, MonitorID: m.ID, At: domain.Millis(since)})
	if err != nil {
		return nil, err
	}
	out := make([]*domain.Event, 0, len(rows))
	for _, r := range rows {
		out = append(out, eventFromRow(r))
	}
	return out, nil
}
