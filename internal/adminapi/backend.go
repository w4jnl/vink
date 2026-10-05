package adminapi

import (
	"context"
	"time"

	"github.com/w4jnl/vink/internal/domain"
)

// Backend is what vink admin runs against: the database on the server
// host (Direct) or /api/v1/admin through a context (cli.AdminClient).
// Both return the same shapes, so the output is the same either way.
type Backend interface {
	ListOrgs(ctx context.Context) ([]Org, error)
	GetOrg(ctx context.Context, slug string) (*Org, error)
	CreateOrg(ctx context.Context, in OrgCreate) (*Org, error)
	UpdateOrg(ctx context.Context, slug string, in OrgPatch) (*Org, error)
	DeleteOrg(ctx context.Context, slug string) error
	ListOrgKeys(ctx context.Context, org string) ([]OrgKey, error)
	CreateOrgKey(ctx context.Context, org string, in KeyCreate) (*OrgKey, error)
	RevokeOrgKey(ctx context.Context, org, id string) error
	ListAgents(ctx context.Context, org string) ([]Agent, error)
	CreateAgent(ctx context.Context, org string, in AgentCreate) (*Agent, error)
	RevokeAgent(ctx context.Context, org, name string) error
	ListUsers(ctx context.Context) ([]User, error)
	GetUser(ctx context.Context, subject string) (*User, error)
	CreateUser(ctx context.Context, in UserCreate) (*User, error)
	UpdateUser(ctx context.Context, subject string, in UserPatch) (*User, error)
	ResetTOTP(ctx context.Context, subject string) error
	CreateResetLink(ctx context.Context, subject string) (*ResetLink, error)
	Grant(ctx context.Context, subject, org string, role domain.Role) (*User, error)
	Ungrant(ctx context.Context, subject, org string) error
	ListAdminKeys(ctx context.Context) ([]AdminKey, error)
	RevokeAdminKey(ctx context.Context, id string) error
}

var _ Backend = Direct{}

// AdminKeyCreate names a new admin key, its access and lifetime (90 days
// when zero).
type AdminKeyCreate struct {
	Name   string
	Access domain.Access
	TTL    time.Duration
}

// CreateAdminKey issues an admin key; Key holds the plaintext, once. Only
// Direct has it: the API never makes admin keys.
func (d Direct) CreateAdminKey(ctx context.Context, in AdminKeyCreate) (*AdminKey, error) {
	k, token, err := d.Svc.CreateAdminKey(ctx, d.Scope, in.Name, in.Access, in.TTL)
	if err != nil {
		return nil, err
	}
	out := adminKey(k, map[string]string{}, d.Svc.Now())
	out.Key = token
	return &out, nil
}
