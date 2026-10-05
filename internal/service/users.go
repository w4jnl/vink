package service

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/w4jnl/vink/internal/audit"
	"github.com/w4jnl/vink/internal/db"
	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/secrets"
)

// MinPasswordLen is the shortest accepted local password.
const MinPasswordLen = 8

func userFromRow(r db.User) *domain.User {
	return &domain.User{
		ID: r.ID, Subject: r.Subject, Email: r.Email, DisplayName: r.DisplayName, HasPassword: r.PasswordHash != nil, Source: r.Source,
		InstanceAdmin: r.IsInstanceAdmin, DisabledAt: domain.FromMillisPtr(r.DisabledAt), DisabledBy: strp(r.DisabledBy), TOTPEnabledAt: domain.FromMillisPtr(r.TotpEnabledAt), CreatedAt: domain.FromMillis(r.CreatedAt),
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
	var out *domain.User
	err := s.db.Tx(ctx, func(q *db.Queries) error {
		u, err := s.createLocalUser(ctx, q, subject, email, name, password, instanceAdmin)
		if err != nil {
			return err
		}
		out = u
		return s.record(ctx, q, sc, audit.Entry{Action: "user.create", Target: u.Subject, TargetID: u.ID, Detail: map[string]any{"instance_admin": instanceAdmin}})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
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
		PasswordHash: &hash, IsInstanceAdmin: instanceAdmin, Source: "local", CreatedAt: domain.Millis(s.now()),
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
	u, err := s.UserByID(ctx, userID)
	if err != nil {
		return err
	}
	return s.db.Tx(ctx, func(q *db.Queries) error {
		if err := q.SetUserPassword(ctx, db.SetUserPasswordParams{PasswordHash: &hash, ID: userID}); err != nil {
			return err
		}
		return s.record(ctx, q, sc, audit.Entry{Action: "user.password", Target: u.Subject, TargetID: u.ID})
	})
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
	if !admin && u.ID == sc.UserID {
		return validation("instance_admin", "you cannot take instance admin from yourself")
	}
	pol := s.AuthPolicy(ctx).For(u.Source)
	setting := SettingFor(u.Source)
	if u.Source != "local" && pol.GroupsDecide() {
		return validation("instance_admin", u.Subject+" gets instance admin from "+setting+".instance_admin_group while "+setting+".roles is groups")
	}
	if !admin && pol.Listed(u.Subject) {
		return validation("instance_admin", u.Subject+" is listed in "+setting+".instance_admins in vink.toml; take the name out there first")
	}
	if u.InstanceAdmin == admin {
		return nil
	}
	if !admin {
		if err := s.keepLastInstanceAdmin(ctx, u); err != nil {
			return err
		}
	}
	return s.db.Tx(ctx, func(q *db.Queries) error {
		if _, err := q.SetInstanceAdmin(ctx, db.SetInstanceAdminParams{IsInstanceAdmin: admin, ID: u.ID}); err != nil {
			return err
		}
		return s.record(ctx, q, sc, audit.Entry{Action: "user.instance_admin", Target: u.Subject, TargetID: u.ID, Detail: map[string]any{"admin": admin}})
	})
}

// keepLastInstanceAdmin refuses to demote or disable the only active
// instance admin, whoever asks: a person, a key or the CLI.
func (s *Service) keepLastInstanceAdmin(ctx context.Context, u *domain.User) error {
	if !u.InstanceAdmin || u.Disabled() {
		return nil
	}
	n, err := s.db.Read().CountActiveInstanceAdmins(ctx)
	if err != nil {
		return err
	}
	if n <= 1 {
		return validation("instance_admin", u.Subject+" is the last instance admin; make someone else instance admin first")
	}
	return nil
}

// CreateProviderUser creates an account for a person who signs in through
// the proxy or OIDC (source proxy or oidc), before their first visit, so a
// role can be given ahead. The name is normalised as that provider's
// settings normalise it. Instance admins only.
func (s *Service) CreateProviderUser(ctx context.Context, sc domain.Scope, subject, email, name, source string) (*domain.User, error) {
	if err := requireInstanceAdmin(sc); err != nil {
		return nil, err
	}
	if source != "proxy" && source != "oidc" {
		return nil, validation("source", "must be proxy or oidc; local accounts get a password")
	}
	subject = s.AuthPolicy(ctx).For(source).Normalize(subject)
	if err := validateSubject(subject); err != nil {
		return nil, err
	}
	var out *domain.User
	err := s.db.Tx(ctx, func(q *db.Queries) error {
		u, err := s.createProviderUser(ctx, q, subject, email, name, source)
		if err != nil {
			return err
		}
		out = u
		return s.record(ctx, q, sc, audit.Entry{Action: "user.create", Target: u.Subject, TargetID: u.ID, Detail: map[string]any{"source": source}})
	})
	return out, err
}

func (s *Service) createProviderUser(ctx context.Context, q *db.Queries, subject, email, name, source string) (*domain.User, error) {
	if name == "" {
		name = subject
	}
	row, err := q.CreateUser(ctx, db.CreateUserParams{
		ID: domain.NewID(), Subject: subject, Email: strings.TrimSpace(email), DisplayName: strings.TrimSpace(name),
		PasswordHash: nil, IsInstanceAdmin: false, Source: source, CreatedAt: domain.Millis(s.now()),
	})
	if err != nil {
		return nil, conflictIfUnique(err, "a user named "+subject+" exists")
	}
	return userFromRow(row), nil
}

// AddMember gives a person who signs in through a provider in vink mode a
// role in the scope's org, by the name the provider sends; a name vink has
// not seen yet becomes an account, so the role is there at the first
// visit. Org admins, and owners for an owner.
func (s *Service) AddMember(ctx context.Context, sc domain.Scope, subject string, role domain.Role) (*domain.User, error) {
	if sc.OrgID == "" || (!sc.InstanceAdmin && !sc.CanAdminOrg()) {
		return nil, domain.ErrForbidden
	}
	if !role.Valid() {
		return nil, validation("role", "must be owner, admin, member or viewer")
	}
	if role == domain.RoleOwner && !sc.InstanceAdmin && !sc.CanOwnOrg() {
		return nil, validation("role", "only an owner can make someone an owner")
	}
	pol := s.AuthPolicy(ctx)
	source := pol.AddSource()
	if source == "" {
		return nil, validation("subject", "adding people by name needs auth.proxy.roles or auth.oidc.roles set to vink; invite a local account instead")
	}
	subject = pol.For(source).Normalize(subject)
	if err := validateSubject(subject); err != nil {
		return nil, err
	}
	var out *domain.User
	err := s.db.Tx(ctx, func(q *db.Queries) error {
		var u *domain.User
		row, err := q.GetUserBySubject(ctx, subject)
		switch {
		case db.IsNotFound(err):
			if u, err = s.createProviderUser(ctx, q, subject, "", "", source); err != nil {
				return err
			}
			if err := s.record(ctx, q, sc, audit.Entry{Action: "user.create", Target: u.Subject, TargetID: u.ID, Detail: map[string]any{"source": source}}); err != nil {
				return err
			}
		case err != nil:
			return err
		default:
			u = userFromRow(row)
			if u.Source == "local" {
				return validation("subject", subject+" is a local account here; invite them, or ask an instance admin")
			}
			if pol.For(u.Source).GroupsDecide() {
				return validation("subject", subject+"’s roles come from the groups of "+SettingFor(u.Source)+"; change them there")
			}
			if _, err := q.GetMembership(ctx, db.GetMembershipParams{UserID: u.ID, OrgID: sc.OrgID}); err == nil {
				return domain.Conflict(subject + " is a member already")
			}
		}
		if err := q.UpsertLocalMembership(ctx, db.UpsertLocalMembershipParams{UserID: u.ID, OrgID: sc.OrgID, Role: string(role), CreatedAt: domain.Millis(s.now())}); err != nil {
			return err
		}
		out = u
		e := orgEntry(sc.OrgID, "member.role", u.Subject, u.ID)
		e.Detail = map[string]any{"from": "", "to": string(role), "added": true}
		return s.record(ctx, q, sc, e)
	})
	if err != nil {
		return nil, err
	}
	s.log.Info("member added by name", "org_id", sc.OrgID, "user", out.Subject, "role", role, "actor", sc.Actor)
	return out, nil
}

// derivedLocked refuses a change to a role that a provider's groups own:
// the next sync would undo it.
func (s *Service) derivedLocked(ctx context.Context, subject, orgID, source string) error {
	if source == "local" || !s.AuthPolicy(ctx).For(source).GroupsDecide() {
		return nil
	}
	org, err := s.db.Read().GetOrg(ctx, orgID)
	if err != nil {
		return notFoundIfNoRows(err, "org")
	}
	from := "the proxy’s groups"
	if source == "oidc" {
		from = "the identity provider’s groups"
	}
	setting := SettingFor(source)
	return validation("role", subject+"’s role in "+org.Slug+" comes from "+from+"; change it there, or set "+setting+".roles = \"vink\"")
}

// EnsureProxyUser creates a proxy-authenticated user on first sight and
// keeps email and name current.
func (s *Service) EnsureProxyUser(ctx context.Context, subject, email, name string) (*domain.User, error) {
	return s.EnsureExternalUser(ctx, subject, email, name, "proxy")
}

// EnsureExternalUser is EnsureProxyUser for any identity provider: source
// is proxy or oidc. A local account with the subject comes back as it is,
// for the caller to refuse.
func (s *Service) EnsureExternalUser(ctx context.Context, subject, email, name, source string) (*domain.User, error) {
	row, err := s.db.Read().GetUserBySubject(ctx, subject)
	if err == nil {
		if row.Source == "local" {
			// a provider never edits a local account; the caller refuses it
			return userFromRow(row), nil
		}
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
		ID: domain.NewID(), Subject: subject, Email: email, DisplayName: name, PasswordHash: nil, IsInstanceAdmin: false, Source: source, CreatedAt: domain.Millis(s.now()),
	})
	if err != nil {
		if db.IsUniqueViolation(err) {
			return s.EnsureExternalUser(ctx, subject, email, name, source)
		}
		return nil, err
	}
	s.log.Info("user created from identity provider", "subject", subject, "source", source)
	return userFromRow(created), nil
}

// AdoptProviderUser moves a proxy account to OIDC on its first OIDC
// sign-in: both are the same identity behind the provider, and a proxy
// account has no password to protect. Local accounts are never adopted.
func (s *Service) AdoptProviderUser(ctx context.Context, u *domain.User, source string) error {
	if u.Source == source {
		return nil
	}
	if u.Source == "local" {
		return domain.ErrForbidden
	}
	return s.db.Tx(ctx, func(q *db.Queries) error {
		if err := q.SetUserSource(ctx, db.SetUserSourceParams{Source: source, ID: u.ID}); err != nil {
			return err
		}
		from := u.Source
		u.Source = source
		return s.record(ctx, q, domain.Scope{}, audit.Entry{Action: "user.source", Target: u.Subject, TargetID: u.ID, Detail: map[string]any{"from": from, "to": source}})
	})
}

// SetDerivedInstanceAdmin sets the flag an identity provider's group
// decides, for proxy and OIDC accounts; a change is logged.
func (s *Service) SetDerivedInstanceAdmin(ctx context.Context, u *domain.User, admin bool, why string) error {
	if u.InstanceAdmin == admin {
		return nil
	}
	sc := domain.Scope{Actor: "", UserID: ""}
	return s.db.Tx(ctx, func(q *db.Queries) error {
		if _, err := q.SetInstanceAdmin(ctx, db.SetInstanceAdminParams{IsInstanceAdmin: admin, ID: u.ID}); err != nil {
			return err
		}
		u.InstanceAdmin = admin
		return s.record(ctx, q, sc, audit.Entry{Action: "user.instance_admin", Target: u.Subject, TargetID: u.ID, Detail: map[string]any{"admin": admin, "source": why}})
	})
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
	if role == domain.RoleOwner && !sc.InstanceAdmin && !sc.CanOwnOrg() {
		return validation("role", "only an owner can make someone an owner")
	}
	user, err := s.db.Read().GetUser(ctx, userID)
	if err != nil {
		return notFoundIfNoRows(err, "user")
	}
	if _, err := s.db.Read().GetOrg(ctx, orgID); err != nil {
		return notFoundIfNoRows(err, "org")
	}
	if cur, err := s.db.Read().GetMembership(ctx, db.GetMembershipParams{UserID: userID, OrgID: orgID}); err == nil {
		if err := s.derivedLocked(ctx, user.Subject, orgID, cur.Source); err != nil {
			return err
		}
	}
	if role != domain.RoleOwner {
		if err := s.keepLastOwner(ctx, userID, orgID); err != nil {
			return err
		}
	}
	from := ""
	if cur, err := s.db.Read().GetMembership(ctx, db.GetMembershipParams{UserID: userID, OrgID: orgID}); err == nil {
		from = cur.Role
	}
	return s.db.Tx(ctx, func(q *db.Queries) error {
		if err := q.UpsertLocalMembership(ctx, db.UpsertLocalMembershipParams{UserID: userID, OrgID: orgID, Role: string(role), CreatedAt: domain.Millis(s.now())}); err != nil {
			return err
		}
		e := orgEntry(orgID, "member.role", user.Subject, user.ID)
		e.Detail = map[string]any{"from": from, "to": string(role)}
		return s.record(ctx, q, sc, e)
	})
}

// RemoveMembership ends a user's role in an org. The last owner stays.
func (s *Service) RemoveMembership(ctx context.Context, sc domain.Scope, userID, orgID string) error {
	if !sc.InstanceAdmin && (sc.OrgID != orgID || !sc.CanAdminOrg()) {
		return domain.ErrForbidden
	}
	if err := s.keepLastOwner(ctx, userID, orgID); err != nil {
		return err
	}
	user, err := s.db.Read().GetUser(ctx, userID)
	if err != nil {
		return notFoundIfNoRows(err, "user")
	}
	cur, err := s.db.Read().GetMembership(ctx, db.GetMembershipParams{UserID: userID, OrgID: orgID})
	if err != nil {
		return notFoundIfNoRows(err, "membership")
	}
	if err := s.derivedLocked(ctx, user.Subject, orgID, cur.Source); err != nil {
		return err
	}
	err = s.db.Tx(ctx, func(q *db.Queries) error {
		n, err := q.DeleteMembership(ctx, db.DeleteMembershipParams{UserID: userID, OrgID: orgID})
		if err != nil {
			return err
		}
		if n == 0 {
			return domain.NotFound("membership")
		}
		e := orgEntry(orgID, "member.remove", user.Subject, user.ID)
		e.Detail = map[string]any{"from": cur.Role}
		return s.record(ctx, q, sc, e)
	})
	if err != nil {
		return err
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
// to roles (org slug to role). A role of another source in the same org
// (set in vink, or from OIDC) is never replaced. Unknown org slugs are
// ignored. It writes only when something differs.
func (s *Service) SyncHeaderMemberships(ctx context.Context, userID string, roles map[string]domain.Role) error {
	return s.SyncDerivedMemberships(ctx, userID, roles, "header")
}

// SyncDerivedMemberships is SyncHeaderMemberships for any provider:
// source is header or oidc.
func (s *Service) SyncDerivedMemberships(ctx context.Context, userID string, roles map[string]domain.Role, source string) error {
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
	// an org where the user holds a role of another source (set in vink,
	// or from the other provider) stays as it is: groups never override it
	for _, m := range current {
		if m.Source != source {
			delete(want, m.OrgID)
		}
	}
	same := true
	seen := 0
	for _, m := range current {
		if m.Source != source {
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
		if err := q.DeleteMembershipsBySource(ctx, db.DeleteMembershipsBySourceParams{UserID: userID, Source: source}); err != nil {
			return err
		}
		for orgID, role := range want {
			if err := q.UpsertDerivedMembership(ctx, db.UpsertDerivedMembershipParams{UserID: userID, OrgID: orgID, Role: string(role), Source: source, CreatedAt: domain.Millis(s.now())}); err != nil {
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
	err = s.db.Tx(ctx, func(q *db.Queries) error {
		u, err := s.createLocalUser(ctx, q, in.Subject, in.Email, in.Name, in.Password, true)
		if err != nil {
			return err
		}
		res.User = u
		return s.record(ctx, q, admin, audit.Entry{Action: "user.create", Target: u.Subject, TargetID: u.ID, Detail: map[string]any{"instance_admin": true}})
	})
	if err != nil {
		return nil, err
	}
	res.Org, err = s.CreateOrg(ctx, admin, in.OrgSlug, in.OrgName)
	if err != nil {
		return nil, err
	}
	if err := s.db.Write().UpsertLocalMembership(ctx, db.UpsertLocalMembershipParams{UserID: res.User.ID, OrgID: res.Org.ID, Role: string(domain.RoleOwner), CreatedAt: domain.Millis(s.now())}); err != nil {
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
