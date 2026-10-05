package cli

import (
	"context"
	"net/url"

	"github.com/w4jnl/vink/internal/adminapi"
	"github.com/w4jnl/vink/internal/domain"
)

// AdminClient runs vink admin through /api/v1/admin with an instance
// admin key.
type AdminClient struct{ C *Client }

var _ adminapi.Backend = AdminClient{}

type page[T any] struct {
	Items []T `json:"items"`
}

func esc(s string) string { return url.PathEscape(s) }

func list[T any](ctx context.Context, c *Client, path string) ([]T, error) {
	var p page[T]
	if err := c.Do(ctx, "GET", path, nil, &p); err != nil {
		return nil, err
	}
	if p.Items == nil {
		p.Items = []T{}
	}
	return p.Items, nil
}

func one[T any](ctx context.Context, c *Client, method, path string, in any) (*T, error) {
	var out T
	if err := c.Do(ctx, method, path, in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListOrgs lists every org.
func (a AdminClient) ListOrgs(ctx context.Context) ([]adminapi.Org, error) {
	return list[adminapi.Org](ctx, a.C, "/admin/orgs")
}

// GetOrg returns one org.
func (a AdminClient) GetOrg(ctx context.Context, slug string) (*adminapi.Org, error) {
	return one[adminapi.Org](ctx, a.C, "GET", "/admin/orgs/"+esc(slug), nil)
}

// CreateOrg makes an org.
func (a AdminClient) CreateOrg(ctx context.Context, in adminapi.OrgCreate) (*adminapi.Org, error) {
	return one[adminapi.Org](ctx, a.C, "POST", "/admin/orgs", in)
}

// UpdateOrg renames an org or sets its quotas.
func (a AdminClient) UpdateOrg(ctx context.Context, slug string, in adminapi.OrgPatch) (*adminapi.Org, error) {
	return one[adminapi.Org](ctx, a.C, "PATCH", "/admin/orgs/"+esc(slug), in)
}

// DeleteOrg deletes an org without projects.
func (a AdminClient) DeleteOrg(ctx context.Context, slug string) error {
	return a.C.Do(ctx, "DELETE", "/admin/orgs/"+esc(slug), nil, nil)
}

// ListOrgKeys lists an org's org keys.
func (a AdminClient) ListOrgKeys(ctx context.Context, org string) ([]adminapi.OrgKey, error) {
	return list[adminapi.OrgKey](ctx, a.C, "/admin/orgs/"+esc(org)+"/keys")
}

// CreateOrgKey issues an org key.
func (a AdminClient) CreateOrgKey(ctx context.Context, org string, in adminapi.KeyCreate) (*adminapi.OrgKey, error) {
	return one[adminapi.OrgKey](ctx, a.C, "POST", "/admin/orgs/"+esc(org)+"/keys", in)
}

// RevokeOrgKey revokes an org key.
func (a AdminClient) RevokeOrgKey(ctx context.Context, org, id string) error {
	return a.C.Do(ctx, "DELETE", "/admin/orgs/"+esc(org)+"/keys/"+esc(id), nil, nil)
}

// ListAgents lists an org's agents.
func (a AdminClient) ListAgents(ctx context.Context, org string) ([]adminapi.Agent, error) {
	return list[adminapi.Agent](ctx, a.C, "/admin/orgs/"+esc(org)+"/agents")
}

// CreateAgent registers an agent.
func (a AdminClient) CreateAgent(ctx context.Context, org string, in adminapi.AgentCreate) (*adminapi.Agent, error) {
	return one[adminapi.Agent](ctx, a.C, "POST", "/admin/orgs/"+esc(org)+"/agents", in)
}

// RevokeAgent revokes an agent.
func (a AdminClient) RevokeAgent(ctx context.Context, org, name string) error {
	return a.C.Do(ctx, "DELETE", "/admin/orgs/"+esc(org)+"/agents/"+esc(name), nil, nil)
}

// ListUsers lists every account.
func (a AdminClient) ListUsers(ctx context.Context) ([]adminapi.User, error) {
	return list[adminapi.User](ctx, a.C, "/admin/users")
}

// GetUser returns one account.
func (a AdminClient) GetUser(ctx context.Context, subject string) (*adminapi.User, error) {
	return one[adminapi.User](ctx, a.C, "GET", "/admin/users/"+esc(subject), nil)
}

// CreateUser makes an account.
func (a AdminClient) CreateUser(ctx context.Context, in adminapi.UserCreate) (*adminapi.User, error) {
	return one[adminapi.User](ctx, a.C, "POST", "/admin/users", in)
}

// UpdateUser sets instance admin or disabled.
func (a AdminClient) UpdateUser(ctx context.Context, subject string, in adminapi.UserPatch) (*adminapi.User, error) {
	return one[adminapi.User](ctx, a.C, "PATCH", "/admin/users/"+esc(subject), in)
}

// ResetTOTP turns two-factor off.
func (a AdminClient) ResetTOTP(ctx context.Context, subject string) error {
	return a.C.Do(ctx, "POST", "/admin/users/"+esc(subject)+"/totp-reset", nil, nil)
}

// CreateResetLink makes a password reset link.
func (a AdminClient) CreateResetLink(ctx context.Context, subject string) (*adminapi.ResetLink, error) {
	return one[adminapi.ResetLink](ctx, a.C, "POST", "/admin/users/"+esc(subject)+"/reset-link", nil)
}

// Grant gives a role in an org.
func (a AdminClient) Grant(ctx context.Context, subject, org string, role domain.Role) (*adminapi.User, error) {
	return one[adminapi.User](ctx, a.C, "PUT", "/admin/users/"+esc(subject)+"/orgs/"+esc(org), adminapi.RoleSet{Role: role})
}

// Ungrant removes a role.
func (a AdminClient) Ungrant(ctx context.Context, subject, org string) error {
	return a.C.Do(ctx, "DELETE", "/admin/users/"+esc(subject)+"/orgs/"+esc(org), nil, nil)
}

// ListAdminKeys lists admin keys.
func (a AdminClient) ListAdminKeys(ctx context.Context) ([]adminapi.AdminKey, error) {
	return list[adminapi.AdminKey](ctx, a.C, "/admin/keys")
}

// RevokeAdminKey revokes an admin key.
func (a AdminClient) RevokeAdminKey(ctx context.Context, id string) error {
	return a.C.Do(ctx, "DELETE", "/admin/keys/"+esc(id), nil, nil)
}
