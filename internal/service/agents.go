package service

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/w4jnl/vink/internal/db"
	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/secrets"
)

func agentFromRow(r db.Agent) *domain.Agent {
	a := &domain.Agent{ID: r.ID, OrgID: r.OrgID, Name: r.Name, Labels: domain.ParseLabelsJSON(r.Labels), TokenPrefix: r.TokenPrefix, CreatedAt: domain.FromMillis(r.CreatedAt), LastSeenAt: domain.FromMillisPtr(r.LastSeenAt)}
	if r.Version != nil {
		a.Version = *r.Version
	}
	if r.LastAddr != nil {
		a.LastAddr = *r.LastAddr
	}
	return a
}

// requireOrgAdmin: org admins and owners, or an instance admin, on an org scope.
func requireOrgAdmin(sc domain.Scope) error {
	if sc.OrgID == "" {
		return domain.ErrForbidden
	}
	if sc.InstanceAdmin || sc.CanAdminOrg() {
		return nil
	}
	return domain.ErrForbidden
}

// CreateAgent registers an agent in the scope's org and returns its token
// once. The org's agent quota applies.
func (s *Service) CreateAgent(ctx context.Context, sc domain.Scope, name string, labels map[string]string) (*domain.Agent, string, error) {
	if err := requireOrgAdmin(sc); err != nil {
		return nil, "", err
	}
	if sc.IsKey() && !sc.InstanceKey {
		return nil, "", domain.ErrForbidden
	}
	a := &domain.Agent{Name: name, Labels: labels}
	if a.Labels == nil {
		a.Labels = map[string]string{}
	}
	a.Normalize()
	if err := a.Validate(); err != nil {
		return nil, "", err
	}
	org, err := s.db.Read().GetOrg(ctx, sc.OrgID)
	if err != nil {
		return nil, "", notFoundIfNoRows(err, "org")
	}
	if org.QuotaAgents != nil {
		n, err := s.db.Read().CountAgents(ctx, sc.OrgID)
		if err != nil {
			return nil, "", err
		}
		if n >= *org.QuotaAgents {
			return nil, "", (&domain.ValidationError{Errors: []domain.FieldError{{Field: "quota", Msg: "this org can have " + strconv.FormatInt(*org.QuotaAgents, 10) + " agents; ask the instance admin for more"}}}).OrNil()
		}
	}
	token, prefix, err := secrets.NewAgentToken()
	if err != nil {
		return nil, "", err
	}
	var out *domain.Agent
	err = s.db.Tx(ctx, func(q *db.Queries) error {
		row, err := q.CreateAgent(ctx, db.CreateAgentParams{
			ID: domain.NewID(), OrgID: sc.OrgID, Name: a.Name, TokenHash: secrets.HashToken(token), TokenPrefix: prefix, Labels: domain.LabelsJSON(a.Labels), Version: nil, CreatedAt: domain.Millis(s.now()),
		})
		if err != nil {
			return conflictIfUnique(err, "an agent named "+a.Name+" exists")
		}
		out = agentFromRow(row)
		e := orgEntry(sc.OrgID, "agent.create", out.Name, out.ID)
		e.After = agentSnapshot(out)
		return s.record(ctx, q, sc, e)
	})
	if err != nil {
		return nil, "", err
	}
	s.log.Info("agent created", "org_id", sc.OrgID, "agent", a.Name, "actor", sc.Actor)
	return out, token, nil
}

// ListAgents lists the org's agents by name.
func (s *Service) ListAgents(ctx context.Context, sc domain.Scope) ([]*domain.Agent, error) {
	if err := requireOrgAdmin(sc); err != nil {
		return nil, err
	}
	rows, err := s.db.Read().ListAgents(ctx, sc.OrgID)
	if err != nil {
		return nil, err
	}
	out := make([]*domain.Agent, 0, len(rows))
	for _, r := range rows {
		out = append(out, agentFromRow(r))
	}
	return out, nil
}

// Agent returns one agent of the org by name.
func (s *Service) Agent(ctx context.Context, sc domain.Scope, name string) (*domain.Agent, error) {
	if err := requireOrgAdmin(sc); err != nil {
		return nil, err
	}
	row, err := s.db.Read().GetAgentByName(ctx, db.GetAgentByNameParams{OrgID: sc.OrgID, Name: strings.ToLower(strings.TrimSpace(name))})
	if err != nil {
		return nil, notFoundIfNoRows(err, "agent")
	}
	return agentFromRow(row), nil
}

// UpdateAgentLabels replaces an agent's labels.
func (s *Service) UpdateAgentLabels(ctx context.Context, sc domain.Scope, name string, labels map[string]string) (*domain.Agent, error) {
	cur, err := s.Agent(ctx, sc, name)
	if err != nil {
		return nil, err
	}
	next := *cur
	next.Labels = labels
	if next.Labels == nil {
		next.Labels = map[string]string{}
	}
	next.Normalize()
	if err := next.Validate(); err != nil {
		return nil, err
	}
	var out *domain.Agent
	err = s.db.Tx(ctx, func(q *db.Queries) error {
		row, err := q.UpdateAgentLabels(ctx, db.UpdateAgentLabelsParams{Labels: domain.LabelsJSON(next.Labels), OrgID: sc.OrgID, ID: cur.ID})
		if err != nil {
			return err
		}
		out = agentFromRow(row)
		e := orgEntry(sc.OrgID, "agent.labels", out.Name, out.ID)
		e.Before, e.After = agentSnapshot(cur), agentSnapshot(out)
		return s.record(ctx, q, sc, e)
	})
	if err != nil {
		return nil, err
	}
	s.log.Info("agent labels updated", "org_id", sc.OrgID, "agent", cur.Name, "actor", sc.Actor)
	s.bus.Publish(engineChanged(""))
	return out, nil
}

// RevokeAgent deletes an agent; its token stops working at once and the
// gateway drops the connection.
func (s *Service) RevokeAgent(ctx context.Context, sc domain.Scope, name string) error {
	cur, err := s.Agent(ctx, sc, name)
	if err != nil {
		return err
	}
	err = s.db.Tx(ctx, func(q *db.Queries) error {
		n, err := q.DeleteAgent(ctx, db.DeleteAgentParams{OrgID: sc.OrgID, ID: cur.ID})
		if err != nil {
			return err
		}
		if n == 0 {
			return domain.NotFound("agent")
		}
		if _, err := q.ClearAgentMonitors(ctx, ptrs(cur.ID)); err != nil {
			return err
		}
		e := orgEntry(sc.OrgID, "agent.revoke", cur.Name, cur.ID)
		e.Before = agentSnapshot(cur)
		return s.record(ctx, q, sc, e)
	})
	if err != nil {
		return err
	}
	s.agentCache.drop(cur.ID)
	s.log.Info("agent revoked", "org_id", sc.OrgID, "agent", cur.Name, "actor", sc.Actor)
	s.bus.Publish(engineChanged(""))
	return nil
}

// VerifyAgentToken resolves a bearer token to its agent.
func (s *Service) VerifyAgentToken(ctx context.Context, token string) (*domain.Agent, error) {
	prefix, ok := secrets.ParseAgentTokenPrefix(token)
	if !ok {
		return nil, domain.ErrUnauthorized
	}
	fp := secrets.Fingerprint(token)
	if a, ok := s.agentCache.get(fp, s.now()); ok {
		return a, nil
	}
	rows, err := s.db.Read().ListAgentsByPrefix(ctx, prefix)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if secrets.VerifyToken(r.TokenHash, token) {
			a := agentFromRow(r)
			s.agentCache.put(fp, a, s.now())
			return a, nil
		}
	}
	return nil, domain.ErrUnauthorized
}

// TouchAgent records a connection or heartbeat.
func (s *Service) TouchAgent(ctx context.Context, id, addr, version string) error {
	now := s.now()
	return s.db.Write().TouchAgent(ctx, db.TouchAgentParams{LastSeenAt: ptri(domain.Millis(now)), LastAddr: ptrs(addr), Version: ptrs(version), ID: id})
}

// AgentByID loads an agent for the gateway, with its org.
func (s *Service) AgentByID(ctx context.Context, orgID, id string) (*domain.Agent, error) {
	row, err := s.db.Read().GetAgent(ctx, db.GetAgentParams{OrgID: orgID, ID: id})
	if err != nil {
		return nil, notFoundIfNoRows(err, "agent")
	}
	return agentFromRow(row), nil
}

// agentCache remembers verified tokens for five minutes, like API keys.
type agentCache struct {
	keyCache
}

func (c *agentCache) get(fp string, now time.Time) (*domain.Agent, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.agents[fp]
	if !ok || now.After(e.until) {
		return nil, false
	}
	return e.agent, true
}

func (c *agentCache) put(fp string, a *domain.Agent, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.agents == nil {
		c.agents = map[string]agentCacheEntry{}
	}
	c.agents[fp] = agentCacheEntry{agent: a, until: now.Add(5 * time.Minute)}
}

func (c *agentCache) drop(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for fp, e := range c.agents {
		if e.agent.ID == id {
			delete(c.agents, fp)
		}
	}
}

// SetAgentPresence installs the gateway's view of live connections.
func (s *Service) SetAgentPresence(fn func(agentID string) bool) { s.agentPresence = fn }

// AgentConnected reports whether the gateway holds the agent's socket.
func (s *Service) AgentConnected(id string) bool {
	return s.agentPresence != nil && s.agentPresence(id)
}

// AdoptAgentLabels stores the labels an agent announced on first contact,
// when the admin has set none yet.
func (s *Service) AdoptAgentLabels(ctx context.Context, orgID, agentID string, labels map[string]string) (*domain.Agent, error) {
	probe := &domain.Agent{Name: "probe", Labels: labels}
	probe.Normalize()
	if err := probe.Validate(); err != nil {
		return nil, err
	}
	row, err := s.db.Write().UpdateAgentLabels(ctx, db.UpdateAgentLabelsParams{Labels: domain.LabelsJSON(probe.Labels), OrgID: orgID, ID: agentID})
	if err != nil {
		return nil, notFoundIfNoRows(err, "agent")
	}
	s.agentCache.drop(agentID)
	s.log.Info("agent labels adopted", "org_id", orgID, "agent", row.Name, "labels", domain.LabelsString(probe.Labels))
	return agentFromRow(row), nil
}

// AgentChoices lists the org's agents for a project member picking where
// a check runs: names and states, nothing secret.
func (s *Service) AgentChoices(ctx context.Context, sc domain.Scope) ([]*domain.Agent, error) {
	if err := requireProject(sc); err != nil {
		return nil, err
	}
	rows, err := s.db.Read().ListAgents(ctx, sc.OrgID)
	if err != nil {
		return nil, err
	}
	out := make([]*domain.Agent, 0, len(rows))
	for _, r := range rows {
		out = append(out, agentFromRow(r))
	}
	return out, nil
}

// AgentMonitorCounts says how many monitors each agent of the org holds.
func (s *Service) AgentMonitorCounts(ctx context.Context, sc domain.Scope) (map[string]int, error) {
	if err := requireOrgAdmin(sc); err != nil {
		return nil, err
	}
	rows, err := s.db.Read().ListRemoteMonitors(ctx, sc.OrgID)
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	for _, r := range rows {
		if r.AgentID != nil && *r.AgentID != "" {
			counts[*r.AgentID]++
		}
	}
	return counts, nil
}

// SetAgentSince installs the gateway's record of when each agent connected.
func (s *Service) SetAgentSince(fn func(agentID string) (time.Time, bool)) { s.agentSince = fn }

// AgentConnectedSince is when the agent's current socket opened.
func (s *Service) AgentConnectedSince(id string) (time.Time, bool) {
	if s.agentSince == nil {
		return time.Time{}, false
	}
	return s.agentSince(id)
}
