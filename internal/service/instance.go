package service

import (
	"context"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/w4jnl/vink/internal/audit"
	"github.com/w4jnl/vink/internal/db"
	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/secrets"
)

// ResetLinkTTL is how long a password reset link works.
const ResetLinkTTL = 24 * time.Hour

// OrgSummary is one row of the instance admin's org list.
type OrgSummary struct {
	Org      domain.Org
	Projects int
	Owners   []string
	Monitors int
	Agents   int
}

// OrgSummaries lists every org with what the instance admin wants to see.
func (s *Service) OrgSummaries(ctx context.Context, sc domain.Scope) ([]OrgSummary, error) {
	orgs, err := s.ListOrgs(ctx, sc)
	if err != nil {
		return nil, err
	}
	members, err := s.db.Read().ListAllMemberships(ctx)
	if err != nil {
		return nil, err
	}
	owners := map[string][]string{}
	if len(members) > 0 {
		users, err := s.db.Read().ListUsers(ctx)
		if err != nil {
			return nil, err
		}
		names := map[string]string{}
		for _, u := range users {
			names[u.ID] = u.Subject
		}
		for _, m := range members {
			if m.Role == string(domain.RoleOwner) {
				owners[m.OrgID] = append(owners[m.OrgID], names[m.UserID])
			}
		}
	}
	out := make([]OrgSummary, 0, len(orgs))
	for _, o := range orgs {
		row := OrgSummary{Org: *o, Owners: owners[o.ID]}
		n, err := s.db.Read().CountProjects(ctx, o.ID)
		if err != nil {
			return nil, err
		}
		row.Projects = int(n)
		if n, err := s.db.Read().CountMonitorsInOrg(ctx, o.ID); err == nil {
			row.Monitors = int(n)
		}
		if n, err := s.db.Read().CountAgents(ctx, o.ID); err == nil {
			row.Agents = int(n)
		}
		out = append(out, row)
	}
	return out, nil
}

// CreateOrgWithOwner makes an org, gives an existing user its first owner
// role and sets the quotas, in one transaction. Instance admins only.
func (s *Service) CreateOrgWithOwner(ctx context.Context, sc domain.Scope, slug, name, ownerSubject string, quotaMonitors, quotaAgents *int64) (*domain.Org, error) {
	if err := requireInstanceAdmin(sc); err != nil {
		return nil, err
	}
	ownerSubject = strings.TrimSpace(ownerSubject)
	var owner *domain.User
	if ownerSubject != "" {
		u, err := s.UserBySubject(ctx, ownerSubject)
		if err != nil {
			return nil, validation("owner", "no user named "+ownerSubject+"; invite them from the org's Members tab afterwards, or leave this empty")
		}
		owner = u
	}
	var out *domain.Org
	err := s.db.Tx(ctx, func(q *db.Queries) error {
		tx := s.inTx(q)
		org, err := tx.CreateOrg(ctx, sc, slug, name)
		if err != nil {
			return err
		}
		if quotaMonitors != nil || quotaAgents != nil {
			if err := q.SetOrgQuotas(ctx, db.SetOrgQuotasParams{QuotaMonitors: quotaMonitors, QuotaAgents: quotaAgents, ID: org.ID}); err != nil {
				return err
			}
			org.QuotaMonitors, org.QuotaAgents = quotaMonitors, quotaAgents
		}
		if owner != nil {
			if err := q.UpsertLocalMembership(ctx, db.UpsertLocalMembershipParams{UserID: owner.ID, OrgID: org.ID, Role: string(domain.RoleOwner), CreatedAt: domain.Millis(s.now())}); err != nil {
				return err
			}
			e := orgEntry(org.ID, "member.role", owner.Subject, owner.ID)
			e.Detail = map[string]any{"from": "", "to": "owner"}
			if err := s.record(ctx, q, sc, e); err != nil {
				return err
			}
		}
		out = org
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// UpdateOrg renames an org and sets its quotas. Instance admins only.
func (s *Service) UpdateOrg(ctx context.Context, sc domain.Scope, orgID, name string, quotaMonitors, quotaAgents *int64) (*domain.Org, error) {
	if err := requireInstanceAdmin(sc); err != nil {
		return nil, err
	}
	cur, err := s.OrgByID(ctx, orgID)
	if err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = cur.Slug
	}
	next := *cur
	next.Name, next.QuotaMonitors, next.QuotaAgents = name, quotaMonitors, quotaAgents
	err = s.db.Tx(ctx, func(q *db.Queries) error {
		if err := q.UpdateOrgName(ctx, db.UpdateOrgNameParams{Name: name, ID: orgID}); err != nil {
			return err
		}
		if err := q.SetOrgQuotas(ctx, db.SetOrgQuotasParams{QuotaMonitors: quotaMonitors, QuotaAgents: quotaAgents, ID: orgID}); err != nil {
			return err
		}
		e := orgEntry(orgID, "org.update", cur.Slug, orgID)
		e.Before, e.After = orgSnapshot(cur), orgSnapshot(&next)
		e.Detail = map[string]any{"fields": changedFields(e.Before, e.After)}
		if len(e.Detail["fields"].([]string)) == 0 {
			return nil
		}
		return s.record(ctx, q, sc, e)
	})
	if err != nil {
		return nil, err
	}
	return &next, nil
}

// InstanceUser is one row of the instance admin's user list.
type InstanceUser struct {
	domain.User
	Memberships []domain.Membership
	LastSeenAt  *time.Time
}

// ListInstanceUsers lists everyone with their roles. Instance admins only.
func (s *Service) ListInstanceUsers(ctx context.Context, sc domain.Scope) ([]InstanceUser, error) {
	if err := requireInstanceAdmin(sc); err != nil {
		return nil, err
	}
	rows, err := s.db.Read().ListUsersWithSeen(ctx)
	if err != nil {
		return nil, err
	}
	members, err := s.db.Read().ListAllMemberships(ctx)
	if err != nil {
		return nil, err
	}
	byUser := map[string][]domain.Membership{}
	for _, m := range members {
		byUser[m.UserID] = append(byUser[m.UserID], domain.Membership{UserID: m.UserID, OrgID: m.OrgID, OrgSlug: m.OrgSlug, OrgName: m.OrgName, Role: domain.Role(m.Role), Source: m.Source})
	}
	out := make([]InstanceUser, 0, len(rows))
	for _, r := range rows {
		u := InstanceUser{User: *userFromRow(db.User{ID: r.ID, Subject: r.Subject, Email: r.Email, DisplayName: r.DisplayName, PasswordHash: r.PasswordHash, IsInstanceAdmin: r.IsInstanceAdmin, CreatedAt: r.CreatedAt, Source: r.Source, DisabledAt: r.DisabledAt, DisabledBy: r.DisabledBy, TotpSecret: r.TotpSecret, TotpEnabledAt: r.TotpEnabledAt, TotpLastStep: r.TotpLastStep, PasswordChangedAt: r.PasswordChangedAt}), Memberships: byUser[r.ID]}
		if ms, ok := r.LastSeenAt.(int64); ok {
			t := domain.FromMillis(ms)
			u.LastSeenAt = &t
		}
		out = append(out, u)
	}
	return out, nil
}

// SetUserDisabled blocks or allows sign-in; disabling ends every session.
// You cannot disable yourself. Instance admins only.
func (s *Service) SetUserDisabled(ctx context.Context, sc domain.Scope, userID string, disabled bool) error {
	if err := requireInstanceAdmin(sc); err != nil {
		return err
	}
	if disabled && userID == sc.UserID {
		return validation("user", "you cannot disable yourself")
	}
	u, err := s.UserByID(ctx, userID)
	if err != nil {
		return err
	}
	if u.Disabled() == disabled {
		return nil
	}
	now := s.now()
	return s.db.Tx(ctx, func(q *db.Queries) error {
		var at *int64
		var by *string
		action := "user.enable"
		if disabled {
			at, by, action = ptri(domain.Millis(now)), ptrs(strings.TrimPrefix(sc.Actor, "user:")), "user.disable"
			if err := q.DeleteUserSessions(ctx, userID); err != nil {
				return err
			}
		}
		if err := q.SetUserDisabled(ctx, db.SetUserDisabledParams{DisabledAt: at, DisabledBy: by, ID: userID}); err != nil {
			return err
		}
		return s.record(ctx, q, sc, audit.Entry{Action: action, Target: u.Subject, TargetID: u.ID})
	})
}

// ResetTOTP turns two-factor off for a user who lost the phone; they set
// it up again at the next sign-in. Instance admins, or the account itself.
func (s *Service) ResetTOTP(ctx context.Context, sc domain.Scope, userID string) error {
	if !sc.InstanceAdmin && sc.UserID != userID {
		return domain.ErrForbidden
	}
	u, err := s.UserByID(ctx, userID)
	if err != nil {
		return err
	}
	return s.db.Tx(ctx, func(q *db.Queries) error {
		if err := q.ResetUserTOTP(ctx, userID); err != nil {
			return err
		}
		if err := q.DeleteRecoveryCodes(ctx, userID); err != nil {
			return err
		}
		return s.record(ctx, q, sc, audit.Entry{Action: "user.totp_reset", Target: u.Subject, TargetID: u.ID})
	})
}

// CreateResetLink makes a one-time password reset link for a local
// account, valid for a day. The token is returned once. Instance admins only.
func (s *Service) CreateResetLink(ctx context.Context, sc domain.Scope, userID string) (token string, expires time.Time, err error) {
	if err := requireInstanceAdmin(sc); err != nil {
		return "", time.Time{}, err
	}
	u, err := s.UserByID(ctx, userID)
	if err != nil {
		return "", time.Time{}, err
	}
	if u.Source != "local" {
		return "", time.Time{}, validation("user", "only local accounts have a password")
	}
	token, err = secrets.NewLinkToken("rs_")
	if err != nil {
		return "", time.Time{}, err
	}
	now := s.now()
	expires = now.Add(ResetLinkTTL)
	var createdBy *string
	if sc.UserID != "" {
		createdBy = &sc.UserID
	}
	err = s.db.Tx(ctx, func(q *db.Queries) error {
		if err := q.CreateResetToken(ctx, db.CreateResetTokenParams{ID: domain.NewID(), UserID: userID, TokenHash: secrets.HashLink(token), CreatedBy: createdBy, CreatedAt: domain.Millis(now), ExpiresAt: domain.Millis(expires)}); err != nil {
			return err
		}
		e := audit.Entry{Action: "user.reset_link", Target: u.Subject, TargetID: u.ID, Detail: map[string]any{"expires_at": expires.UTC().Format(time.RFC3339)}}
		return s.record(ctx, q, sc, e)
	})
	if err != nil {
		return "", time.Time{}, err
	}
	return token, expires, nil
}

// ResetLink is what the reset page shows for a link.
type ResetLink struct {
	ID        string
	UserID    string
	Subject   string
	Name      string
	CreatedBy string
	ExpiresAt time.Time
	Open      bool
}

// ResetLinkByToken resolves a link; a used or expired one resolves with
// Open false, so the page can say so.
func (s *Service) ResetLinkByToken(ctx context.Context, token string) (*ResetLink, error) {
	r, err := s.db.Read().GetResetTokenByHash(ctx, secrets.HashLink(strings.TrimSpace(token)))
	if err != nil {
		return nil, notFoundIfNoRows(err, "reset link")
	}
	link := &ResetLink{ID: r.ID, UserID: r.UserID, Subject: r.Subject, Name: r.DisplayName, CreatedBy: nameOf(r.CreatedByName, r.CreatedBySubject), ExpiresAt: domain.FromMillis(r.ExpiresAt)}
	link.Open = r.UsedAt == nil && s.now().Before(link.ExpiresAt)
	return link, nil
}

// ResetPasswordByToken sets a new password through a link, uses the link
// up and signs the account out everywhere.
func (s *Service) ResetPasswordByToken(ctx context.Context, token, password string) (*domain.User, error) {
	link, err := s.ResetLinkByToken(ctx, token)
	if err != nil {
		return nil, err
	}
	if !link.Open {
		return nil, domain.NotFound("reset link")
	}
	if utf8.RuneCountInString(password) < MinPasswordLen {
		return nil, validation("password", "must be at least "+strconv.Itoa(MinPasswordLen)+" characters")
	}
	hash, err := secrets.HashPassword(password)
	if err != nil {
		return nil, err
	}
	u, err := s.UserByID(ctx, link.UserID)
	if err != nil {
		return nil, err
	}
	now := s.now()
	err = s.db.Tx(ctx, func(q *db.Queries) error {
		n, err := q.UseResetToken(ctx, db.UseResetTokenParams{UsedAt: ptri(domain.Millis(now)), ID: link.ID, ExpiresAt: domain.Millis(now)})
		if err != nil {
			return err
		}
		if n == 0 {
			return domain.NotFound("reset link")
		}
		if err := q.SetUserPassword(ctx, db.SetUserPasswordParams{PasswordHash: &hash, ID: u.ID}); err != nil {
			return err
		}
		if err := q.DeleteUserSessions(ctx, u.ID); err != nil {
			return err
		}
		sc := domain.Scope{UserID: u.ID, Actor: "user:" + u.Subject}
		return s.record(ctx, q, sc, audit.Entry{Action: "user.password", Target: u.Subject, TargetID: u.ID, Detail: map[string]any{"via_link": true}})
	})
	if err != nil {
		return nil, err
	}
	return u, nil
}
