package service

import (
	"context"
	"sort"
	"time"

	"github.com/w4jnl/vink/internal/db"
	"github.com/w4jnl/vink/internal/domain"
)

func maintenanceFromRow(r db.Maintenance) (*domain.Maintenance, error) {
	w := &domain.Maintenance{
		ID: r.ID, ProjectID: r.ProjectID, Name: r.Name, MatchTags: domain.ParseTags(r.MatchTags), Timezone: r.Timezone,
		CreatedAt: domain.FromMillis(r.CreatedAt), UpdatedAt: domain.FromMillis(r.UpdatedAt),
	}
	if w.MatchTags == nil {
		w.MatchTags = []string{}
	}
	if r.StartsAt != nil {
		t := domain.FromMillis(*r.StartsAt)
		w.StartsAt = &t
	}
	if r.EndsAt != nil {
		t := domain.FromMillis(*r.EndsAt)
		w.EndsAt = &t
	}
	if r.EndedUntil != nil {
		t := domain.FromMillis(*r.EndedUntil)
		w.EndedUntil = &t
	}
	if r.Rrule != nil && *r.Rrule != "" {
		days, err := domain.ParseRRule(*r.Rrule)
		if err != nil {
			return nil, err
		}
		w.Weekly, w.Days = true, days
		if r.FromTime != nil {
			w.From = *r.FromTime
		}
		if r.ToTime != nil {
			w.To = *r.ToTime
		}
	}
	return w, nil
}

func maintenanceParams(w *domain.Maintenance) (starts, ends *int64, rrule, from, to *string) {
	starts, ends = domain.MillisPtr(w.StartsAt), domain.MillisPtr(w.EndsAt)
	if w.Weekly {
		r := w.RRule()
		rrule, from, to = &r, ptrs(w.From), ptrs(w.To)
		starts, ends = nil, nil
	}
	return starts, ends, rrule, from, to
}

// prepareMaintenance normalises, fills the project timezone and validates.
func (s *Service) prepareMaintenance(ctx context.Context, sc domain.Scope, w *domain.Maintenance) error {
	if err := requireEdit(sc); err != nil {
		return err
	}
	w.Normalize()
	if w.Timezone == "" {
		project, err := s.Project(ctx, sc)
		if err != nil {
			return err
		}
		w.Timezone = project.Timezone
	}
	return w.Validate()
}

// CreateMaintenance adds a window.
func (s *Service) CreateMaintenance(ctx context.Context, sc domain.Scope, w *domain.Maintenance) (*domain.Maintenance, error) {
	if err := s.prepareMaintenance(ctx, sc, w); err != nil {
		return nil, err
	}
	now := s.now()
	starts, ends, rrule, from, to := maintenanceParams(w)
	row, err := s.db.Write().CreateMaintenance(ctx, db.CreateMaintenanceParams{
		ID: domain.NewID(), ProjectID: sc.ProjectID, Name: w.Name, MatchTags: tagsJSON(w.MatchTags), StartsAt: starts, EndsAt: ends,
		Rrule: rrule, FromTime: from, ToTime: to, Timezone: w.Timezone, CreatedAt: domain.Millis(now), UpdatedAt: domain.Millis(now),
	})
	if err != nil {
		return nil, err
	}
	s.log.Info("maintenance window created", "project_id", sc.ProjectID, "window", w.Name, "actor", sc.Actor)
	return maintenanceFromRow(row)
}

// Maintenance returns one window.
func (s *Service) Maintenance(ctx context.Context, sc domain.Scope, id string) (*domain.Maintenance, error) {
	if err := requireProject(sc); err != nil {
		return nil, err
	}
	row, err := s.db.Read().GetMaintenance(ctx, db.GetMaintenanceParams{ProjectID: sc.ProjectID, ID: id})
	if err != nil {
		return nil, notFoundIfNoRows(err, "maintenance window")
	}
	return maintenanceFromRow(row)
}

func (s *Service) listMaintenance(ctx context.Context, q *db.Queries, projectID string) ([]*domain.Maintenance, error) {
	rows, err := q.ListMaintenance(ctx, projectID)
	if err != nil {
		return nil, err
	}
	out := make([]*domain.Maintenance, 0, len(rows))
	for _, r := range rows {
		w, err := maintenanceFromRow(r)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, nil
}

// ListMaintenance lists the project's windows: active first, then by
// their next start, windows that are over last.
func (s *Service) ListMaintenance(ctx context.Context, sc domain.Scope) ([]*domain.Maintenance, error) {
	if err := requireProject(sc); err != nil {
		return nil, err
	}
	list, err := s.listMaintenance(ctx, s.db.Read(), sc.ProjectID)
	if err != nil {
		return nil, err
	}
	now := s.now()
	rank := func(w *domain.Maintenance) (int, time.Time) {
		if _, active := w.ActiveAt(now); active {
			return 0, time.Time{}
		}
		if start, _, ok := w.Occurrence(now); ok {
			return 1, start
		}
		return 2, w.CreatedAt
	}
	sort.SliceStable(list, func(i, j int) bool {
		ri, ti := rank(list[i])
		rj, tj := rank(list[j])
		if ri != rj {
			return ri < rj
		}
		return ti.Before(tj)
	})
	return list, nil
}

// UpdateMaintenance replaces a window; End now is forgotten.
func (s *Service) UpdateMaintenance(ctx context.Context, sc domain.Scope, id string, w *domain.Maintenance) (*domain.Maintenance, error) {
	cur, err := s.Maintenance(ctx, sc, id)
	if err != nil {
		return nil, err
	}
	next := *w
	if next.Timezone == "" {
		next.Timezone = cur.Timezone
	}
	if err := s.prepareMaintenance(ctx, sc, &next); err != nil {
		return nil, err
	}
	starts, ends, rrule, from, to := maintenanceParams(&next)
	row, err := s.db.Write().UpdateMaintenance(ctx, db.UpdateMaintenanceParams{
		Name: next.Name, MatchTags: tagsJSON(next.MatchTags), StartsAt: starts, EndsAt: ends, Rrule: rrule, FromTime: from, ToTime: to, Timezone: next.Timezone,
		EndedUntil: nil, UpdatedAt: domain.Millis(s.now()), ProjectID: sc.ProjectID, ID: id,
	})
	if err != nil {
		return nil, err
	}
	s.log.Info("maintenance window updated", "project_id", sc.ProjectID, "window", next.Name, "actor", sc.Actor)
	return maintenanceFromRow(row)
}

// DeleteMaintenance removes a window.
func (s *Service) DeleteMaintenance(ctx context.Context, sc domain.Scope, id string) error {
	if err := requireEdit(sc); err != nil {
		return err
	}
	n, err := s.db.Write().DeleteMaintenance(ctx, db.DeleteMaintenanceParams{ProjectID: sc.ProjectID, ID: id})
	if err != nil {
		return err
	}
	if n == 0 {
		return domain.NotFound("maintenance window")
	}
	s.log.Info("maintenance window deleted", "project_id", sc.ProjectID, "window_id", id, "actor", sc.Actor)
	return nil
}

// EndMaintenance ends the running occurrence now: a one-off window ends,
// a weekly one skips to its next occurrence.
func (s *Service) EndMaintenance(ctx context.Context, sc domain.Scope, id string) (*domain.Maintenance, error) {
	if err := requireOperate(sc); err != nil {
		return nil, err
	}
	w, err := s.Maintenance(ctx, sc, id)
	if err != nil {
		return nil, err
	}
	now := s.now()
	until, active := w.ActiveAt(now)
	if !active {
		return nil, (&domain.ValidationError{Errors: []domain.FieldError{{Field: "state", Msg: "the window is not active"}}}).OrNil()
	}
	if w.Weekly {
		w.EndedUntil = &until
	} else {
		w.EndsAt = &now
	}
	starts, ends, rrule, from, to := maintenanceParams(w)
	row, err := s.db.Write().UpdateMaintenance(ctx, db.UpdateMaintenanceParams{
		Name: w.Name, MatchTags: tagsJSON(w.MatchTags), StartsAt: starts, EndsAt: ends, Rrule: rrule, FromTime: from, ToTime: to, Timezone: w.Timezone,
		EndedUntil: domain.MillisPtr(w.EndedUntil), UpdatedAt: domain.Millis(now), ProjectID: sc.ProjectID, ID: id,
	})
	if err != nil {
		return nil, err
	}
	s.log.Info("maintenance window ended", "project_id", sc.ProjectID, "window", w.Name, "actor", sc.Actor)
	s.bus.Publish(engineChanged(sc.ProjectID))
	return maintenanceFromRow(row)
}

// ActiveMaintenance lists the windows running at now.
func (s *Service) ActiveMaintenance(ctx context.Context, sc domain.Scope, now time.Time) ([]*domain.Maintenance, error) {
	if err := requireProject(sc); err != nil {
		return nil, err
	}
	list, err := s.listMaintenance(ctx, s.db.Read(), sc.ProjectID)
	if err != nil {
		return nil, err
	}
	var active []*domain.Maintenance
	for _, w := range list {
		if _, on := w.ActiveAt(now); on {
			active = append(active, w)
		}
	}
	return active, nil
}

// coverage reports whether an active window covers the monitor at now,
// and when the last covering window ends.
func (s *Service) coverage(ctx context.Context, q *db.Queries, m *domain.Monitor, now time.Time) (until time.Time, covered bool, err error) {
	list, err := s.listMaintenance(ctx, q, m.ProjectID)
	if err != nil {
		return until, false, err
	}
	for _, w := range list {
		if !w.Covers(m) {
			continue
		}
		if u, active := w.ActiveAt(now); active {
			covered = true
			if u.After(until) {
				until = u
			}
		}
	}
	return until, covered, nil
}
