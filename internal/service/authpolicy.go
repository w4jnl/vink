package service

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/w4jnl/vink/internal/audit"
	"github.com/w4jnl/vink/internal/db"
	"github.com/w4jnl/vink/internal/domain"
)

// Where the roles of people who sign in through a provider come from.
const (
	RolesGroups = "groups"
	RolesVink   = "vink"
)

// ProviderPolicy is one identity provider's say over roles.
type ProviderPolicy struct {
	Enabled bool `json:"enabled"`
	// Roles is RolesGroups (memberships and instance admin follow the
	// provider's groups) or RolesVink (they are set in vink).
	Roles string `json:"roles"`
	// InstanceAdmins are sign-in names, normalised, that are instance
	// admins on access; while listed, config wins over vink.
	InstanceAdmins []string `json:"instance_admins,omitempty"`
	StripRealm     bool     `json:"strip_realm"`
	Lowercase      bool     `json:"lowercase"`
	// AdminGroup names the instance admin group, for messages.
	AdminGroup string `json:"admin_group,omitempty"`
}

// GroupsDecide reports whether the provider's groups own its people's
// roles: it is on and in groups mode. A provider that is off syncs
// nothing, so whatever it left can be changed in vink.
func (p ProviderPolicy) GroupsDecide() bool { return p.Enabled && p.Roles != RolesVink }

// Normalize turns a sign-in name into the subject the provider sends.
func (p ProviderPolicy) Normalize(name string) string {
	return domain.NormalizeSubject(name, p.StripRealm, p.Lowercase)
}

// Listed reports whether the subject is in instance_admins.
func (p ProviderPolicy) Listed(subject string) bool {
	for _, s := range p.InstanceAdmins {
		if s == subject {
			return true
		}
	}
	return false
}

// AuthPolicy is the role source of both providers.
type AuthPolicy struct {
	Proxy ProviderPolicy `json:"proxy"`
	OIDC  ProviderPolicy `json:"oidc"`
}

// For returns the policy of a user source (proxy, oidc) or membership
// source (header, oidc); local has none and gets the zero policy.
func (p AuthPolicy) For(source string) ProviderPolicy {
	switch source {
	case "proxy", "header":
		return p.Proxy
	case "oidc":
		return p.OIDC
	}
	return ProviderPolicy{}
}

// SettingFor names a user source's config section, for messages.
func SettingFor(source string) string {
	if source == "oidc" {
		return "auth.oidc"
	}
	return "auth.proxy"
}

// AddSource is the provider an org admin's Add member creates accounts
// for: the proxy when it is on and in vink mode, else OIDC, else none.
func (p AuthPolicy) AddSource() string {
	switch {
	case p.Proxy.Enabled && p.Proxy.Roles == RolesVink:
		return "proxy"
	case p.OIDC.Enabled && p.OIDC.Roles == RolesVink:
		return "oidc"
	}
	return ""
}

// MetaAuthPolicy is the instance_meta name of the policy vink serve last
// started with, so a local vink admin sees the server's modes.
const MetaAuthPolicy = "auth.policy"

type policyHolder struct {
	mu sync.RWMutex
	p  *AuthPolicy
}

// AuthPolicy returns the role sources in force: the ones vink serve
// applied, else the ones it saved last, else groups for both.
func (s *Service) AuthPolicy(ctx context.Context) AuthPolicy {
	s.policy.mu.RLock()
	p := s.policy.p
	s.policy.mu.RUnlock()
	if p != nil {
		return *p
	}
	if saved, ok := s.savedPolicy(ctx, s.db.Read()); ok {
		return saved
	}
	return AuthPolicy{Proxy: ProviderPolicy{Roles: RolesGroups}, OIDC: ProviderPolicy{Roles: RolesGroups}}
}

func (s *Service) savedPolicy(ctx context.Context, q *db.Queries) (AuthPolicy, bool) {
	row, err := q.GetInstanceMeta(ctx, MetaAuthPolicy)
	if err != nil {
		return AuthPolicy{}, false
	}
	var p AuthPolicy
	if err := json.Unmarshal([]byte(row.Value), &p); err != nil {
		return AuthPolicy{}, false
	}
	return p, true
}

// ApplyAuthPolicy puts the configured role sources in force at start. A
// provider switched from groups to vink has its group-derived roles turned
// into ordinary ones, once, so nobody loses access; a switch back keeps
// those roles (they go on overriding groups) and says how many there are.
// Both are audited as the system. It warns when nobody is instance admin.
func (s *Service) ApplyAuthPolicy(ctx context.Context, p AuthPolicy) error {
	for _, pp := range []*ProviderPolicy{&p.Proxy, &p.OIDC} {
		if pp.Roles == "" {
			pp.Roles = RolesGroups
		}
		names := make([]string, 0, len(pp.InstanceAdmins))
		for _, n := range pp.InstanceAdmins {
			names = append(names, pp.Normalize(n))
		}
		pp.InstanceAdmins = names
	}
	err := s.db.Tx(ctx, func(q *db.Queries) error {
		prev, ok := s.savedPolicy(ctx, q)
		if !ok {
			prev = AuthPolicy{Proxy: ProviderPolicy{Roles: RolesGroups}, OIDC: ProviderPolicy{Roles: RolesGroups}}
		}
		for _, sw := range []struct {
			setting, membership, user string
			from, to                  string
		}{
			{"auth.proxy", "header", "proxy", prev.Proxy.Roles, p.Proxy.Roles},
			{"auth.oidc", "oidc", "oidc", prev.OIDC.Roles, p.OIDC.Roles},
		} {
			switch {
			case sw.from != RolesVink && sw.to == RolesVink:
				n, err := q.ConvertMembershipsSource(ctx, sw.membership)
				if err != nil {
					return err
				}
				s.log.Info("roles are set in vink now; the roles groups gave became ordinary ones", "setting", sw.setting+".roles", "memberships", n)
				if err := s.record(ctx, q, domain.Scope{}, audit.Entry{Action: "auth.roles", Target: sw.setting, Detail: map[string]any{"from": RolesGroups, "to": RolesVink, "memberships": n}}); err != nil {
					return err
				}
			case sw.from == RolesVink && sw.to != RolesVink:
				n, err := q.CountLocalMembershipsOfSource(ctx, sw.user)
				if err != nil {
					return err
				}
				if n > 0 {
					s.log.Warn("roles follow groups again, but roles set in vink stay and keep overriding them; remove them on the Members tabs to hand control back",
						"setting", sw.setting+".roles", "memberships", n)
				}
				if err := s.record(ctx, q, domain.Scope{}, audit.Entry{Action: "auth.roles", Target: sw.setting, Detail: map[string]any{"from": RolesVink, "to": RolesGroups, "memberships_set_in_vink": n}}); err != nil {
					return err
				}
			}
		}
		raw, err := json.Marshal(p)
		if err != nil {
			return err
		}
		return q.SetInstanceMeta(ctx, db.SetInstanceMetaParams{Name: MetaAuthPolicy, Value: string(raw), UpdatedAt: domain.Millis(s.now())})
	})
	if err != nil {
		return err
	}
	s.policy.mu.Lock()
	s.policy.p = &p
	s.policy.mu.Unlock()
	// nobody is instance admin, and no group or list can make one
	if n, err := s.db.Read().CountActiveInstanceAdmins(ctx); err == nil && n == 0 && len(p.Proxy.InstanceAdmins) == 0 && len(p.OIDC.InstanceAdmins) == 0 &&
		!p.Proxy.GroupsDecide() && !p.OIDC.GroupsDecide() {
		s.log.Error("nobody is instance admin: list a sign-in name in auth.proxy.instance_admins or auth.oidc.instance_admins, or run vink admin user promote on the server host")
	}
	return nil
}
