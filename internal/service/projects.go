package service

import (
	"context"
	"strings"
	"time"

	"github.com/w4jnl/vink/internal/audit"
	"github.com/w4jnl/vink/internal/db"
	"github.com/w4jnl/vink/internal/domain"
)

// ProjectSummary is a project with its org, for switchers and the CLI.
type ProjectSummary struct {
	domain.Project
	OrgSlug string
	OrgName string
	Role    domain.Role
}

// CreateProject creates a project in orgID. The caller must be an org
// admin of that org or an instance admin.
func (s *Service) CreateProject(ctx context.Context, sc domain.Scope, orgID, slug, name, timezone string) (*domain.Project, error) {
	if !sc.InstanceAdmin && (sc.OrgID != orgID || !sc.CanAdminOrg()) {
		return nil, domain.ErrForbidden
	}
	slug = strings.TrimSpace(slug)
	name = strings.TrimSpace(name)
	timezone = strings.TrimSpace(timezone)
	ve := &domain.ValidationError{}
	if !domain.ValidSlug(slug) {
		ve.Add("slug", "must be lowercase letters, digits and dashes, at most 64 characters")
	}
	if name == "" {
		name = slug
	}
	if timezone == "" {
		timezone = "UTC"
	}
	if !domain.ValidTimezone(timezone) {
		ve.Addf("timezone", "unknown timezone %q", timezone)
	}
	if err := ve.OrNil(); err != nil {
		return nil, err
	}
	if _, err := s.db.Read().GetOrg(ctx, orgID); err != nil {
		return nil, notFoundIfNoRows(err, "org")
	}
	var out *domain.Project
	err := s.db.Tx(ctx, func(q *db.Queries) error {
		row, err := q.CreateProject(ctx, db.CreateProjectParams{
			ID: domain.NewID(), OrgID: orgID, Slug: slug, Name: name, Timezone: timezone, PingKey: NewPingKey(), CreatedAt: domain.Millis(s.now()),
		})
		if err != nil {
			return conflictIfUnique(err, "a project with slug "+slug+" exists in this org")
		}
		out = projectFromRow(row)
		e := audit.Entry{Action: "project.create", Target: out.Slug, TargetID: out.ID, OrgID: orgID, ProjectID: out.ID, After: projectSnapshot(out)}
		return s.record(ctx, q, sc, e)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Project returns the project the scope is bound to.
func (s *Service) Project(ctx context.Context, sc domain.Scope) (*domain.Project, error) {
	if err := requireProject(sc); err != nil {
		return nil, err
	}
	row, err := s.db.Read().GetProject(ctx, db.GetProjectParams{OrgID: sc.OrgID, ID: sc.ProjectID})
	if err != nil {
		return nil, notFoundIfNoRows(err, "project")
	}
	return projectFromRow(row), nil
}

// ProjectBySlug resolves a project inside an org, for URL routing.
func (s *Service) ProjectBySlug(ctx context.Context, orgID, slug string) (*domain.Project, error) {
	row, err := s.db.Read().GetProjectBySlug(ctx, db.GetProjectBySlugParams{OrgID: orgID, Slug: slug})
	if err != nil {
		return nil, notFoundIfNoRows(err, "project")
	}
	return projectFromRow(row), nil
}

// ProjectByID resolves a project by id, for API-key scopes.
func (s *Service) ProjectByID(ctx context.Context, id string) (*domain.Project, error) {
	row, err := s.db.Read().GetProjectByID(ctx, id)
	if err != nil {
		return nil, notFoundIfNoRows(err, "project")
	}
	return projectFromRow(row), nil
}

// ProjectByPingKey resolves the project a ping key belongs to, honouring a
// rotated key inside its grace.
func (s *Service) ProjectByPingKey(ctx context.Context, key string) (*domain.Project, error) {
	row, err := s.db.Read().GetProjectByPingKey(ctx, db.GetProjectByPingKeyParams{Key: key, Now: ptri(domain.Millis(s.now()))})
	if err != nil {
		return nil, notFoundIfNoRows(err, "project")
	}
	return projectFromRow(row), nil
}

// ListProjects lists the projects of the scope's org.
func (s *Service) ListProjects(ctx context.Context, sc domain.Scope) ([]*domain.Project, error) {
	if sc.OrgID == "" {
		return nil, domain.ErrForbidden
	}
	rows, err := s.db.Read().ListProjects(ctx, sc.OrgID)
	if err != nil {
		return nil, err
	}
	out := make([]*domain.Project, 0, len(rows))
	for _, r := range rows {
		out = append(out, projectFromRow(r))
	}
	return out, nil
}

// ProjectsForUser lists every project the user can see through a
// membership, with the role. Instance admins see all projects as owner.
func (s *Service) ProjectsForUser(ctx context.Context, userID string, instanceAdmin bool) ([]ProjectSummary, error) {
	if instanceAdmin {
		rows, err := s.db.Read().ListAllProjects(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]ProjectSummary, 0, len(rows))
		for _, r := range rows {
			out = append(out, ProjectSummary{Project: *projectFromRow(r.Project), OrgSlug: r.OrgSlug, OrgName: r.OrgName, Role: domain.RoleOwner})
		}
		return out, nil
	}
	rows, err := s.db.Read().ListProjectsForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]ProjectSummary, 0, len(rows))
	for _, r := range rows {
		out = append(out, ProjectSummary{Project: *projectFromRow(r.Project), OrgSlug: r.OrgSlug, OrgName: r.OrgName, Role: domain.Role(r.Role)})
	}
	return out, nil
}

// UpdateProject changes name and timezone. Project admins only.
func (s *Service) UpdateProject(ctx context.Context, sc domain.Scope, name, timezone string) (*domain.Project, error) {
	if err := requireProject(sc); err != nil {
		return nil, err
	}
	if !sc.CanAdminProject() {
		return nil, domain.ErrForbidden
	}
	ve := &domain.ValidationError{}
	name = strings.TrimSpace(name)
	if name == "" {
		ve.Add("name", "must not be empty")
	}
	if !domain.ValidTimezone(timezone) {
		ve.Addf("timezone", "unknown timezone %q", timezone)
	}
	if err := ve.OrNil(); err != nil {
		return nil, err
	}
	cur, err := s.Project(ctx, sc)
	if err != nil {
		return nil, err
	}
	err = s.db.Tx(ctx, func(q *db.Queries) error {
		if err := q.UpdateProject(ctx, db.UpdateProjectParams{Name: name, Timezone: timezone, OrgID: sc.OrgID, ID: sc.ProjectID}); err != nil {
			return err
		}
		next := *cur
		next.Name, next.Timezone = name, timezone
		e := projectEntry(sc, "project.update", cur.Slug, cur.ID)
		e.Before, e.After = projectSnapshot(cur), projectSnapshot(&next)
		e.Detail = map[string]any{"fields": changedFields(e.Before, e.After)}
		return s.record(ctx, q, sc, e)
	})
	if err != nil {
		return nil, err
	}
	return s.Project(ctx, sc)
}

// RotatePingKey issues a new ping key; the old one keeps working for the
// configured grace. Project admins only.
func (s *Service) RotatePingKey(ctx context.Context, sc domain.Scope) (*domain.Project, error) {
	if err := requireProject(sc); err != nil {
		return nil, err
	}
	if !sc.CanAdminProject() {
		return nil, domain.ErrForbidden
	}
	until := s.now().Add(s.cfg.PingKeyGrace)
	if s.cfg.PingKeyGrace <= 0 {
		until = s.now().Add(-time.Second)
	}
	cur, err := s.Project(ctx, sc)
	if err != nil {
		return nil, err
	}
	err = s.db.Tx(ctx, func(q *db.Queries) error {
		if err := q.RotatePingKey(ctx, db.RotatePingKeyParams{PingKey: NewPingKey(), PingKeyPrevUntil: ptri(domain.Millis(until)), OrgID: sc.OrgID, ID: sc.ProjectID}); err != nil {
			return err
		}
		e := projectEntry(sc, "pingkey.rotate", cur.Slug, cur.ID)
		e.Detail = map[string]any{"old_key_until": until.UTC().Format(time.RFC3339)}
		return s.record(ctx, q, sc, e)
	})
	if err != nil {
		return nil, err
	}
	return s.Project(ctx, sc)
}
