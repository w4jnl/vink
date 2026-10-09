package service

import (
	"context"
	"sort"
	"time"

	"github.com/w4jnl/vink/internal/apply"
	"github.com/w4jnl/vink/internal/db"
	"github.com/w4jnl/vink/internal/domain"
)

// Org routes send events of the org's monitors to the org's channels: for
// the projects a route lists or, listing none, for every project of the
// org, new ones included. They fire beside the projects' own routes.

func orgRouteFromRow(r db.OrgRoute) *domain.Route {
	rt := &domain.Route{
		ID: r.ID, OrgID: r.OrgID, Projects: domain.ParseTags(r.Projects), MatchTags: domain.ParseTags(r.MatchTags),
		RepeatEvery: time.Duration(r.RepeatEveryS) * time.Second, Priority: int(r.Priority),
		CreatedAt: domain.FromMillis(r.CreatedAt), UpdatedAt: domain.FromMillis(r.UpdatedAt),
	}
	for _, s := range domain.ParseTags(r.OnStates) {
		rt.On = append(rt.On, domain.State(s))
	}
	if rt.MatchTags == nil {
		rt.MatchTags = []string{}
	}
	if rt.Projects == nil {
		rt.Projects = []string{}
	}
	return rt
}

// uniqueIDs drops empty and repeated ids, keeping the first order.
func uniqueIDs(ids []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// prepareOrgRoute normalises and validates an org route and checks that
// every channel and project belongs to the org.
func (s *Service) prepareOrgRoute(ctx context.Context, q *db.Queries, sc domain.Scope, r *domain.Route) error {
	r.ProjectID, r.OrgID = "", sc.OrgID
	r.MatchTags = domain.NormalizeTags(r.MatchTags)
	if len(r.On) == 0 {
		r.On = []domain.State{domain.StateDown, domain.StateUp}
	}
	r.ChannelIDs = uniqueIDs(r.ChannelIDs)
	r.Projects = uniqueIDs(r.Projects)
	sort.Strings(r.Projects)
	if err := r.Validate(); err != nil {
		return err
	}
	for _, id := range r.ChannelIDs {
		if _, err := q.GetOrgChannel(ctx, db.GetOrgChannelParams{OrgID: sc.OrgID, ID: id}); err != nil {
			if db.IsNotFound(err) {
				return validation("channels", "no such channel in this org")
			}
			return err
		}
	}
	return s.checkOrgProjects(ctx, q, sc.OrgID, r.Projects)
}

func (s *Service) writeOrgRouteChannels(ctx context.Context, q *db.Queries, sc domain.Scope, routeID string, channelIDs []string) error {
	if err := q.DeleteOrgRouteChannels(ctx, db.DeleteOrgRouteChannelsParams{OrgID: sc.OrgID, RouteID: routeID}); err != nil {
		return err
	}
	for _, id := range channelIDs {
		if err := q.InsertOrgRouteChannel(ctx, db.InsertOrgRouteChannelParams{RouteID: routeID, ChannelID: id, OrgID: sc.OrgID}); err != nil {
			return err
		}
	}
	return nil
}

// CreateOrgRoute adds a route of the org with its channels.
func (s *Service) CreateOrgRoute(ctx context.Context, sc domain.Scope, r *domain.Route) (*domain.Route, error) {
	if err := requireOrgAdmin(sc); err != nil {
		return nil, err
	}
	var id string
	err := s.db.Tx(ctx, func(q *db.Queries) error {
		if err := s.prepareOrgRoute(ctx, q, sc, r); err != nil {
			return err
		}
		now := s.now()
		row, err := q.CreateOrgRoute(ctx, db.CreateOrgRouteParams{
			ID: domain.NewID(), OrgID: sc.OrgID, Projects: idsJSON(r.Projects), MatchTags: tagsJSON(r.MatchTags), OnStates: statesJSON(r.On),
			RepeatEveryS: int64(r.RepeatEvery.Seconds()), Priority: int64(r.Priority), CreatedAt: domain.Millis(now), UpdatedAt: domain.Millis(now),
		})
		if err != nil {
			return err
		}
		id = row.ID
		if err := s.writeOrgRouteChannels(ctx, q, sc, id, r.ChannelIDs); err != nil {
			return err
		}
		created, err := s.inTx(q).OrgRoute(ctx, sc, id)
		if err != nil {
			return err
		}
		e := orgEntry(sc.OrgID, "route.create", routeLabel(created.MatchTags, created.ChannelNames()), id)
		e.After = s.orgRouteSnapshotIn(ctx, q, created)
		return s.record(ctx, q, sc, e)
	})
	if err != nil {
		return nil, err
	}
	s.log.Info("org route created", "org_id", sc.OrgID, "route_id", id, "actor", sc.Actor)
	return s.OrgRoute(ctx, sc, id)
}

// OrgRoute returns one route of the org with its channels.
func (s *Service) OrgRoute(ctx context.Context, sc domain.Scope, id string) (*domain.Route, error) {
	routes, err := s.ListOrgRoutes(ctx, sc)
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

// ListOrgRoutes lists the org's routes by priority, each with its channels.
func (s *Service) ListOrgRoutes(ctx context.Context, sc domain.Scope) ([]*domain.Route, error) {
	if err := requireOrgAdmin(sc); err != nil {
		return nil, err
	}
	return s.listOrgRoutes(ctx, s.db.Read(), sc.OrgID)
}

func (s *Service) listOrgRoutes(ctx context.Context, q *db.Queries, orgID string) ([]*domain.Route, error) {
	rows, err := q.ListOrgRoutes(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]*domain.Route, 0, len(rows))
	for _, r := range rows {
		out = append(out, orgRouteFromRow(r))
	}
	if len(out) == 0 {
		return out, nil
	}
	rcs, err := q.ListOrgRouteChannels(ctx, orgID)
	if err != nil {
		return nil, err
	}
	joined := make([]db.ListRouteChannelsRow, 0, len(rcs))
	for _, rc := range rcs {
		joined = append(joined, db.ListRouteChannelsRow(rc))
	}
	attachChannels(out, joined)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority > out[j].Priority
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

// routesFor lists every route that may send for monitors of a project:
// the project's own, then the org routes that cover it.
func (s *Service) routesFor(ctx context.Context, q *db.Queries, projectID string) ([]*domain.Route, error) {
	routes, err := s.listRoutes(ctx, q, projectID)
	if err != nil {
		return nil, err
	}
	p, err := q.GetProjectByID(ctx, projectID)
	if err != nil {
		return nil, err
	}
	orgRoutes, err := s.listOrgRoutes(ctx, q, p.OrgID)
	if err != nil {
		return nil, err
	}
	for _, rt := range orgRoutes {
		if rt.Covers(projectID) {
			routes = append(routes, rt)
		}
	}
	return routes, nil
}

// UpdateOrgRoute replaces a route of the org and its channels.
func (s *Service) UpdateOrgRoute(ctx context.Context, sc domain.Scope, id string, r *domain.Route) (*domain.Route, error) {
	cur, err := s.OrgRoute(ctx, sc, id)
	if err != nil {
		return nil, err
	}
	err = s.db.Tx(ctx, func(q *db.Queries) error {
		if _, err := q.GetOrgRoute(ctx, db.GetOrgRouteParams{OrgID: sc.OrgID, ID: id}); err != nil {
			return notFoundIfNoRows(err, "route")
		}
		if err := s.prepareOrgRoute(ctx, q, sc, r); err != nil {
			return err
		}
		if _, err := q.UpdateOrgRoute(ctx, db.UpdateOrgRouteParams{
			Projects: idsJSON(r.Projects), MatchTags: tagsJSON(r.MatchTags), OnStates: statesJSON(r.On), RepeatEveryS: int64(r.RepeatEvery.Seconds()),
			Priority: int64(r.Priority), UpdatedAt: domain.Millis(s.now()), OrgID: sc.OrgID, ID: id,
		}); err != nil {
			return err
		}
		if err := s.writeOrgRouteChannels(ctx, q, sc, id, r.ChannelIDs); err != nil {
			return err
		}
		next, err := s.inTx(q).OrgRoute(ctx, sc, id)
		if err != nil {
			return err
		}
		e := orgEntry(sc.OrgID, "route.update", routeLabel(next.MatchTags, next.ChannelNames()), id)
		e.Before, e.After = s.orgRouteSnapshotIn(ctx, q, cur), s.orgRouteSnapshotIn(ctx, q, next)
		e.Detail = map[string]any{"fields": changedFields(e.Before, e.After)}
		return s.record(ctx, q, sc, e)
	})
	if err != nil {
		return nil, err
	}
	s.log.Info("org route updated", "org_id", sc.OrgID, "route_id", id, "actor", sc.Actor)
	return s.OrgRoute(ctx, sc, id)
}

// DeleteOrgRoute removes a route of the org.
func (s *Service) DeleteOrgRoute(ctx context.Context, sc domain.Scope, id string) error {
	cur, err := s.OrgRoute(ctx, sc, id)
	if err != nil {
		return err
	}
	err = s.db.Tx(ctx, func(q *db.Queries) error {
		n, err := q.DeleteOrgRoute(ctx, db.DeleteOrgRouteParams{OrgID: sc.OrgID, ID: id})
		if err != nil {
			return err
		}
		if n == 0 {
			return domain.NotFound("route")
		}
		e := orgEntry(sc.OrgID, "route.delete", routeLabel(cur.MatchTags, cur.ChannelNames()), id)
		e.Before = s.orgRouteSnapshotIn(ctx, q, cur)
		return s.record(ctx, q, sc, e)
	})
	if err != nil {
		return err
	}
	s.log.Info("org route deleted", "org_id", sc.OrgID, "route_id", id, "actor", sc.Actor)
	return nil
}

// orgRouteEntry is an org route as an org file writes it, its projects
// named by slug; an id projectSlugs lacks is written as it is.
func orgRouteEntry(r *domain.Route, projectSlugs map[string]string) apply.Route {
	out := apply.Route{MatchTags: r.MatchTags, Channels: r.ChannelNames(), On: r.On, RepeatEvery: domain.Duration(r.RepeatEvery), Priority: r.Priority}
	for _, id := range r.Projects {
		if slug, ok := projectSlugs[id]; ok {
			out.Projects = append(out.Projects, slug)
		} else {
			out.Projects = append(out.Projects, id)
		}
	}
	sort.Strings(out.Projects)
	return out
}

// orgRouteSnapshotIn renders an org route for the audit log, its projects
// named by slug, read inside the transaction.
func (s *Service) orgRouteSnapshotIn(ctx context.Context, q *db.Queries, r *domain.Route) string {
	var slugs map[string]string
	if len(r.Projects) > 0 {
		if rows, err := q.ListProjects(ctx, r.OrgID); err == nil {
			slugs = make(map[string]string, len(rows))
			for _, p := range rows {
				slugs[p.ID] = p.Slug
			}
		}
	}
	return yamlOf(orgRouteEntry(r, slugs))
}
