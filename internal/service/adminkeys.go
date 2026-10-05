package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/w4jnl/vink/internal/audit"
	"github.com/w4jnl/vink/internal/db"
	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/secrets"
)

// Admin key lifetimes: every key expires, after a day at the least and a
// year at the most.
const (
	AdminKeyDefaultTTL = 90 * 24 * time.Hour
	AdminKeyMinTTL     = 24 * time.Hour
	AdminKeyMaxTTL     = 365 * 24 * time.Hour
)

// KeyExpiredError is the answer to an admin key whose time is up; it is
// an ErrUnauthorized that says when, so the CLI can tell the person.
type KeyExpiredError struct{ At time.Time }

func (e *KeyExpiredError) Error() string {
	return "this admin key expired on " + e.At.UTC().Format("2 Jan 2006") + "; create a new one in Instance admin › API keys"
}

// Unwrap makes the error an ErrUnauthorized.
func (e *KeyExpiredError) Unwrap() error { return domain.ErrUnauthorized }

func adminKeyFromRow(r db.AdminKey) *domain.AdminKey {
	return &domain.AdminKey{
		ID: r.ID, Name: r.Name, Prefix: r.Prefix, Access: domain.Access(r.Access), CreatedBy: strp(r.CreatedBy),
		CreatedAt: domain.FromMillis(r.CreatedAt), ExpiresAt: domain.FromMillis(r.ExpiresAt),
		LastUsedAt: domain.FromMillisPtr(r.LastUsedAt), LastUsedIP: strp(r.LastUsedIp), RevokedAt: domain.FromMillisPtr(r.RevokedAt),
	}
}

// AdminKeyScope is what an admin key acts as: an instance admin, owner
// for rw and viewer for ro, on /api/v1/admin only.
func AdminKeyScope(k *domain.AdminKey) domain.Scope {
	role := domain.RoleViewer
	if k.Access == domain.AccessRW {
		role = domain.RoleOwner
	}
	return domain.Scope{
		Role: role, InstanceAdmin: true, InstanceKey: true, Actor: "key:" + k.Prefix,
		KeyID: k.ID, KeyName: k.Name, KeyAccess: k.Access,
	}
}

// CreateAdminKey issues an instance admin key that expires after ttl (90
// days when zero). Only a signed-in instance admin or vink admin on the
// server host may: a key never mints another key. The plaintext is
// returned once.
func (s *Service) CreateAdminKey(ctx context.Context, sc domain.Scope, name string, access domain.Access, ttl time.Duration) (*domain.AdminKey, string, error) {
	if err := requireInstanceAdmin(sc); err != nil {
		return nil, "", err
	}
	if sc.IsKey() {
		return nil, "", fmt.Errorf("%w: admin keys are created in Instance admin › API keys or with vink admin key create on the server host", domain.ErrForbidden)
	}
	if ttl == 0 {
		ttl = AdminKeyDefaultTTL
	}
	ve := &domain.ValidationError{}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "admin key"
	}
	if len(name) > domain.MaxNameLen {
		ve.Addf("name", "at most %d characters", domain.MaxNameLen)
	}
	if !access.Valid() {
		ve.Add("access", "must be ro or rw")
	}
	if ttl < AdminKeyMinTTL || ttl > AdminKeyMaxTTL {
		ve.Add("expires", "must be between 1 and 365 days")
	}
	if err := ve.OrNil(); err != nil {
		return nil, "", err
	}
	token, prefix, err := secrets.NewAdminKey()
	if err != nil {
		return nil, "", err
	}
	now := s.now()
	var out *domain.AdminKey
	err = s.db.Tx(ctx, func(q *db.Queries) error {
		row, err := q.CreateAdminKey(ctx, db.CreateAdminKeyParams{
			ID: domain.NewID(), Name: name, Prefix: prefix, Hash: secrets.HashToken(token), Access: string(access),
			CreatedBy: ptrs(sc.UserID), CreatedAt: domain.Millis(now), ExpiresAt: domain.Millis(now.Add(ttl)),
		})
		if err != nil {
			return err
		}
		out = adminKeyFromRow(row)
		return s.record(ctx, q, sc, audit.Entry{Action: "adminkey.create", Target: out.Name, TargetID: out.ID, Detail: map[string]any{
			"prefix": out.Prefix, "access": string(out.Access), "expires_at": out.ExpiresAt.UTC().Format(time.RFC3339),
		}})
	})
	if err != nil {
		return nil, "", err
	}
	s.log.Info("admin key created", "prefix", prefix, "access", access, "expires_at", out.ExpiresAt, "actor", sc.Actor)
	return out, token, nil
}

// ListAdminKeys lists the admin keys not revoked, newest first; expired
// ones are among them until revoked. Instance admins only.
func (s *Service) ListAdminKeys(ctx context.Context, sc domain.Scope) ([]*domain.AdminKey, error) {
	if err := requireInstanceAdmin(sc); err != nil {
		return nil, err
	}
	rows, err := s.db.Read().ListAdminKeys(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*domain.AdminKey, 0, len(rows))
	for _, r := range rows {
		out = append(out, adminKeyFromRow(r))
	}
	return out, nil
}

// RevokeAdminKey revokes an admin key at once, in every process that
// verifies it. Instance admins, and rw admin keys.
func (s *Service) RevokeAdminKey(ctx context.Context, sc domain.Scope, id string) error {
	if err := requireInstanceAdmin(sc); err != nil {
		return err
	}
	if sc.IsAdminKey() && sc.KeyAccess != domain.AccessRW {
		return domain.ErrForbidden
	}
	row, err := s.db.Read().GetAdminKey(ctx, id)
	if err != nil {
		return notFoundIfNoRows(err, "admin key")
	}
	if row.RevokedAt != nil {
		return domain.NotFound("admin key")
	}
	err = s.db.Tx(ctx, func(q *db.Queries) error {
		n, err := q.RevokeAdminKey(ctx, db.RevokeAdminKeyParams{RevokedAt: ptri(domain.Millis(s.now())), ID: id})
		if err != nil {
			return err
		}
		if n == 0 {
			return domain.NotFound("admin key")
		}
		return s.record(ctx, q, sc, audit.Entry{Action: "adminkey.revoke", Target: row.Name, TargetID: row.ID, Detail: map[string]any{"prefix": row.Prefix, "access": row.Access}})
	})
	if err != nil {
		return err
	}
	s.keyCache.forget(row.Prefix)
	s.log.Info("admin key revoked", "prefix", row.Prefix, "actor", sc.Actor)
	return nil
}

// revokeAdminKeysOf revokes, inside q's transaction, the admin keys a user
// made, when the user stops being an instance admin or is disabled. Each
// revoke is audited with the reason.
func (s *Service) revokeAdminKeysOf(ctx context.Context, q *db.Queries, sc domain.Scope, userID, reason string) error {
	rows, err := q.ListAdminKeysCreatedBy(ctx, &userID)
	if err != nil {
		return err
	}
	at := ptri(domain.Millis(s.now()))
	for _, r := range rows {
		if _, err := q.RevokeAdminKey(ctx, db.RevokeAdminKeyParams{RevokedAt: at, ID: r.ID}); err != nil {
			return err
		}
		if err := s.record(ctx, q, sc, audit.Entry{Action: "adminkey.revoke", Target: r.Name, TargetID: r.ID, Detail: map[string]any{
			"prefix": r.Prefix, "access": r.Access, "reason": reason,
		}}); err != nil {
			return err
		}
		// a rolled-back revoke only costs a cache miss
		s.keyCache.forget(r.Prefix)
		s.log.Info("admin key revoked", "prefix", r.Prefix, "reason", reason)
	}
	return nil
}

// VerifyAdminKey resolves a vka_ bearer token to its key. argon2 runs
// once per key and five minutes; every use still reads the key's state,
// so a revoke from any process, vink admin on the host included, holds
// at once. Use is recorded at most once a minute, with the client IP.
func (s *Service) VerifyAdminKey(ctx context.Context, token, ip string) (*domain.AdminKey, error) {
	prefix, ok := secrets.ParseAdminKeyPrefix(token)
	if !ok {
		return nil, domain.ErrUnauthorized
	}
	now := s.now()
	fp := secrets.Fingerprint(token)
	if k, ok := s.keyCache.getAdmin(fp, now); ok {
		st, err := s.db.Read().GetAdminKeyState(ctx, k.ID)
		if err != nil || st.RevokedAt != nil {
			s.keyCache.forget(prefix)
			if err != nil && !db.IsNotFound(err) {
				return nil, err
			}
			return nil, domain.ErrUnauthorized
		}
		k.ExpiresAt = domain.FromMillis(st.ExpiresAt)
		if k.Expired(now) {
			s.keyCache.forget(prefix)
			return nil, &KeyExpiredError{At: k.ExpiresAt}
		}
		s.touchAdminKey(ctx, fp, k, ip)
		return k, nil
	}
	rows, err := s.db.Read().ListAdminKeysByPrefix(ctx, prefix)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if !secrets.VerifyToken(r.Hash, token) {
			continue
		}
		k := adminKeyFromRow(r)
		if k.Expired(now) {
			return nil, &KeyExpiredError{At: k.ExpiresAt}
		}
		s.keyCache.putAdmin(fp, prefix, k, now)
		s.touchAdminKey(ctx, fp, k, ip)
		return k, nil
	}
	return nil, domain.ErrUnauthorized
}

// touchAdminKey records last use at most once a minute, or at once from a
// new address.
func (s *Service) touchAdminKey(ctx context.Context, fp string, k *domain.AdminKey, ip string) {
	now := s.now()
	if k.LastUsedAt != nil && now.Sub(*k.LastUsedAt) < time.Minute && k.LastUsedIP == ip {
		return
	}
	t := now
	k.LastUsedAt, k.LastUsedIP = &t, ip
	s.keyCache.touchAdmin(fp, t, ip)
	if err := s.db.Write().TouchAdminKey(ctx, db.TouchAdminKeyParams{LastUsedAt: ptri(domain.Millis(now)), LastUsedIp: ptrs(ip), ID: k.ID}); err != nil {
		s.log.Warn("touch admin key", "err", err)
	}
}

type adminCacheEntry struct {
	key    *domain.AdminKey
	prefix string
	at     time.Time
}

func (c *keyCache) getAdmin(fp string, now time.Time) (*domain.AdminKey, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.admins[fp]
	if !ok || now.Sub(e.at) > keyCacheTTL {
		delete(c.admins, fp)
		return nil, false
	}
	k := *e.key
	return &k, true
}

func (c *keyCache) putAdmin(fp, prefix string, k *domain.AdminKey, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.admins == nil || len(c.admins) > 1000 {
		c.admins = map[string]adminCacheEntry{}
	}
	cp := *k
	c.admins[fp] = adminCacheEntry{key: &cp, prefix: prefix, at: now}
}

func (c *keyCache) touchAdmin(fp string, at time.Time, ip string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.admins[fp]; ok {
		k := *e.key
		k.LastUsedAt, k.LastUsedIP = &at, ip
		e.key = &k
		c.admins[fp] = e
	}
}
