// Package adminapi is the instance administration that vink admin and
// /api/v1/admin share: the shapes both print and send (snake_case JSON),
// and Direct, which runs each action on the service with the caller's
// scope. The API handlers call Direct with the request's scope; vink admin
// on the server host calls it with the host's.
package adminapi

import (
	"context"
	"time"

	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/service"
)

// Direct runs admin actions on a service as Scope, an instance admin.
type Direct struct {
	Svc   *service.Service
	Scope domain.Scope
}

// AdminKey is an instance admin API key.
type AdminKey struct {
	ID     string        `json:"id"`
	Name   string        `json:"name"`
	Prefix string        `json:"prefix"`
	Access domain.Access `json:"access"`
	// CreatedBy is the creator's sign-in name; empty when vink admin on
	// the server host made it, or the creator is gone.
	CreatedBy  string     `json:"created_by,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	Expired    bool       `json:"expired"`
	LastUsedAt *time.Time `json:"last_used_at"`
	LastUsedIP string     `json:"last_used_ip,omitempty"`
	// Key is the plaintext, only when the key is created.
	Key string `json:"key,omitempty"`
}

// adminKey shapes k; subjects maps user ids to sign-in names.
func adminKey(k *domain.AdminKey, subjects map[string]string, now time.Time) AdminKey {
	return AdminKey{
		ID: k.ID, Name: k.Name, Prefix: k.Prefix, Access: k.Access, CreatedBy: subjects[k.CreatedBy],
		CreatedAt: k.CreatedAt, ExpiresAt: k.ExpiresAt, Expired: k.Expired(now), LastUsedAt: k.LastUsedAt, LastUsedIP: k.LastUsedIP,
	}
}

// ListAdminKeys lists the admin keys not revoked, expired ones included.
func (d Direct) ListAdminKeys(ctx context.Context) ([]AdminKey, error) {
	keys, err := d.Svc.ListAdminKeys(ctx, d.Scope)
	if err != nil {
		return nil, err
	}
	subjects := map[string]string{}
	for _, k := range keys {
		if _, done := subjects[k.CreatedBy]; done || k.CreatedBy == "" {
			continue
		}
		subjects[k.CreatedBy] = ""
		if u, err := d.Svc.UserByID(ctx, k.CreatedBy); err == nil {
			subjects[k.CreatedBy] = u.Subject
		}
	}
	now := d.Svc.Now()
	out := make([]AdminKey, 0, len(keys))
	for _, k := range keys {
		out = append(out, adminKey(k, subjects, now))
	}
	return out, nil
}

// RevokeAdminKey revokes an admin key by id.
func (d Direct) RevokeAdminKey(ctx context.Context, id string) error {
	return d.Svc.RevokeAdminKey(ctx, d.Scope, id)
}
