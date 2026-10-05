package adminapi

import (
	"context"
	"time"

	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/service"
)

// User is an account with its roles.
type User struct {
	ID      string `json:"id"`
	Subject string `json:"subject"`
	Email   string `json:"email,omitempty"`
	Name    string `json:"name,omitempty"`
	// Source is local (a password), proxy or oidc.
	Source        string     `json:"source"`
	InstanceAdmin bool       `json:"instance_admin"`
	Disabled      bool       `json:"disabled"`
	DisabledAt    *time.Time `json:"disabled_at,omitempty"`
	DisabledBy    string     `json:"disabled_by,omitempty"`
	TwoFactor     bool       `json:"two_factor"`
	CreatedAt     time.Time  `json:"created_at"`
	LastSeenAt    *time.Time `json:"last_seen_at"`
	Roles         []Role     `json:"roles"`
}

// Role is a user's role in an org, and where it comes from: local (set
// in vink), header (the proxy's groups) or oidc (the IdP's groups).
type Role struct {
	Org    string      `json:"org"`
	Role   domain.Role `json:"role"`
	Source string      `json:"source"`
}

func userFrom(u service.InstanceUser) User {
	roles := make([]Role, 0, len(u.Memberships))
	for _, m := range u.Memberships {
		roles = append(roles, Role{Org: m.OrgSlug, Role: m.Role, Source: m.Source})
	}
	return User{
		ID: u.ID, Subject: u.Subject, Email: u.Email, Name: u.DisplayName, Source: u.Source, InstanceAdmin: u.InstanceAdmin,
		Disabled: u.Disabled(), DisabledAt: u.DisabledAt, DisabledBy: u.DisabledBy, TwoFactor: u.TOTPOn(),
		CreatedAt: u.CreatedAt, LastSeenAt: u.LastSeenAt, Roles: roles,
	}
}

// UserCreate makes an account. A local one needs a password; a proxy or
// oidc one is made ahead of the person's first visit, so roles can be
// given before, and its name is normalised as that provider's settings
// normalise it.
type UserCreate struct {
	Subject       string `json:"subject"`
	Source        string `json:"source,omitempty"`
	Email         string `json:"email,omitempty"`
	Name          string `json:"name,omitempty"`
	Password      string `json:"password,omitempty"`
	InstanceAdmin bool   `json:"instance_admin,omitempty"`
}

// UserPatch changes what it names.
type UserPatch struct {
	InstanceAdmin *bool `json:"instance_admin,omitempty"`
	Disabled      *bool `json:"disabled,omitempty"`
}

// RoleSet is the body of a grant.
type RoleSet struct {
	Role domain.Role `json:"role"`
}

// ResetLink is a one-time password reset link.
type ResetLink struct {
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}

// ListUsers lists every account with its roles.
func (d Direct) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := d.Svc.ListInstanceUsers(ctx, d.Scope)
	if err != nil {
		return nil, err
	}
	out := make([]User, 0, len(rows))
	for _, r := range rows {
		out = append(out, userFrom(r))
	}
	return out, nil
}

// GetUser returns one account by sign-in name.
func (d Direct) GetUser(ctx context.Context, subject string) (*User, error) {
	rows, err := d.Svc.ListInstanceUsers(ctx, d.Scope)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if r.Subject == subject {
			u := userFrom(r)
			return &u, nil
		}
	}
	return nil, domain.NotFound("user")
}

// CreateUser makes an account.
func (d Direct) CreateUser(ctx context.Context, in UserCreate) (*User, error) {
	var (
		u   *domain.User
		err error
	)
	switch in.Source {
	case "", "local":
		u, err = d.Svc.CreateLocalUser(ctx, d.Scope, in.Subject, in.Email, in.Name, in.Password, in.InstanceAdmin)
	case "proxy", "oidc":
		if in.Password != "" {
			return nil, validation("password", "only local accounts have a password")
		}
		if in.InstanceAdmin && d.Svc.AuthPolicy(ctx).For(in.Source).GroupsDecide() {
			setting := service.SettingFor(in.Source)
			return nil, validation("instance_admin", "instance admin comes from "+setting+".instance_admin_group while "+setting+".roles is groups")
		}
		if u, err = d.Svc.CreateProviderUser(ctx, d.Scope, in.Subject, in.Email, in.Name, in.Source); err == nil && in.InstanceAdmin {
			err = d.Svc.SetInstanceAdmin(ctx, d.Scope, u.Subject, true)
		}
	default:
		return nil, validation("source", "must be local, proxy or oidc")
	}
	if err != nil {
		return nil, err
	}
	return d.GetUser(ctx, u.Subject)
}

// UpdateUser makes an account instance admin or not, and disables or
// enables it.
func (d Direct) UpdateUser(ctx context.Context, subject string, in UserPatch) (*User, error) {
	if err := requireAdmin(d.Scope); err != nil {
		return nil, err
	}
	u, err := d.Svc.UserBySubject(ctx, subject)
	if err != nil {
		return nil, err
	}
	if in.InstanceAdmin != nil {
		if err := d.Svc.SetInstanceAdmin(ctx, d.Scope, u.Subject, *in.InstanceAdmin); err != nil {
			return nil, err
		}
	}
	if in.Disabled != nil {
		if err := d.Svc.SetUserDisabled(ctx, d.Scope, u.ID, *in.Disabled); err != nil {
			return nil, err
		}
	}
	return d.GetUser(ctx, u.Subject)
}

// ResetTOTP turns two-factor off for an account.
func (d Direct) ResetTOTP(ctx context.Context, subject string) error {
	if err := requireAdmin(d.Scope); err != nil {
		return err
	}
	u, err := d.Svc.UserBySubject(ctx, subject)
	if err != nil {
		return err
	}
	return d.Svc.ResetTOTP(ctx, d.Scope, u.ID)
}

// CreateResetLink makes a one-time password reset link for a local
// account, valid for a day.
func (d Direct) CreateResetLink(ctx context.Context, subject string) (*ResetLink, error) {
	if err := requireAdmin(d.Scope); err != nil {
		return nil, err
	}
	u, err := d.Svc.UserBySubject(ctx, subject)
	if err != nil {
		return nil, err
	}
	token, expires, err := d.Svc.CreateResetLink(ctx, d.Scope, u.ID)
	if err != nil {
		return nil, err
	}
	return &ResetLink{URL: d.Svc.Config().BaseURL + "/reset/" + token, ExpiresAt: expires}, nil
}

// Grant gives an account a role in an org, or changes it.
func (d Direct) Grant(ctx context.Context, subject, org string, role domain.Role) (*User, error) {
	_, o, err := d.orgScope(ctx, org)
	if err != nil {
		return nil, err
	}
	u, err := d.Svc.UserBySubject(ctx, subject)
	if err != nil {
		return nil, err
	}
	if err := d.Svc.SetMembership(ctx, d.Scope, u.ID, o.ID, role); err != nil {
		return nil, err
	}
	return d.GetUser(ctx, u.Subject)
}

// Ungrant removes an account from an org; the last owner stays.
func (d Direct) Ungrant(ctx context.Context, subject, org string) error {
	_, o, err := d.orgScope(ctx, org)
	if err != nil {
		return err
	}
	u, err := d.Svc.UserBySubject(ctx, subject)
	if err != nil {
		return err
	}
	return d.Svc.RemoveMembership(ctx, d.Scope, u.ID, o.ID)
}

// requireAdmin refuses a caller that is not an instance admin before any
// lookup, so a name's existence never leaks.
func requireAdmin(sc domain.Scope) error {
	if !sc.InstanceAdmin {
		return domain.ErrForbidden
	}
	return nil
}

func validation(field, msg string) error {
	return (&domain.ValidationError{Errors: []domain.FieldError{{Field: field, Msg: msg}}}).OrNil()
}
