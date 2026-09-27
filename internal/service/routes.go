package service

import (
	"context"

	"github.com/w4jnl/vink/internal/db"
	"github.com/w4jnl/vink/internal/domain"
)

func (s *Service) prepareRoute(ctx context.Context, sc domain.Scope, r *domain.Route) error {
	r.MatchTags = domain.NormalizeTags(r.MatchTags)
	if len(r.On) == 0 {
		r.On = []domain.State{domain.StateDown, domain.StateUp}
	}
	if err := r.Validate(); err != nil {
		return err
	}
	if _, err := s.db.Read().GetChannel(ctx, db.GetChannelParams{ProjectID: sc.ProjectID, ID: r.ChannelID}); err != nil {
		if db.IsNotFound(err) {
			return (&domain.ValidationError{Errors: []domain.FieldError{{Field: "channel_id", Msg: "no such channel in this project"}}}).OrNil()
		}
		return err
	}
	return nil
}

// CreateRoute adds a route.
func (s *Service) CreateRoute(ctx context.Context, sc domain.Scope, r *domain.Route) (*domain.Route, error) {
	if err := requireEdit(sc); err != nil {
		return nil, err
	}
	if err := s.prepareRoute(ctx, sc, r); err != nil {
		return nil, err
	}
	now := s.now()
	row, err := s.db.Write().CreateRoute(ctx, db.CreateRouteParams{
		ID: domain.NewID(), ProjectID: sc.ProjectID, MatchTags: tagsJSON(r.MatchTags), ChannelID: r.ChannelID, OnStates: statesJSON(r.On),
		RepeatEveryS: int64(r.RepeatEvery.Seconds()), Priority: int64(r.Priority), CreatedAt: domain.Millis(now), UpdatedAt: domain.Millis(now),
	})
	if err != nil {
		return nil, err
	}
	return s.Route(ctx, sc, row.ID)
}

// Route returns one route with its channel name and kind.
func (s *Service) Route(ctx context.Context, sc domain.Scope, id string) (*domain.Route, error) {
	routes, err := s.ListRoutes(ctx, sc)
	if err != nil {
		return nil, err
	}
	for _, r := range routes {
		if r.ID == id {
			return r, nil
		}
	}
	return nil, domain.NotFound("route")
}

// ListRoutes lists routes by priority.
func (s *Service) ListRoutes(ctx context.Context, sc domain.Scope) ([]*domain.Route, error) {
	if err := requireProject(sc); err != nil {
		return nil, err
	}
	rows, err := s.db.Read().ListRoutes(ctx, sc.ProjectID)
	if err != nil {
		return nil, err
	}
	out := make([]*domain.Route, 0, len(rows))
	for _, r := range rows {
		out = append(out, routeFromListRow(r))
	}
	return out, nil
}

// UpdateRoute replaces a route.
func (s *Service) UpdateRoute(ctx context.Context, sc domain.Scope, id string, r *domain.Route) (*domain.Route, error) {
	if err := requireEdit(sc); err != nil {
		return nil, err
	}
	if _, err := s.db.Read().GetRoute(ctx, db.GetRouteParams{ProjectID: sc.ProjectID, ID: id}); err != nil {
		return nil, notFoundIfNoRows(err, "route")
	}
	if err := s.prepareRoute(ctx, sc, r); err != nil {
		return nil, err
	}
	if _, err := s.db.Write().UpdateRoute(ctx, db.UpdateRouteParams{
		MatchTags: tagsJSON(r.MatchTags), ChannelID: r.ChannelID, OnStates: statesJSON(r.On), RepeatEveryS: int64(r.RepeatEvery.Seconds()),
		Priority: int64(r.Priority), UpdatedAt: domain.Millis(s.now()), ProjectID: sc.ProjectID, ID: id,
	}); err != nil {
		return nil, err
	}
	return s.Route(ctx, sc, id)
}

// DeleteRoute removes a route.
func (s *Service) DeleteRoute(ctx context.Context, sc domain.Scope, id string) error {
	if err := requireEdit(sc); err != nil {
		return err
	}
	n, err := s.db.Write().DeleteRoute(ctx, db.DeleteRouteParams{ProjectID: sc.ProjectID, ID: id})
	if err != nil {
		return err
	}
	if n == 0 {
		return domain.NotFound("route")
	}
	return nil
}
