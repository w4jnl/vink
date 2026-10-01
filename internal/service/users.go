package service

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/w4jnl/vink/internal/db"
	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/secrets"
)

// MinPasswordLen is the shortest accepted local password.
const MinPasswordLen = 8

func userFromRow(r db.User) *domain.User {
	return &domain.User{
		ID: r.ID, Subject: r.Subject, Email: r.Email, DisplayName: r.DisplayName, HasPassword: r.PasswordHash != nil,
		InstanceAdmin: r.IsInstanceAdmin, CreatedAt: domain.FromMillis(r.CreatedAt),
	}
}

func validateSubject(subject string) error {
	subject = strings.TrimSpace(subject)
	if subject == "" || utf8.RuneCountInString(subject) > 120 || strings.ContainsAny(subject, " \t\n") {
		return (&domain.ValidationError{Errors: []domain.FieldError{{Field: "user", Msg: "must be a login name without spaces, at most 120 characters"}}}).OrNil()
	}
	return nil
}

// CreateLocalUser creates a user who signs in with a password. Instance
// admins only.
func (s *Service) CreateLocalUser(ctx context.Context, sc domain.Scope, subject, email, name, password string, instanceAdmin bool) (*domain.User, error) {
	if err := requireInstanceAdmin(sc); err != nil {
		return nil, err
	}
	return s.createLocalUser(ctx, s.db.Write(), subject, email, name, password, instanceAdmin)
}

func (s *Service) createLocalUser(ctx context.Context, q *db.Queries, subject, email, name, password string, instanceAdmin bool) (*domain.User, error) {
	subject = strings.TrimSpace(subject)
	if err := validateSubject(subject); err != nil {
		return nil, err
	}
	if utf8.RuneCountInString(password) < MinPasswordLen {
		return nil, (&domain.ValidationError{Errors: []domain.FieldError{{Field: "password", Msg: fmt.Sprintf("must be at least %d characters", MinPasswordLen)}}}).OrNil()
	}
	hash, err := secrets.HashPassword(password)
	if err != nil {
		return nil, err
	}
	if name == "" {
		name = subject
	}
	row, err := q.CreateUser(ctx, db.CreateUserParams{
		ID: domain.NewID(), Subject: subject, Email: strings.TrimSpace(email), DisplayName: strings.TrimSpace(name),
		PasswordHash: &hash, IsInstanceAdmin: instanceAdmin, CreatedAt: domain.Millis(s.now()),
	})
	if err != nil {
		return nil, conflictIfUnique(err, "a user named "+subject+" exists")
	}
	return userFromRow(row), nil
}

// VerifyPassword checks a local login. Unknown users cost the same time
// as a wrong password.
func (s *Service) VerifyPassword(ctx context.Context, subject, password string) (*domain.User, error) {
	row, err := s.db.Read().GetUserBySubject(ctx, strings.TrimSpace(subject))
	hash := ""
	if err == nil && row.PasswordHash != nil {
		hash = *row.PasswordHash
	}
	if !secrets.VerifyPassword(hash, password) || err != nil {
		return nil, domain.ErrUnauthorized
	}
	return userFromRow(row), nil
}

// SetPassword changes a user's password. Instance admins, or the user
// themselves.
func (s *Service) SetPassword(ctx context.Context, sc domain.Scope, userID, password string) error {
	if !sc.InstanceAdmin && sc.UserID != userID {
		return domain.ErrForbidden
	}
	if utf8.RuneCountInString(password) < MinPasswordLen {
		return (&domain.ValidationError{Errors: []domain.FieldError{{Field: "password", Msg: fmt.Sprintf("must be at least %d characters", MinPasswordLen)}}}).OrNil()
	}
	hash, err := secrets.HashPassword(password)
	if err != nil {
		return err
	}
	return s.db.Write().SetUserPassword(ctx, db.SetUserPasswordParams{PasswordHash: &hash, ID: userID})
}

// UserBySubject looks a user up by login name or proxy subject.
func (s *Service) UserBySubject(ctx context.Context, subject string) (*domain.User, error) {
	row, err := s.db.Read().GetUserBySubject(ctx, subject)
	if err != nil {
		return nil, notFoundIfNoRows(err, "user")
	}
	return userFromRow(row), nil
}

// UserByID looks a user up by id.
func (s *Service) UserByID(ctx context.Context, id string) (*domain.User, error) {
	row, err := s.db.Read().GetUser(ctx, id)
	if err != nil {
		return nil, notFoundIfNoRows(err, "user")
	}
	return userFromRow(row), nil
}

// ListUsers lists every user. Instance admins only.
func (s *Service) ListUsers(ctx context.Context, sc domain.Scope) ([]*domain.User, error) {
	if err := requireInstanceAdmin(sc); err != nil {
		return nil, err
	}
	rows, err := s.db.Read().ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*domain.User, 0, len(rows))
	for _, r := range rows {
		out = append(out, userFromRow(r))
	}
	return out, nil
}

// SetInstanceAdmin grants or revokes instance admin. Instance admins only.
func (s *Service) SetInstanceAdmin(ctx context.Context, sc domain.Scope, subject string, admin bool) error {
	if err := requireInstanceAdmin(sc); err != nil {
		return err
	}
	u, err := s.UserBySubject(ctx, subject)
	if err != nil {
		return err
	}
	_, err = s.db.Write().SetInstanceAdmin(ctx, db.SetInstanceAdminParams{IsInstanceAdmin: admin, ID: u.ID})
	return err
}

// EnsureProxyUser creates a proxy-authenticated user on first sight and
// keeps email and name current.
func (s *Service) EnsureProxyUser(ctx context.Context, subject, email, name string) (*domain.User, error) {
	row, err := s.db.Read().GetUserBySubject(ctx, subject)
	if err == nil {
		if (email != "" && row.Email != email) || (name != "" && row.DisplayName != name) {
			if email == "" {
				email = row.Email
			}
			if name == "" {
				name = row.DisplayName
			}
			if err := s.db.Write().UpdateUserProfile(ctx, db.UpdateUserProfileParams{Email: email, DisplayName: name, ID: row.ID}); err != nil {
				return nil, err
			}
			row.Email, row.DisplayName = email, name
		}
		return userFromRow(row), nil
	}
	if !db.IsNotFound(err) {
		return nil, err
	}
	if name == "" {
		name = subject
	}
	created, err := s.db.Write().CreateUser(ctx, db.CreateUserParams{
		ID: domain.NewID(), Subject: subject, Email: email, DisplayName: name, PasswordHash: nil, IsInstanceAdmin: false, CreatedAt: domain.Millis(s.now()),
	})
	if err != nil {
		if db.IsUniqueViolation(err) {
			return s.EnsureProxyUser(ctx, subject, email, name)
		}
		return nil, err
	}
	s.log.Info("user created from proxy identity", "subject", subject)
	return userFromRow(created), nil
}

// SetMembership grants role in orgID with source local. Org admins of that
// org or instance admins.
func (s *Service) SetMembership(ctx context.Context, sc domain.Scope, userID, orgID string, role domain.Role) error {
	if !sc.InstanceAdmin && (sc.OrgID != orgID || !sc.CanAdminOrg()) {
		return domain.ErrForbidden
	}
	if !role.Valid() {
		return (&domain.ValidationError{Errors: []domain.FieldError{{Field: "role", Msg: "must be owner, admin, member or viewer"}}}).OrNil()
	}
	if _, err := s.db.Read().GetUser(ctx, userID); err != nil {
		return notFoundIfNoRows(err, "user")
	}
	if _, err := s.db.Read().GetOrg(ctx, orgID); err != nil {
		return notFoundIfNoRows(err, "org")
	}
	if role != domain.RoleOwner {
		if err := s.keepLastOwner(ctx, userID, orgID); err != nil {
			return err
		}
	}
	return s.db.Write().UpsertMembership(ctx, db.UpsertMembershipParams{UserID: userID, OrgID: orgID, Role: string(role), Source: "local", CreatedAt: domain.Millis(s.now())})
}

// RemoveMembership ends a user's role in an org. The last owner stays.
func (s *Service) RemoveMembership(ctx context.Context, sc domain.Scope, userID, orgID string) error {
	if !sc.InstanceAdmin && (sc.OrgID != orgID || !sc.CanAdminOrg()) {
		return domain.ErrForbidden
	}
	if err := s.keepLastOwner(ctx, userID, orgID); err != nil {
		return err
	}
	n, err := s.db.Write().DeleteMembership(ctx, db.DeleteMembershipParams{UserID: userID, OrgID: orgID})
	if err != nil {
		return err
	}
	if n == 0 {
		return domain.NotFound("membership")
	}
	s.log.Info("membership removed", "org_id", orgID, "user_id", userID, "actor", sc.Actor)
	return nil
}

// keepLastOwner refuses to demote or remove the only owner of an org.
func (s *Service) keepLastOwner(ctx context.Context, userID, orgID string) error {
	cur, err := s.db.Read().GetMembership(ctx, db.GetMembershipParams{UserID: userID, OrgID: orgID})
	if err != nil || cur.Role != string(domain.RoleOwner) {
		return nil // not a member, or not an owner: nothing to protect
	}
	members, err := s.db.Read().ListMembershipsForOrg(ctx, orgID)
	if err != nil {
		return err
	}
	owners := 0
	for _, m := range members {
		if m.Role == string(domain.RoleOwner) {
			owners++
		}
	}
	if owners <= 1 {
		return (&domain.ValidationError{Errors: []domain.FieldError{{Field: "role", Msg: "the last owner of an org cannot be demoted or removed; make someone else an owner first"}}}).OrNil()
	}
	return nil
}

// MembershipsForUser lists the user's org roles.
func (s *Service) MembershipsForUser(ctx context.Context, userID string) ([]domain.Membership, error) {
	rows, err := s.db.Read().ListMembershipsForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Membership, 0, len(rows))
	for _, r := range rows {
		out = append(out, domain.Membership{UserID: r.UserID, OrgID: r.OrgID, OrgSlug: r.OrgSlug, OrgName: r.OrgName, Role: domain.Role(r.Role), Source: r.Source})
	}
	return out, nil
}

// SyncHeaderMemberships makes the user's header-derived memberships equal
// to roles (org slug to role). Local memberships are untouched. Unknown
// org slugs are ignored. It writes only when something differs.
func (s *Service) SyncHeaderMemberships(ctx context.Context, userID string, roles map[string]domain.Role) error {
	current, err := s.MembershipsForUser(ctx, userID)
	if err != nil {
		return err
	}
	want := map[string]domain.Role{}
	for slug, role := range roles {
		org, err := s.db.Read().GetOrgBySlug(ctx, slug)
		if err != nil {
			if db.IsNotFound(err) {
				continue
			}
			return err
		}
		want[org.ID] = role
	}
	same := true
	seen := 0
	for _, m := range current {
		if m.Source != "header" {
			continue
		}
		seen++
		if want[m.OrgID] != m.Role {
			same = false
		}
	}
	if same && seen == len(want) {
		return nil
	}
	return s.db.Tx(ctx, func(q *db.Queries) error {
		if err := q.DeleteHeaderMembershipsForUser(ctx, userID); err != nil {
			return err
		}
		for orgID, role := range want {
			if err := q.UpsertMembership(ctx, db.UpsertMembershipParams{UserID: userID, OrgID: orgID, Role: string(role), Source: "header", CreatedAt: domain.Millis(s.now())}); err != nil {
				return err
			}
		}
		return nil
	})
}

// BootstrapInput is what `vink admin init` provides.
type BootstrapInput struct {
	OrgSlug, OrgName         string
	ProjectSlug, ProjectName string
	Timezone                 string
	Subject, Email, Name     string
	Password                 string
}

// BootstrapResult is what it prints once.
type BootstrapResult struct {
	User        *domain.User
	Org         *domain.Org
	Project     *domain.Project
	APIKey      *domain.APIKey
	APIKeyPlain string
}

// Bootstrap initialises an empty database: instance admin, first org,
// first project, one rw API key. It refuses when any user exists.
func (s *Service) Bootstrap(ctx context.Context, in BootstrapInput) (*BootstrapResult, error) {
	n, err := s.db.Read().CountUsers(ctx)
	if err != nil {
		return nil, err
	}
	if n > 0 {
		return nil, domain.Conflict("the database already has users; use admin org create and admin user create instead")
	}
	admin := domain.Scope{InstanceAdmin: true, Role: domain.RoleOwner, Actor: "cli:admin-init"}
	if in.ProjectSlug == "" {
		in.ProjectSlug = in.OrgSlug
	}
	if in.ProjectName == "" {
		in.ProjectName = in.OrgName
	}
	res := &BootstrapResult{}
	res.User, err = s.createLocalUser(ctx, s.db.Write(), in.Subject, in.Email, in.Name, in.Password, true)
	if err != nil {
		return nil, err
	}
	res.Org, err = s.CreateOrg(ctx, admin, in.OrgSlug, in.OrgName)
	if err != nil {
		return nil, err
	}
	if err := s.db.Write().UpsertMembership(ctx, db.UpsertMembershipParams{UserID: res.User.ID, OrgID: res.Org.ID, Role: string(domain.RoleOwner), Source: "local", CreatedAt: domain.Millis(s.now())}); err != nil {
		return nil, err
	}
	res.Project, err = s.CreateProject(ctx, admin, res.Org.ID, in.ProjectSlug, in.ProjectName, in.Timezone)
	if err != nil {
		return nil, err
	}
	sc := domain.Scope{OrgID: res.Org.ID, ProjectID: res.Project.ID, UserID: res.User.ID, Role: domain.RoleOwner, InstanceAdmin: true, Actor: "cli:admin-init"}
	res.APIKey, res.APIKeyPlain, err = s.CreateAPIKey(ctx, sc, "admin init", domain.AccessRW)
	if err != nil {
		return nil, err
	}
	return res, nil
}
