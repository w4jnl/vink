package service

import (
	"context"
	"strings"

	"github.com/w4jnl/vink/internal/db"
	"github.com/w4jnl/vink/internal/domain"
)

// CreateOrg creates an org. Instance admins only.
func (s *Service) CreateOrg(ctx context.Context, sc domain.Scope, slug, name string) (*domain.Org, error) {
	if err := requireInstanceAdmin(sc); err != nil {
		return nil, err
	}
	slug = strings.TrimSpace(slug)
	name = strings.TrimSpace(name)
	ve := &domain.ValidationError{}
	if !domain.ValidSlug(slug) {
		ve.Add("slug", "must be lowercase letters, digits and dashes, at most 64 characters")
	}
	if name == "" {
		name = slug
	}
	if err := ve.OrNil(); err != nil {
		return nil, err
	}
	row, err := s.db.Write().CreateOrg(ctx, db.CreateOrgParams{ID: domain.NewID(), Slug: slug, Name: name, CreatedAt: domain.Millis(s.now())})
	if err != nil {
		return nil, conflictIfUnique(err, "an org with slug "+slug+" exists")
	}
	return orgFromRow(row), nil
}

// OrgBySlug looks an org up by slug. It carries no scope because the auth
// layer uses it to build scopes.
func (s *Service) OrgBySlug(ctx context.Context, slug string) (*domain.Org, error) {
	row, err := s.db.Read().GetOrgBySlug(ctx, slug)
	if err != nil {
		return nil, notFoundIfNoRows(err, "org")
	}
	return orgFromRow(row), nil
}

// OrgByID looks an org up by id.
func (s *Service) OrgByID(ctx context.Context, id string) (*domain.Org, error) {
	row, err := s.db.Read().GetOrg(ctx, id)
	if err != nil {
		return nil, notFoundIfNoRows(err, "org")
	}
	return orgFromRow(row), nil
}

// ListOrgs lists every org. Instance admins only.
func (s *Service) ListOrgs(ctx context.Context, sc domain.Scope) ([]*domain.Org, error) {
	if err := requireInstanceAdmin(sc); err != nil {
		return nil, err
	}
	rows, err := s.db.Read().ListOrgs(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*domain.Org, 0, len(rows))
	for _, r := range rows {
		out = append(out, orgFromRow(r))
	}
	return out, nil
}
