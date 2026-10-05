package adminapi

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/service"
)

// Org is an org with what an instance admin wants to see.
type Org struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
	// Quotas are null when unlimited.
	QuotaMonitors *int64    `json:"quota_monitors"`
	QuotaAgents   *int64    `json:"quota_agents"`
	Projects      int       `json:"projects"`
	Monitors      int       `json:"monitors"`
	Agents        int       `json:"agents"`
	Owners        []string  `json:"owners"`
	CreatedAt     time.Time `json:"created_at"`
}

func orgFrom(s service.OrgSummary) Org {
	owners := s.Owners
	if owners == nil {
		owners = []string{}
	}
	return Org{
		ID: s.Org.ID, Slug: s.Org.Slug, Name: s.Org.Name, QuotaMonitors: s.Org.QuotaMonitors, QuotaAgents: s.Org.QuotaAgents,
		Projects: s.Projects, Monitors: s.Monitors, Agents: s.Agents, Owners: owners, CreatedAt: s.Org.CreatedAt,
	}
}

// OrgCreate makes an org; Owner, when set, names an existing user who
// becomes its first owner.
type OrgCreate struct {
	Slug          string `json:"slug"`
	Name          string `json:"name,omitempty"`
	Owner         string `json:"owner,omitempty"`
	QuotaMonitors *int64 `json:"quota_monitors,omitempty"`
	QuotaAgents   *int64 `json:"quota_agents,omitempty"`
}

// OrgPatch changes what it names: a quota set to null is unlimited, a
// field left out stays.
type OrgPatch struct {
	Name          *string  `json:"name,omitempty"`
	QuotaMonitors OptInt64 `json:"quota_monitors,omitzero"`
	QuotaAgents   OptInt64 `json:"quota_agents,omitzero"`
}

// OptInt64 tells a field left out (Set false) from one set to null
// (Set, Value nil) or to a number.
type OptInt64 struct {
	Set   bool
	Value *int64
}

// Int64 is an OptInt64 set to n.
func Int64(n int64) OptInt64 { return OptInt64{Set: true, Value: &n} }

// Null is an OptInt64 set to null.
func Null() OptInt64 { return OptInt64{Set: true} }

// IsZero reports a field left out, for omitzero.
func (o OptInt64) IsZero() bool { return !o.Set }

// MarshalJSON writes the number or null.
func (o OptInt64) MarshalJSON() ([]byte, error) {
	if o.Value == nil {
		return []byte("null"), nil
	}
	return []byte(strconv.FormatInt(*o.Value, 10)), nil
}

// UnmarshalJSON reads a number or null.
func (o *OptInt64) UnmarshalJSON(b []byte) error {
	o.Set = true
	if string(b) == "null" {
		o.Value = nil
		return nil
	}
	var n int64
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	o.Value = &n
	return nil
}

// orgScope is the caller's scope inside the org named by slug.
func (d Direct) orgScope(ctx context.Context, slug string) (domain.Scope, *domain.Org, error) {
	if err := requireAdmin(d.Scope); err != nil {
		return domain.Scope{}, nil, err
	}
	org, err := d.Svc.OrgBySlug(ctx, slug)
	if err != nil {
		return domain.Scope{}, nil, err
	}
	sc := d.Scope
	sc.OrgID = org.ID
	return sc, org, nil
}

// ListOrgs lists every org.
func (d Direct) ListOrgs(ctx context.Context) ([]Org, error) {
	if err := requireAdmin(d.Scope); err != nil {
		return nil, err
	}
	rows, err := d.Svc.OrgSummaries(ctx, d.Scope)
	if err != nil {
		return nil, err
	}
	out := make([]Org, 0, len(rows))
	for _, r := range rows {
		out = append(out, orgFrom(r))
	}
	return out, nil
}

// GetOrg returns one org by slug.
func (d Direct) GetOrg(ctx context.Context, slug string) (*Org, error) {
	if err := requireAdmin(d.Scope); err != nil {
		return nil, err
	}
	rows, err := d.Svc.OrgSummaries(ctx, d.Scope)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if r.Org.Slug == slug {
			o := orgFrom(r)
			return &o, nil
		}
	}
	return nil, domain.NotFound("org")
}

// CreateOrg makes an org, with its first owner and quotas.
func (d Direct) CreateOrg(ctx context.Context, in OrgCreate) (*Org, error) {
	org, err := d.Svc.CreateOrgWithOwner(ctx, d.Scope, in.Slug, in.Name, in.Owner, in.QuotaMonitors, in.QuotaAgents)
	if err != nil {
		return nil, err
	}
	return d.GetOrg(ctx, org.Slug)
}

// UpdateOrg renames an org or sets its quotas.
func (d Direct) UpdateOrg(ctx context.Context, slug string, in OrgPatch) (*Org, error) {
	_, org, err := d.orgScope(ctx, slug)
	if err != nil {
		return nil, err
	}
	name, qm, qa := org.Name, org.QuotaMonitors, org.QuotaAgents
	if in.Name != nil {
		name = *in.Name
	}
	if in.QuotaMonitors.Set {
		qm = in.QuotaMonitors.Value
	}
	if in.QuotaAgents.Set {
		qa = in.QuotaAgents.Value
	}
	if _, err := d.Svc.UpdateOrg(ctx, d.Scope, org.ID, name, qm, qa); err != nil {
		return nil, err
	}
	return d.GetOrg(ctx, slug)
}

// DeleteOrg deletes an org that has no projects left.
func (d Direct) DeleteOrg(ctx context.Context, slug string) error {
	sc, _, err := d.orgScope(ctx, slug)
	if err != nil {
		return err
	}
	return d.Svc.DeleteOrg(ctx, sc)
}

// OrgKey is an org key: it exports and applies every project of the org.
type OrgKey struct {
	ID         string        `json:"id"`
	Name       string        `json:"name"`
	Prefix     string        `json:"prefix"`
	Access     domain.Access `json:"access"`
	CreatedAt  time.Time     `json:"created_at"`
	LastUsedAt *time.Time    `json:"last_used_at"`
	// Key is the plaintext, only when the key is created.
	Key string `json:"key,omitempty"`
}

func orgKeyFrom(k *domain.APIKey) OrgKey {
	return OrgKey{ID: k.ID, Name: k.Name, Prefix: k.Prefix, Access: k.Access, CreatedAt: k.CreatedAt, LastUsedAt: k.LastUsedAt}
}

// KeyCreate names a new key and its access.
type KeyCreate struct {
	Name   string        `json:"name,omitempty"`
	Access domain.Access `json:"access"`
}

// ListOrgKeys lists an org's live org keys.
func (d Direct) ListOrgKeys(ctx context.Context, org string) ([]OrgKey, error) {
	sc, _, err := d.orgScope(ctx, org)
	if err != nil {
		return nil, err
	}
	keys, err := d.Svc.ListOrgAPIKeys(ctx, sc)
	if err != nil {
		return nil, err
	}
	out := make([]OrgKey, 0, len(keys))
	for _, k := range keys {
		out = append(out, orgKeyFrom(k))
	}
	return out, nil
}

// CreateOrgKey issues an org key; Key holds the plaintext, once.
func (d Direct) CreateOrgKey(ctx context.Context, org string, in KeyCreate) (*OrgKey, error) {
	sc, _, err := d.orgScope(ctx, org)
	if err != nil {
		return nil, err
	}
	if in.Access == "" {
		in.Access = domain.AccessRO
	}
	k, token, err := d.Svc.CreateOrgAPIKey(ctx, sc, in.Name, in.Access)
	if err != nil {
		return nil, err
	}
	out := orgKeyFrom(k)
	out.Key = token
	return &out, nil
}

// RevokeOrgKey revokes an org key by id.
func (d Direct) RevokeOrgKey(ctx context.Context, org, id string) error {
	sc, _, err := d.orgScope(ctx, org)
	if err != nil {
		return err
	}
	return d.Svc.RevokeOrgAPIKey(ctx, sc, id)
}

// Agent is an org's probe agent.
type Agent struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Labels      map[string]string `json:"labels"`
	TokenPrefix string            `json:"token_prefix"`
	Version     string            `json:"version,omitempty"`
	LastSeenAt  *time.Time        `json:"last_seen_at"`
	LastAddr    string            `json:"last_addr,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	// Token and Command, the line to run on the agent's host, only when
	// the agent is created.
	Token   string `json:"token,omitempty"`
	Command string `json:"command,omitempty"`
}

func agentFrom(a *domain.Agent) Agent {
	labels := a.Labels
	if labels == nil {
		labels = map[string]string{}
	}
	return Agent{ID: a.ID, Name: a.Name, Labels: labels, TokenPrefix: a.TokenPrefix, Version: a.Version, LastSeenAt: a.LastSeenAt, LastAddr: a.LastAddr, CreatedAt: a.CreatedAt}
}

// AgentCreate registers an agent with labels monitors can select.
type AgentCreate struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels,omitempty"`
}

// ListAgents lists an org's agents by name.
func (d Direct) ListAgents(ctx context.Context, org string) ([]Agent, error) {
	sc, _, err := d.orgScope(ctx, org)
	if err != nil {
		return nil, err
	}
	agents, err := d.Svc.ListAgents(ctx, sc)
	if err != nil {
		return nil, err
	}
	out := make([]Agent, 0, len(agents))
	for _, a := range agents {
		out = append(out, agentFrom(a))
	}
	return out, nil
}

// CreateAgent registers an agent; Token and Command are set, once.
func (d Direct) CreateAgent(ctx context.Context, org string, in AgentCreate) (*Agent, error) {
	sc, _, err := d.orgScope(ctx, org)
	if err != nil {
		return nil, err
	}
	a, token, err := d.Svc.CreateAgent(ctx, sc, in.Name, in.Labels)
	if err != nil {
		return nil, err
	}
	out := agentFrom(a)
	out.Token = token
	out.Command = domain.AgentCommand(d.Svc.Config().BaseURL, token, a.Labels)
	return &out, nil
}

// RevokeAgent revokes an agent's token and forgets it.
func (d Direct) RevokeAgent(ctx context.Context, org, name string) error {
	sc, _, err := d.orgScope(ctx, org)
	if err != nil {
		return err
	}
	return d.Svc.RevokeAgent(ctx, sc, name)
}
