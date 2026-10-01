package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/w4jnl/vink/internal/db"
	"github.com/w4jnl/vink/internal/domain"
)

func TestAgentsLifecycle(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	orgAdmin := domain.Scope{OrgID: f.org.ID, UserID: "u3", Role: domain.RoleAdmin, Actor: "user:a"}

	// members and keys cannot register agents
	if _, _, err := f.svc.CreateAgent(ctx, f.member, "probe", nil); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("member: %v", err)
	}
	key := domain.Scope{OrgID: f.org.ID, ProjectID: f.project.ID, KeyID: "k1", KeyAccess: domain.AccessRW, Actor: "key:k1"}
	if _, _, err := f.svc.CreateAgent(ctx, key, "probe", nil); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("key: %v", err)
	}

	a, token, err := f.svc.CreateAgent(ctx, orgAdmin, "DC2-probe", map[string]string{"site": "dc2"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Name != "dc2-probe" || !strings.HasPrefix(token, "vat_"+a.TokenPrefix+"_") || a.State(false) != domain.AgentWaiting {
		t.Fatalf("created: %+v %s", a, token)
	}
	if _, _, err := f.svc.CreateAgent(ctx, orgAdmin, "dc2-probe", nil); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate: %v", err)
	}

	// the token verifies and is cached; a wrong secret with the right prefix is refused
	got, err := f.svc.VerifyAgentToken(ctx, token)
	if err != nil || got.ID != a.ID {
		t.Fatalf("verify: %v", err)
	}
	if _, err := f.svc.VerifyAgentToken(ctx, "vat_"+a.TokenPrefix+"_"+strings.Repeat("x", 32)); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("wrong secret: %v", err)
	}
	if _, err := f.svc.VerifyAgentToken(ctx, "garbage"); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("garbage: %v", err)
	}

	// presence and the last contact
	if err := f.svc.TouchAgent(ctx, a.ID, "10.0.0.5:4421", "0.2.0"); err != nil {
		t.Fatal(err)
	}
	f.svc.SetAgentPresence(func(id string) bool { return id == a.ID })
	got, err = f.svc.Agent(ctx, orgAdmin, "dc2-probe")
	if err != nil {
		t.Fatal(err)
	}
	if got.LastSeenAt == nil || got.LastAddr != "10.0.0.5:4421" || got.Version != "0.2.0" || got.State(f.svc.AgentConnected(got.ID)) != domain.AgentConnected {
		t.Fatalf("touched: %+v", got)
	}
	f.svc.SetAgentPresence(func(string) bool { return false })
	if got.State(f.svc.AgentConnected(got.ID)) != domain.AgentOffline {
		t.Fatalf("offline: %+v", got)
	}

	// labels
	upd, err := f.svc.UpdateAgentLabels(ctx, orgAdmin, "dc2-probe", map[string]string{"site": "dc3", "zone": "dmz"})
	if err != nil || upd.Labels["zone"] != "dmz" || upd.Labels["site"] != "dc3" {
		t.Fatalf("labels: %v %+v", err, upd)
	}
	if _, err := f.svc.UpdateAgentLabels(ctx, orgAdmin, "dc2-probe", map[string]string{"Bad Key": "x"}); err == nil {
		t.Fatal("bad label must fail")
	}
	list, err := f.svc.ListAgents(ctx, orgAdmin)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v %d", err, len(list))
	}

	// the quota counts live agents
	one := int64(1)
	if err := f.svc.DB().Write().SetOrgQuotas(ctx, db.SetOrgQuotasParams{QuotaAgents: &one, ID: f.org.ID}); err != nil {
		t.Fatal(err)
	}
	var ve *domain.ValidationError
	if _, _, err := f.svc.CreateAgent(ctx, orgAdmin, "second", nil); !errors.As(err, &ve) || !strings.Contains(err.Error(), "ask the instance admin") {
		t.Fatalf("quota: %v", err)
	}

	// revoke: gone from the list, token refused even though it was cached
	f.drainEvents()
	if err := f.svc.RevokeAgent(ctx, orgAdmin, "dc2-probe"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Agent(ctx, orgAdmin, "dc2-probe"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("after revoke: %v", err)
	}
	if _, err := f.svc.VerifyAgentToken(ctx, token); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("revoked token: %v", err)
	}
	select {
	case <-f.events:
	case <-time.After(time.Second):
		t.Fatal("revoke must wake the engine")
	}
	if _, _, err := f.svc.CreateAgent(ctx, orgAdmin, "second", nil); err != nil {
		t.Fatalf("quota after revoke: %v", err)
	}

	// another org sees nothing
	other, err := f.svc.CreateOrg(ctx, f.admin, "acme", "Acme")
	if err != nil {
		t.Fatal(err)
	}
	otherAdmin := domain.Scope{OrgID: other.ID, UserID: "u9", Role: domain.RoleOwner, Actor: "user:o"}
	if _, err := f.svc.Agent(ctx, otherAdmin, "second"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cross org: %v", err)
	}
	if list, _ := f.svc.ListAgents(ctx, otherAdmin); len(list) != 0 {
		t.Fatalf("cross org list: %d", len(list))
	}
}
