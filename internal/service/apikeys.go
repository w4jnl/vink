package service

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/w4jnl/vink/internal/db"
	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/secrets"
)

func apiKeyFromRow(r db.ApiKey) *domain.APIKey {
	return &domain.APIKey{
		ID: r.ID, ProjectID: strp(r.ProjectID), OrgID: r.OrgID, Name: r.Name, Prefix: r.Prefix, Access: domain.Access(r.Access), CreatedBy: strp(r.CreatedBy),
		CreatedAt: domain.FromMillis(r.CreatedAt), LastUsedAt: domain.FromMillisPtr(r.LastUsedAt), RevokedAt: domain.FromMillisPtr(r.RevokedAt),
	}
}

// CreateAPIKey issues a key for the scope's project. Members may create
// ro keys; rw keys need a project admin. The plaintext is returned once.
func (s *Service) CreateAPIKey(ctx context.Context, sc domain.Scope, name string, access domain.Access) (*domain.APIKey, string, error) {
	if err := requireProject(sc); err != nil {
		return nil, "", err
	}
	if !access.Valid() {
		return nil, "", (&domain.ValidationError{Errors: []domain.FieldError{{Field: "access", Msg: "must be ro or rw"}}}).OrNil()
	}
	if (access == domain.AccessRO && !sc.CanSeePingKey()) || (access == domain.AccessRW && !sc.CanAdminProject()) {
		return nil, "", domain.ErrForbidden
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "key"
	}
	token, prefix, err := secrets.NewAPIKey()
	if err != nil {
		return nil, "", err
	}
	var createdBy *string
	if sc.UserID != "" {
		createdBy = &sc.UserID
	}
	var out *domain.APIKey
	err = s.db.Tx(ctx, func(q *db.Queries) error {
		row, err := q.CreateAPIKey(ctx, db.CreateAPIKeyParams{
			ID: domain.NewID(), ProjectID: ptrs(sc.ProjectID), OrgID: sc.OrgID, Name: name, Prefix: prefix, Hash: secrets.HashToken(token),
			Access: string(access), CreatedBy: createdBy, CreatedAt: domain.Millis(s.now()),
		})
		if err != nil {
			return err
		}
		out = apiKeyFromRow(row)
		e := projectEntry(sc, "key.create", out.Name, out.ID)
		e.Detail = map[string]any{"prefix": out.Prefix, "access": string(out.Access)}
		return s.record(ctx, q, sc, e)
	})
	if err != nil {
		return nil, "", err
	}
	s.log.Info("api key created", "project_id", sc.ProjectID, "prefix", prefix, "access", access, "actor", sc.Actor)
	return out, token, nil
}

// ListAPIKeys lists live keys of the scope's project.
func (s *Service) ListAPIKeys(ctx context.Context, sc domain.Scope) ([]*domain.APIKey, error) {
	if err := requireProject(sc); err != nil {
		return nil, err
	}
	if !sc.CanSeePingKey() {
		return nil, domain.ErrForbidden
	}
	rows, err := s.db.Read().ListAPIKeys(ctx, ptrs(sc.ProjectID))
	if err != nil {
		return nil, err
	}
	out := make([]*domain.APIKey, 0, len(rows))
	for _, r := range rows {
		out = append(out, apiKeyFromRow(r))
	}
	return out, nil
}

// RevokeAPIKey revokes a key. Revoking an rw key needs a project admin.
func (s *Service) RevokeAPIKey(ctx context.Context, sc domain.Scope, id string) error {
	if err := requireProject(sc); err != nil {
		return err
	}
	row, err := s.db.Read().GetAPIKey(ctx, db.GetAPIKeyParams{ProjectID: ptrs(sc.ProjectID), ID: id})
	if err != nil {
		return notFoundIfNoRows(err, "api key")
	}
	if (row.Access == string(domain.AccessRW) && !sc.CanAdminProject()) || !sc.CanSeePingKey() {
		return domain.ErrForbidden
	}
	err = s.db.Tx(ctx, func(q *db.Queries) error {
		n, err := q.RevokeAPIKey(ctx, db.RevokeAPIKeyParams{RevokedAt: ptri(domain.Millis(s.now())), ProjectID: ptrs(sc.ProjectID), ID: id})
		if err != nil {
			return err
		}
		if n == 0 {
			return domain.NotFound("api key")
		}
		e := projectEntry(sc, "key.revoke", row.Name, row.ID)
		e.Detail = map[string]any{"prefix": row.Prefix, "access": row.Access}
		return s.record(ctx, q, sc, e)
	})
	if err != nil {
		return err
	}
	s.keyCache.forget(row.Prefix)
	s.log.Info("api key revoked", "project_id", sc.ProjectID, "prefix", row.Prefix, "actor", sc.Actor)
	return nil
}

// VerifyAPIKey resolves a bearer token to its key. Verified tokens are
// cached for five minutes so argon2 runs once per key, not per request.
func (s *Service) VerifyAPIKey(ctx context.Context, token string) (*domain.APIKey, error) {
	prefix, ok := secrets.ParseAPIKeyPrefix(token)
	if !ok {
		return nil, domain.ErrUnauthorized
	}
	fp := secrets.Fingerprint(token)
	if k, ok := s.keyCache.get(fp, s.now()); ok {
		s.touchKey(ctx, k)
		return k, nil
	}
	rows, err := s.db.Read().ListAPIKeysByPrefix(ctx, prefix)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if secrets.VerifyToken(r.Hash, token) {
			k := apiKeyFromRow(r)
			s.keyCache.put(fp, prefix, k, s.now())
			s.touchKey(ctx, k)
			return k, nil
		}
	}
	return nil, domain.ErrUnauthorized
}

// touchKey records last use at most once a minute.
func (s *Service) touchKey(ctx context.Context, k *domain.APIKey) {
	now := s.now()
	if k.LastUsedAt != nil && now.Sub(*k.LastUsedAt) < time.Minute {
		return
	}
	t := now
	k.LastUsedAt = &t
	if err := s.db.Write().TouchAPIKey(ctx, db.TouchAPIKeyParams{LastUsedAt: ptri(domain.Millis(now)), ID: k.ID}); err != nil {
		s.log.Warn("touch api key", "err", err)
	}
}

// keyCache remembers verified tokens by fingerprint.
type keyCache struct {
	mu      sync.Mutex
	entries map[string]keyCacheEntry
	agents  map[string]agentCacheEntry
	admins  map[string]adminCacheEntry
}

type agentCacheEntry struct {
	agent *domain.Agent
	until time.Time
}

type keyCacheEntry struct {
	key    *domain.APIKey
	prefix string
	at     time.Time
}

const keyCacheTTL = 5 * time.Minute

func (c *keyCache) get(fp string, now time.Time) (*domain.APIKey, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[fp]
	if !ok || now.Sub(e.at) > keyCacheTTL {
		delete(c.entries, fp)
		return nil, false
	}
	k := *e.key
	return &k, true
}

func (c *keyCache) put(fp, prefix string, k *domain.APIKey, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]keyCacheEntry{}
	}
	if len(c.entries) > 10000 {
		c.entries = map[string]keyCacheEntry{}
	}
	cp := *k
	c.entries[fp] = keyCacheEntry{key: &cp, prefix: prefix, at: now}
}

func (c *keyCache) forget(prefix string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for fp, e := range c.entries {
		if e.prefix == prefix {
			delete(c.entries, fp)
		}
	}
	for fp, e := range c.admins {
		if e.prefix == prefix {
			delete(c.admins, fp)
		}
	}
}

// CreateOrgAPIKey issues a key bound to the org and no project: it may
// export and apply every project of the org, and nothing else. Org admins
// and owners only; the plaintext is returned once.
func (s *Service) CreateOrgAPIKey(ctx context.Context, sc domain.Scope, name string, access domain.Access) (*domain.APIKey, string, error) {
	if err := requireOrgAdmin(sc); err != nil {
		return nil, "", err
	}
	if !access.Valid() {
		return nil, "", (&domain.ValidationError{Errors: []domain.FieldError{{Field: "access", Msg: "must be ro or rw"}}}).OrNil()
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "org key"
	}
	token, prefix, err := secrets.NewAPIKey()
	if err != nil {
		return nil, "", err
	}
	var createdBy *string
	if sc.UserID != "" {
		createdBy = &sc.UserID
	}
	var out *domain.APIKey
	err = s.db.Tx(ctx, func(q *db.Queries) error {
		row, err := q.CreateAPIKey(ctx, db.CreateAPIKeyParams{
			ID: domain.NewID(), ProjectID: nil, OrgID: sc.OrgID, Name: name, Prefix: prefix, Hash: secrets.HashToken(token),
			Access: string(access), CreatedBy: createdBy, CreatedAt: domain.Millis(s.now()),
		})
		if err != nil {
			return err
		}
		out = apiKeyFromRow(row)
		e := orgEntry(sc.OrgID, "orgkey.create", out.Name, out.ID)
		e.Detail = map[string]any{"prefix": out.Prefix, "access": string(out.Access)}
		return s.record(ctx, q, sc, e)
	})
	if err != nil {
		return nil, "", err
	}
	s.log.Info("org api key created", "org_id", sc.OrgID, "prefix", prefix, "access", access, "actor", sc.Actor)
	return out, token, nil
}

// ListOrgAPIKeys lists the org's live org keys; project keys are not among them.
func (s *Service) ListOrgAPIKeys(ctx context.Context, sc domain.Scope) ([]*domain.APIKey, error) {
	if err := requireOrgAdmin(sc); err != nil {
		return nil, err
	}
	rows, err := s.db.Read().ListOrgAPIKeys(ctx, sc.OrgID)
	if err != nil {
		return nil, err
	}
	out := make([]*domain.APIKey, 0, len(rows))
	for _, r := range rows {
		out = append(out, apiKeyFromRow(r))
	}
	return out, nil
}

// RevokeOrgAPIKey revokes an org key; a project key's id is not found.
func (s *Service) RevokeOrgAPIKey(ctx context.Context, sc domain.Scope, id string) error {
	if err := requireOrgAdmin(sc); err != nil {
		return err
	}
	keys, err := s.db.Read().ListOrgAPIKeys(ctx, sc.OrgID)
	if err != nil {
		return err
	}
	prefix, name := "", ""
	for _, k := range keys {
		if k.ID == id {
			prefix, name = k.Prefix, k.Name
		}
	}
	err = s.db.Tx(ctx, func(q *db.Queries) error {
		n, err := q.RevokeOrgAPIKey(ctx, db.RevokeOrgAPIKeyParams{RevokedAt: ptri(domain.Millis(s.now())), OrgID: sc.OrgID, ID: id})
		if err != nil {
			return err
		}
		if n == 0 {
			return domain.NotFound("api key")
		}
		e := orgEntry(sc.OrgID, "orgkey.revoke", name, id)
		e.Detail = map[string]any{"prefix": prefix}
		return s.record(ctx, q, sc, e)
	})
	if err != nil {
		return err
	}
	s.keyCache.forget(prefix)
	s.log.Info("org api key revoked", "org_id", sc.OrgID, "prefix", prefix, "actor", sc.Actor)
	return nil
}
