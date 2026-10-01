package service

import (
	"context"
	"errors"
	"testing"

	"github.com/w4jnl/vink/internal/domain"
)

func TestOrgAPIKeys(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	orgAdmin := domain.Scope{OrgID: f.org.ID, UserID: "u3", Role: domain.RoleAdmin, Actor: "user:a"}

	if _, _, err := f.svc.CreateOrgAPIKey(ctx, f.member, "x", domain.AccessRW); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("member: %v", err)
	}
	if _, _, err := f.svc.CreateOrgAPIKey(ctx, orgAdmin, "x", "rwx"); err == nil {
		t.Fatal("bad access must fail")
	}
	rw, rwToken, err := f.svc.CreateOrgAPIKey(ctx, orgAdmin, "gitops", domain.AccessRW)
	if err != nil {
		t.Fatal(err)
	}
	_, roToken, err := f.svc.CreateOrgAPIKey(ctx, orgAdmin, "", domain.AccessRO)
	if err != nil {
		t.Fatal(err)
	}
	if !rw.IsOrg() || rw.OrgID != f.org.ID || rw.ProjectID != "" || rw.Name != "gitops" {
		t.Fatalf("org key: %+v", rw)
	}
	got, err := f.svc.VerifyAPIKey(ctx, rwToken)
	if err != nil || !got.IsOrg() || got.ID != rw.ID {
		t.Fatalf("verify: %+v %v", got, err)
	}
	if got, err := f.svc.VerifyAPIKey(ctx, roToken); err != nil || got.Access != domain.AccessRO || got.Name != "org key" {
		t.Fatalf("verify ro: %+v %v", got, err)
	}

	// org keys and project keys live apart
	pk, _, err := f.svc.CreateAPIKey(ctx, f.member, "proj", domain.AccessRO)
	if err != nil {
		t.Fatal(err)
	}
	orgKeys, err := f.svc.ListOrgAPIKeys(ctx, orgAdmin)
	if err != nil || len(orgKeys) != 2 {
		t.Fatalf("org keys: %d %v", len(orgKeys), err)
	}
	projKeys, err := f.svc.ListAPIKeys(ctx, f.member)
	if err != nil || len(projKeys) != 1 || projKeys[0].ID != pk.ID {
		t.Fatalf("project keys: %+v %v", projKeys, err)
	}
	if _, err := f.svc.ListOrgAPIKeys(ctx, f.member); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("member listing org keys: %v", err)
	}
	if err := f.svc.RevokeOrgAPIKey(ctx, orgAdmin, pk.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("a project key is not an org key: %v", err)
	}
	if err := f.svc.RevokeAPIKey(ctx, f.member, rw.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("an org key is not a project key: %v", err)
	}
	if err := f.svc.RevokeOrgAPIKey(ctx, orgAdmin, rw.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.VerifyAPIKey(ctx, rwToken); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("revoked org key: %v", err)
	}
	if keys, _ := f.svc.ListOrgAPIKeys(ctx, orgAdmin); len(keys) != 1 {
		t.Fatalf("after revoke: %d", len(keys))
	}
}
