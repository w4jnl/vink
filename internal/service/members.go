package service

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/w4jnl/vink/internal/audit"
	"github.com/w4jnl/vink/internal/db"
	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/secrets"
)

// ListMembers lists the org's people with their roles, for admins and owners.
func (s *Service) ListMembers(ctx context.Context, sc domain.Scope) ([]domain.Member, error) {
	if err := requireOrgAdmin(sc); err != nil {
		return nil, err
	}
	rows, err := s.db.Read().ListOrgMembers(ctx, sc.OrgID)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Member, 0, len(rows))
	for _, r := range rows {
		m := domain.Member{UserID: r.UserID, Subject: r.Subject, Email: r.Email, DisplayName: r.DisplayName, Role: domain.Role(r.Role), Source: r.Source, UserSource: r.UserSource, Disabled: r.DisabledAt != nil}
		if ms, ok := r.LastSeenAt.(int64); ok {
			t := domain.FromMillis(ms)
			m.LastSeenAt = &t
		}
		out = append(out, m)
	}
	return out, nil
}

func inviteFromRow(r db.Invite) domain.Invite {
	return domain.Invite{
		ID: r.ID, OrgID: r.OrgID, Role: domain.Role(r.Role), Note: r.Note, CreatedAt: domain.FromMillis(r.CreatedAt), ExpiresAt: domain.FromMillis(r.ExpiresAt),
		UsedAt: domain.FromMillisPtr(r.UsedAt), RevokedAt: domain.FromMillisPtr(r.RevokedAt),
	}
}

// CreateInvite makes a one-time link into the org as a new local account.
// Owners and instance admins may invite owners; admins the rest. The
// token is returned once.
func (s *Service) CreateInvite(ctx context.Context, sc domain.Scope, note string, role domain.Role) (*domain.Invite, string, error) {
	if err := requireOrgAdmin(sc); err != nil {
		return nil, "", err
	}
	if !role.Valid() {
		return nil, "", validation("role", "must be owner, admin, member or viewer")
	}
	if role == domain.RoleOwner && !sc.InstanceAdmin && !sc.CanOwnOrg() {
		return nil, "", domain.ErrForbidden
	}
	note = strings.TrimSpace(note)
	if note == "" {
		return nil, "", validation("note", "say who the invite is for")
	}
	if len(note) > 80 {
		return nil, "", validation("note", "at most 80 characters")
	}
	token, err := secrets.NewLinkToken("iv_")
	if err != nil {
		return nil, "", err
	}
	now := s.now()
	var createdBy *string
	if sc.UserID != "" {
		createdBy = &sc.UserID
	}
	var out domain.Invite
	err = s.db.Tx(ctx, func(q *db.Queries) error {
		row, err := q.CreateInvite(ctx, db.CreateInviteParams{
			ID: domain.NewID(), OrgID: sc.OrgID, Role: string(role), Note: note, TokenHash: secrets.HashLink(token), CreatedBy: createdBy,
			CreatedAt: domain.Millis(now), ExpiresAt: domain.Millis(now.Add(domain.InviteTTL)),
		})
		if err != nil {
			return err
		}
		out = inviteFromRow(row)
		e := orgEntry(sc.OrgID, "invite.create", note, out.ID)
		e.Detail = map[string]any{"role": string(role), "expires_at": out.ExpiresAt.UTC().Format(time.RFC3339)}
		return s.record(ctx, q, sc, e)
	})
	if err != nil {
		return nil, "", err
	}
	s.log.Info("invite created", "org_id", sc.OrgID, "role", role, "actor", sc.Actor)
	return &out, token, nil
}

// ListInvites lists the org's invites, newest first, for admins and owners.
func (s *Service) ListInvites(ctx context.Context, sc domain.Scope) ([]domain.Invite, error) {
	if err := requireOrgAdmin(sc); err != nil {
		return nil, err
	}
	rows, err := s.db.Read().ListInvitesForOrg(ctx, sc.OrgID)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Invite, 0, len(rows))
	for _, r := range rows {
		inv := inviteFromRow(db.Invite{ID: r.ID, OrgID: r.OrgID, Role: r.Role, Note: r.Note, CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt, UsedAt: r.UsedAt, RevokedAt: r.RevokedAt})
		inv.CreatedBy = nameOf(r.CreatedByName, r.CreatedBySubject)
		inv.UsedBy = strp(r.UsedBySubject)
		out = append(out, inv)
	}
	return out, nil
}

func nameOf(display, subject *string) string {
	if display != nil && *display != "" {
		return *display
	}
	return strp(subject)
}

// RevokeInvite stops an open link.
func (s *Service) RevokeInvite(ctx context.Context, sc domain.Scope, id string) error {
	if err := requireOrgAdmin(sc); err != nil {
		return err
	}
	row, err := s.db.Read().GetInvite(ctx, db.GetInviteParams{OrgID: sc.OrgID, ID: id})
	if err != nil {
		return notFoundIfNoRows(err, "invite")
	}
	return s.db.Tx(ctx, func(q *db.Queries) error {
		n, err := q.RevokeInvite(ctx, db.RevokeInviteParams{RevokedAt: ptri(domain.Millis(s.now())), OrgID: sc.OrgID, ID: id})
		if err != nil {
			return err
		}
		if n == 0 {
			return domain.Conflict("the invite is no longer open")
		}
		e := orgEntry(sc.OrgID, "invite.revoke", row.Note, id)
		e.Detail = map[string]any{"role": row.Role}
		return s.record(ctx, q, sc, e)
	})
}

// RemoveInvite deletes a link that nobody used (expired or revoked), to
// tidy the list.
func (s *Service) RemoveInvite(ctx context.Context, sc domain.Scope, id string) error {
	if err := requireOrgAdmin(sc); err != nil {
		return err
	}
	n, err := s.db.Write().DeleteInvite(ctx, db.DeleteInviteParams{OrgID: sc.OrgID, ID: id})
	if err != nil {
		return err
	}
	if n == 0 {
		return domain.NotFound("invite")
	}
	return nil
}

// InviteByToken resolves a link for the invite page; a used, revoked or
// expired link still resolves, so the page can say so.
func (s *Service) InviteByToken(ctx context.Context, token string) (*domain.Invite, error) {
	r, err := s.db.Read().GetInviteByHash(ctx, secrets.HashLink(strings.TrimSpace(token)))
	if err != nil {
		return nil, notFoundIfNoRows(err, "invite")
	}
	inv := inviteFromRow(db.Invite{ID: r.ID, OrgID: r.OrgID, Role: r.Role, Note: r.Note, CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt, UsedAt: r.UsedAt, RevokedAt: r.RevokedAt})
	inv.OrgSlug, inv.OrgName, inv.CreatedBy = r.OrgSlug, r.OrgName, nameOf(r.CreatedByName, r.CreatedBySubject)
	return &inv, nil
}

// AcceptInvite creates the local account and its membership and uses the
// link, in one transaction. A link that is not open fails as not found,
// so a link never tells whether someone joined.
func (s *Service) AcceptInvite(ctx context.Context, token, subject, name, password string) (*domain.User, error) {
	inv, err := s.InviteByToken(ctx, token)
	if err != nil {
		return nil, err
	}
	if inv.State(s.now()) != domain.InviteOpen {
		return nil, domain.NotFound("invite")
	}
	var out *domain.User
	err = s.db.Tx(ctx, func(q *db.Queries) error {
		u, err := s.createLocalUser(ctx, q, subject, "", name, password, false)
		if err != nil {
			return err
		}
		now := s.now()
		n, err := q.UseInvite(ctx, db.UseInviteParams{UsedAt: ptri(domain.Millis(now)), UsedBy: &u.ID, ID: inv.ID, ExpiresAt: domain.Millis(now)})
		if err != nil {
			return err
		}
		if n == 0 {
			return domain.NotFound("invite")
		}
		if err := q.UpsertLocalMembership(ctx, db.UpsertLocalMembershipParams{UserID: u.ID, OrgID: inv.OrgID, Role: string(inv.Role), CreatedAt: domain.Millis(now)}); err != nil {
			return err
		}
		out = u
		sc := domain.Scope{UserID: u.ID, Actor: "user:" + u.Subject}
		if err := s.record(ctx, q, sc, audit.Entry{Action: "user.create", Target: u.Subject, TargetID: u.ID, Detail: map[string]any{"instance_admin": false, "invite": inv.ID}}); err != nil {
			return err
		}
		e := orgEntry(inv.OrgID, "invite.accept", inv.Note, inv.ID)
		e.Detail = map[string]any{"role": string(inv.Role), "subject": u.Subject}
		return s.record(ctx, q, sc, e)
	})
	if err != nil {
		return nil, err
	}
	s.log.Info("invite accepted", "org_id", inv.OrgID, "subject", out.Subject)
	return out, nil
}

// TransferOwnership makes another member the owner; the acting owner
// stays on as admin. Owners and instance admins only.
func (s *Service) TransferOwnership(ctx context.Context, sc domain.Scope, newOwnerID string) error {
	if sc.OrgID == "" || (!sc.InstanceAdmin && !sc.CanOwnOrg()) {
		return domain.ErrForbidden
	}
	target, err := s.db.Read().GetMembership(ctx, db.GetMembershipParams{UserID: newOwnerID, OrgID: sc.OrgID})
	if err != nil {
		return notFoundIfNoRows(err, "member")
	}
	if target.UserID == sc.UserID {
		return validation("new_owner", "pick someone else; you are the owner already")
	}
	user, err := s.UserByID(ctx, newOwnerID)
	if err != nil {
		return err
	}
	now := s.now()
	return s.db.Tx(ctx, func(q *db.Queries) error {
		if err := q.UpsertLocalMembership(ctx, db.UpsertLocalMembershipParams{UserID: newOwnerID, OrgID: sc.OrgID, Role: string(domain.RoleOwner), CreatedAt: domain.Millis(now)}); err != nil {
			return err
		}
		if sc.UserID != "" {
			if cur, err := q.GetMembership(ctx, db.GetMembershipParams{UserID: sc.UserID, OrgID: sc.OrgID}); err == nil && cur.Role == string(domain.RoleOwner) {
				if err := q.SetMembershipRole(ctx, db.SetMembershipRoleParams{Role: string(domain.RoleAdmin), UserID: sc.UserID, OrgID: sc.OrgID}); err != nil {
					return err
				}
			}
		}
		e := orgEntry(sc.OrgID, "org.transfer", user.Subject, user.ID)
		e.Detail = map[string]any{"from_role": target.Role}
		return s.record(ctx, q, sc, e)
	})
}

// DeleteOrg removes an org that has no projects left; memberships,
// invites, agents and keys go with it. Owners and instance admins only.
func (s *Service) DeleteOrg(ctx context.Context, sc domain.Scope) error {
	if sc.OrgID == "" || (!sc.InstanceAdmin && !sc.CanOwnOrg()) {
		return domain.ErrForbidden
	}
	org, err := s.OrgByID(ctx, sc.OrgID)
	if err != nil {
		return err
	}
	n, err := s.db.Read().CountProjects(ctx, sc.OrgID)
	if err != nil {
		return err
	}
	if n > 0 {
		return validation("org", "delete its projects first; it has "+plural2n(int(n), "project", "projects"))
	}
	return s.db.Tx(ctx, func(q *db.Queries) error {
		deleted, err := q.DeleteOrg(ctx, sc.OrgID)
		if err != nil {
			return err
		}
		if deleted == 0 {
			return domain.NotFound("org")
		}
		e := orgEntry(sc.OrgID, "org.delete", org.Slug, org.ID)
		e.Before = orgSnapshot(org)
		return s.record(ctx, q, sc, e)
	})
}

func plural2n(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}
