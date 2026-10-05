package service

import (
	"context"
	"fmt"

	"github.com/w4jnl/vink/internal/db"
	"github.com/w4jnl/vink/internal/domain"
)

// Org status pages: a page of the org itself, showing monitors of its
// projects (all of them, or a chosen list). Org admins and owners manage
// them, as they manage projects and agents; the public page is served at
// /s/{slug} like a project's.

// checkOrgProjects confirms that every id is a project of the org.
func (s *Service) checkOrgProjects(ctx context.Context, q *db.Queries, orgID string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	rows, err := q.ListProjects(ctx, orgID)
	if err != nil {
		return err
	}
	known := make(map[string]bool, len(rows))
	for _, r := range rows {
		known[r.ID] = true
	}
	for _, id := range ids {
		if !known[id] {
			return validation("projects", fmt.Sprintf("%s is not a project of this org", id))
		}
	}
	return nil
}

// CreateOrgStatusPage adds a page of the org; the slug is unique across the
// instance, as for project pages.
func (s *Service) CreateOrgStatusPage(ctx context.Context, sc domain.Scope, p *domain.StatusPage, password string) (*domain.StatusPage, error) {
	if err := requireOrgAdmin(sc); err != nil {
		return nil, err
	}
	p.OrgID, p.ProjectID = sc.OrgID, ""
	if err := preparePage(p, password, ""); err != nil {
		return nil, err
	}
	now := s.now()
	var out *domain.StatusPage
	err := s.db.Tx(ctx, func(q *db.Queries) error {
		if err := s.checkOrgProjects(ctx, q, sc.OrgID, p.Projects); err != nil {
			return err
		}
		row, err := q.CreateStatusPage(ctx, db.CreateStatusPageParams{
			ID: domain.NewID(), OrgID: sc.OrgID, ProjectID: nil, Slug: p.Slug, Title: p.Title, MatchTags: tagsJSON(p.MatchTags),
			Projects: idsJSON(p.Projects), GroupBy: p.GroupBy, Incidents: p.Incidents, Public: p.Public,
			PasswordHash: ptrs(p.PasswordHash), CustomDomain: ptrs(p.CustomDomain), CreatedAt: domain.Millis(now), UpdatedAt: domain.Millis(now),
		})
		if err != nil {
			return conflictIfUnique(err, "the address "+p.Slug+" is taken")
		}
		out = statusPageFromRow(row)
		e := orgEntry(sc.OrgID, "page.create", out.Slug, out.ID)
		e.After = s.pageSnapshotIn(ctx, q, out)
		return s.record(ctx, q, sc, e)
	})
	if err != nil {
		return nil, err
	}
	s.log.Info("org status page created", "org_id", sc.OrgID, "page", out.Slug, "actor", sc.Actor)
	return out, nil
}

// OrgStatusPage returns one page of the org.
func (s *Service) OrgStatusPage(ctx context.Context, sc domain.Scope, slug string) (*domain.StatusPage, error) {
	if err := requireOrgAdmin(sc); err != nil {
		return nil, err
	}
	row, err := s.db.Read().GetOrgStatusPage(ctx, db.GetOrgStatusPageParams{OrgID: sc.OrgID, Slug: slug})
	if err != nil {
		return nil, notFoundIfNoRows(err, "status page")
	}
	return statusPageFromRow(row), nil
}

// ListOrgStatusPages lists the org's own pages by title.
func (s *Service) ListOrgStatusPages(ctx context.Context, sc domain.Scope) ([]*domain.StatusPage, error) {
	if err := requireOrgAdmin(sc); err != nil {
		return nil, err
	}
	rows, err := s.db.Read().ListOrgStatusPages(ctx, sc.OrgID)
	if err != nil {
		return nil, err
	}
	out := make([]*domain.StatusPage, 0, len(rows))
	for _, r := range rows {
		out = append(out, statusPageFromRow(r))
	}
	return out, nil
}

// UpdateOrgStatusPage replaces a page of the org. The slug may change; an
// empty password keeps the current one.
func (s *Service) UpdateOrgStatusPage(ctx context.Context, sc domain.Scope, slug string, p *domain.StatusPage, password string) (*domain.StatusPage, error) {
	cur, err := s.OrgStatusPage(ctx, sc, slug)
	if err != nil {
		return nil, err
	}
	next := *p
	next.OrgID, next.ProjectID = cur.OrgID, ""
	if next.Slug == "" {
		next.Slug = cur.Slug
	}
	if err := preparePage(&next, password, cur.PasswordHash); err != nil {
		return nil, err
	}
	var out *domain.StatusPage
	err = s.db.Tx(ctx, func(q *db.Queries) error {
		if err := s.checkOrgProjects(ctx, q, sc.OrgID, next.Projects); err != nil {
			return err
		}
		row, err := q.UpdateOrgStatusPage(ctx, db.UpdateOrgStatusPageParams{
			Slug: next.Slug, Title: next.Title, MatchTags: tagsJSON(next.MatchTags), Projects: idsJSON(next.Projects), GroupBy: next.GroupBy,
			Incidents: next.Incidents, Public: next.Public, PasswordHash: ptrs(next.PasswordHash), CustomDomain: ptrs(next.CustomDomain),
			UpdatedAt: domain.Millis(s.now()), OrgID: sc.OrgID, ID: cur.ID,
		})
		if err != nil {
			return conflictIfUnique(err, "the address "+next.Slug+" is taken")
		}
		out = statusPageFromRow(row)
		e := orgEntry(sc.OrgID, "page.update", out.Slug, out.ID)
		e.Before, e.After = s.pageSnapshotIn(ctx, q, cur), s.pageSnapshotIn(ctx, q, out)
		e.Detail = map[string]any{"fields": changedFields(e.Before, e.After)}
		return s.record(ctx, q, sc, e)
	})
	if err != nil {
		return nil, err
	}
	s.log.Info("org status page updated", "org_id", sc.OrgID, "page", out.Slug, "actor", sc.Actor)
	return out, nil
}

// DeleteOrgStatusPage removes a page of the org.
func (s *Service) DeleteOrgStatusPage(ctx context.Context, sc domain.Scope, slug string) error {
	cur, err := s.OrgStatusPage(ctx, sc, slug)
	if err != nil {
		return err
	}
	err = s.db.Tx(ctx, func(q *db.Queries) error {
		n, err := q.DeleteOrgStatusPage(ctx, db.DeleteOrgStatusPageParams{OrgID: sc.OrgID, Slug: slug})
		if err != nil {
			return err
		}
		if n == 0 {
			return domain.NotFound("status page")
		}
		e := orgEntry(sc.OrgID, "page.delete", cur.Slug, cur.ID)
		e.Before = s.pageSnapshotIn(ctx, q, cur)
		return s.record(ctx, q, sc, e)
	})
	if err != nil {
		return err
	}
	s.log.Info("org status page deleted", "org_id", sc.OrgID, "page", slug, "actor", sc.Actor)
	return nil
}

// pageSnapshotIn is pageSnapshot with an org page's projects named by slug,
// read inside the transaction.
func (s *Service) pageSnapshotIn(ctx context.Context, q *db.Queries, p *domain.StatusPage) string {
	if !p.IsOrg() || len(p.Projects) == 0 {
		return pageSnapshot(p, nil)
	}
	rows, err := q.ListProjects(ctx, p.OrgID)
	if err != nil {
		return pageSnapshot(p, nil)
	}
	slugs := make(map[string]string, len(rows))
	for _, r := range rows {
		slugs[r.ID] = r.Slug
	}
	return pageSnapshot(p, slugs)
}
