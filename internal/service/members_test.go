package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/w4jnl/vink/internal/domain"
)

func TestInvitesTransferAndDeleteOrg(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	owner, _ := f.svc.CreateLocalUser(ctx, f.admin, "own", "", "", "correct horse", false)
	_ = f.svc.SetMembership(ctx, f.admin, owner.ID, f.org.ID, domain.RoleOwner)
	ownerSc := domain.Scope{OrgID: f.org.ID, UserID: owner.ID, Role: domain.RoleOwner, Actor: "user:own"}
	adminSc := domain.Scope{OrgID: f.org.ID, UserID: "ua", Role: domain.RoleAdmin, Actor: "user:adm"}

	// who may invite whom
	if _, _, err := f.svc.CreateInvite(ctx, f.member, "x", domain.RoleMember); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("member inviting: %v", err)
	}
	if _, _, err := f.svc.CreateInvite(ctx, adminSc, "x", domain.RoleOwner); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("admin inviting an owner: %v", err)
	}
	if _, _, err := f.svc.CreateInvite(ctx, adminSc, "", domain.RoleMember); err == nil || !strings.Contains(err.Error(), "who the invite is for") {
		t.Fatalf("empty note: %v", err)
	}
	inv, token, err := f.svc.CreateInvite(ctx, ownerSc, "Lisa", domain.RoleOwner)
	if err != nil || !strings.HasPrefix(token, "iv_") || inv.State(f.clock.Now()) != domain.InviteOpen || inv.ExpiresAt.Sub(f.clock.Now()) != domain.InviteTTL {
		t.Fatalf("invite: %+v %s %v", inv, token, err)
	}
	if got, err := f.svc.InviteByToken(ctx, token); err != nil || got.OrgSlug != "homelab" || got.CreatedBy != "own" || got.Role != domain.RoleOwner {
		t.Fatalf("by token: %+v %v", got, err)
	}
	if _, err := f.svc.InviteByToken(ctx, "iv_nope"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown token: %v", err)
	}

	// accept once
	if _, err := f.svc.AcceptInvite(ctx, token, "lisa", "Lisa", "short"); err == nil {
		t.Fatal("a short password must fail")
	}
	lisa, err := f.svc.AcceptInvite(ctx, token, "lisa", "Lisa", "a-long-passphrase")
	if err != nil {
		t.Fatal(err)
	}
	if ms, _ := f.svc.MembershipsForUser(ctx, lisa.ID); len(ms) != 1 || ms[0].Role != domain.RoleOwner || ms[0].Source != "local" {
		t.Fatalf("membership: %+v", ms)
	}
	if _, err := f.svc.AcceptInvite(ctx, token, "lisa2", "", "a-long-passphrase"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("second accept: %v", err)
	}
	if _, err := f.svc.UserBySubject(ctx, "lisa2"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("a refused accept must not create a user")
	}
	list, _ := f.svc.ListInvites(ctx, ownerSc)
	if len(list) != 1 || list[0].State(f.clock.Now()) != domain.InviteUsed || list[0].UsedBy != "lisa" || list[0].CreatedBy != "own" {
		t.Fatalf("list: %+v", list)
	}

	// revoke and expiry close a link; removal tidies
	marloes, tok2, _ := f.svc.CreateInvite(ctx, ownerSc, "Marloes", domain.RoleViewer)
	if err := f.svc.RevokeInvite(ctx, ownerSc, marloes.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.RevokeInvite(ctx, ownerSc, marloes.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("revoke twice: %v", err)
	}
	if _, err := f.svc.AcceptInvite(ctx, tok2, "marloes", "", "a-long-passphrase"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("accept revoked: %v", err)
	}
	_, tok3, _ := f.svc.CreateInvite(ctx, ownerSc, "Old", domain.RoleViewer)
	f.clock.Add(domain.InviteTTL + time.Second)
	if got, _ := f.svc.InviteByToken(ctx, tok3); got.State(f.clock.Now()) != domain.InviteExpired {
		t.Fatalf("expired state: %s", got.State(f.clock.Now()))
	}
	if _, err := f.svc.AcceptInvite(ctx, tok3, "old", "", "a-long-passphrase"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("accept expired: %v", err)
	}
	list, _ = f.svc.ListInvites(ctx, ownerSc)
	for _, inv := range list {
		if inv.State(f.clock.Now()) == domain.InviteUsed {
			if err := f.svc.RemoveInvite(ctx, ownerSc, inv.ID); !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("a used invite stays: %v", err)
			}
		} else if err := f.svc.RemoveInvite(ctx, ownerSc, inv.ID); err != nil {
			t.Fatalf("remove %s: %v", inv.State(f.clock.Now()), err)
		}
	}
	if list, _ = f.svc.ListInvites(ctx, ownerSc); len(list) != 1 {
		t.Fatalf("after removal: %+v", list)
	}

	// transfer: lisa owns, own stays as admin; the audit says so
	if err := f.svc.TransferOwnership(ctx, adminSc, lisa.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("admin transferring: %v", err)
	}
	if err := f.svc.TransferOwnership(ctx, ownerSc, owner.ID); err == nil || !strings.Contains(err.Error(), "owner already") {
		t.Fatalf("transfer to self: %v", err)
	}
	members, _ := f.svc.ListMembers(ctx, ownerSc)
	if len(members) != 2 {
		t.Fatalf("members: %+v", members)
	}
	if err := f.svc.TransferOwnership(ctx, ownerSc, lisa.ID); err != nil {
		t.Fatal(err)
	}
	roles := map[string]domain.Role{}
	for _, m := range members {
		roles[m.Subject] = m.Role
	}
	members, _ = f.svc.ListMembers(ctx, domain.Scope{OrgID: f.org.ID, UserID: lisa.ID, Role: domain.RoleOwner, Actor: "user:lisa"})
	for _, m := range members {
		roles[m.Subject] = m.Role
	}
	if roles["lisa"] != domain.RoleOwner || roles["own"] != domain.RoleAdmin {
		t.Fatalf("after transfer: %v", roles)
	}

	// delete: refused with projects, allowed on an empty org, audited at instance level
	if err := f.svc.DeleteOrg(ctx, domain.Scope{OrgID: f.org.ID, UserID: lisa.ID, Role: domain.RoleOwner, Actor: "user:lisa"}); err == nil || !strings.Contains(err.Error(), "delete its projects first") {
		t.Fatalf("delete with projects: %v", err)
	}
	empty, _ := f.svc.CreateOrg(ctx, f.admin, "empty", "Empty")
	_ = f.svc.SetMembership(ctx, f.admin, owner.ID, empty.ID, domain.RoleOwner)
	emptyOwner := domain.Scope{OrgID: empty.ID, UserID: owner.ID, Role: domain.RoleOwner, Actor: "user:own"}
	if err := f.svc.DeleteOrg(ctx, domain.Scope{OrgID: empty.ID, UserID: owner.ID, Role: domain.RoleAdmin, Actor: "user:own"}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("admin deleting: %v", err)
	}
	if err := f.svc.DeleteOrg(ctx, emptyOwner); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.OrgBySlug(ctx, "empty"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("org still there: %v", err)
	}
	all := f.auditAll(t, f.admin, AuditFilter{Since: start.Add(-time.Hour)})
	seen := map[string]bool{}
	for _, e := range all {
		seen[e.Action] = true
	}
	for _, want := range []string{"invite.create", "invite.accept", "invite.revoke", "org.transfer", "org.delete", "user.create"} {
		if !seen[want] {
			t.Errorf("audit lacks %s", want)
		}
	}
}
