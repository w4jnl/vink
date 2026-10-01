package service

import (
	"context"
	"sort"

	"github.com/w4jnl/vink/internal/db"
	"github.com/w4jnl/vink/internal/domain"
)

// prepareRoute normalises and validates a route and checks that every
// channel belongs to the project.
func (s *Service) prepareRoute(ctx context.Context, q *db.Queries, sc domain.Scope, r *domain.Route) error {
	r.MatchTags = domain.NormalizeTags(r.MatchTags)
	if len(r.On) == 0 {
		r.On = []domain.State{domain.StateDown, domain.StateUp}
	}
	seen := map[string]bool{}
	ids := r.ChannelIDs[:0]
	for _, id := range r.ChannelIDs {
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	r.ChannelIDs = ids
	if err := r.Validate(); err != nil {
		return err
	}
	for _, id := range r.ChannelIDs {
		if _, err := q.GetChannel(ctx, db.GetChannelParams{ProjectID: sc.ProjectID, ID: id}); err != nil {
			if db.IsNotFound(err) {
				return (&domain.ValidationError{Errors: []domain.FieldError{{Field: "channels", Msg: "no such channel in this project"}}}).OrNil()
			}
			return err
		}
	}
	return nil
}

func (s *Service) writeRouteChannels(ctx context.Context, q *db.Queries, sc domain.Scope, routeID string, channelIDs []string) error {
	if err := q.DeleteRouteChannels(ctx, db.DeleteRouteChannelsParams{ProjectID: sc.ProjectID, RouteID: routeID}); err != nil {
		return err
	}
	for _, id := range channelIDs {
		if err := q.InsertRouteChannel(ctx, db.InsertRouteChannelParams{RouteID: routeID, ChannelID: id, ProjectID: sc.ProjectID}); err != nil {
			return err
		}
	}
	return nil
}

// CreateRoute adds a route with its channels.
func (s *Service) CreateRoute(ctx context.Context, sc domain.Scope, r *domain.Route) (*domain.Route, error) {
	if err := requireEdit(sc); err != nil {
		return nil, err
	}
	var id string
	err := s.db.Tx(ctx, func(q *db.Queries) error {
		if err := s.prepareRoute(ctx, q, sc, r); err != nil {
			return err
		}
		now := s.now()
		row, err := q.CreateRoute(ctx, db.CreateRouteParams{
			ID: domain.NewID(), ProjectID: sc.ProjectID, MatchTags: tagsJSON(r.MatchTags), OnStates: statesJSON(r.On),
			RepeatEveryS: int64(r.RepeatEvery.Seconds()), Priority: int64(r.Priority), CreatedAt: domain.Millis(now), UpdatedAt: domain.Millis(now),
		})
		if err != nil {
			return err
		}
		id = row.ID
		if err := s.writeRouteChannels(ctx, q, sc, id, r.ChannelIDs); err != nil {
			return err
		}
		created, err := s.inTx(q).Route(ctx, sc, id)
		if err != nil {
			return err
		}
		e := projectEntry(sc, "route.create", routeLabel(created.MatchTags, created.ChannelNames()), id)
		e.After = routeSnapshot(created)
		return s.record(ctx, q, sc, e)
	})
	if err != nil {
		return nil, err
	}
	return s.Route(ctx, sc, id)
}

// Route returns one route with its channels.
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

// ListRoutes lists routes by priority, each with its channels.
func (s *Service) ListRoutes(ctx context.Context, sc domain.Scope) ([]*domain.Route, error) {
	if err := requireProject(sc); err != nil {
		return nil, err
	}
	return s.listRoutes(ctx, s.db.Read(), sc.ProjectID)
}

func (s *Service) listRoutes(ctx context.Context, q *db.Queries, projectID string) ([]*domain.Route, error) {
	rows, err := q.ListRoutes(ctx, projectID)
	if err != nil {
		return nil, err
	}
	out := make([]*domain.Route, 0, len(rows))
	for _, r := range rows {
		out = append(out, routeFromRow(r))
	}
	rcs, err := q.ListRouteChannels(ctx, projectID)
	if err != nil {
		return nil, err
	}
	attachChannels(out, rcs)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority > out[j].Priority
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

// UpdateRoute replaces a route and its channels.
func (s *Service) UpdateRoute(ctx context.Context, sc domain.Scope, id string, r *domain.Route) (*domain.Route, error) {
	if err := requireEdit(sc); err != nil {
		return nil, err
	}
	cur, err := s.Route(ctx, sc, id)
	if err != nil {
		return nil, err
	}
	err = s.db.Tx(ctx, func(q *db.Queries) error {
		if _, err := q.GetRoute(ctx, db.GetRouteParams{ProjectID: sc.ProjectID, ID: id}); err != nil {
			return notFoundIfNoRows(err, "route")
		}
		if err := s.prepareRoute(ctx, q, sc, r); err != nil {
			return err
		}
		if _, err := q.UpdateRoute(ctx, db.UpdateRouteParams{
			MatchTags: tagsJSON(r.MatchTags), OnStates: statesJSON(r.On), RepeatEveryS: int64(r.RepeatEvery.Seconds()),
			Priority: int64(r.Priority), UpdatedAt: domain.Millis(s.now()), ProjectID: sc.ProjectID, ID: id,
		}); err != nil {
			return err
		}
		if err := s.writeRouteChannels(ctx, q, sc, id, r.ChannelIDs); err != nil {
			return err
		}
		next, err := s.inTx(q).Route(ctx, sc, id)
		if err != nil {
			return err
		}
		e := projectEntry(sc, "route.update", routeLabel(next.MatchTags, next.ChannelNames()), id)
		e.Before, e.After = routeSnapshot(cur), routeSnapshot(next)
		e.Detail = map[string]any{"fields": changedFields(e.Before, e.After)}
		return s.record(ctx, q, sc, e)
	})
	if err != nil {
		return nil, err
	}
	return s.Route(ctx, sc, id)
}

// DeleteRoute removes a route.
func (s *Service) DeleteRoute(ctx context.Context, sc domain.Scope, id string) error {
	if err := requireEdit(sc); err != nil {
		return err
	}
	cur, err := s.Route(ctx, sc, id)
	if err != nil {
		return err
	}
	return s.db.Tx(ctx, func(q *db.Queries) error {
		n, err := q.DeleteRoute(ctx, db.DeleteRouteParams{ProjectID: sc.ProjectID, ID: id})
		if err != nil {
			return err
		}
		if n == 0 {
			return domain.NotFound("route")
		}
		e := projectEntry(sc, "route.delete", routeLabel(cur.MatchTags, cur.ChannelNames()), id)
		e.Before = routeSnapshot(cur)
		return s.record(ctx, q, sc, e)
	})
}

// RouteCountForChannel says how many routes send to a channel.
func (s *Service) RouteCountForChannel(ctx context.Context, sc domain.Scope, channelID string) (int, error) {
	if err := requireProject(sc); err != nil {
		return 0, err
	}
	n, err := s.db.Read().CountRoutesForChannel(ctx, db.CountRoutesForChannelParams{ProjectID: sc.ProjectID, ChannelID: channelID})
	return int(n), err
}
