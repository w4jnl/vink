package service

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/totp"
)

func TestAccountProfilePasswordAndSessions(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	u, _ := f.svc.CreateLocalUser(ctx, f.admin, "j", "j@example.com", "Jaro", "correct horse", false)
	sc := domain.Scope{UserID: u.ID, Actor: "user:j"}

	if err := f.svc.UpdateProfile(ctx, sc, "Jaro Z", "not an address"); err == nil || !strings.Contains(err.Error(), "address") {
		t.Fatalf("bad email: %v", err)
	}
	if err := f.svc.UpdateProfile(ctx, sc, "Jaro Z", "jz@example.com"); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.svc.UserByID(ctx, u.ID); got.DisplayName != "Jaro Z" || got.Email != "jz@example.com" {
		t.Fatalf("profile: %+v", got)
	}

	keep, _ := f.svc.CreateSessionWith(ctx, u.ID, "10.0.0.1", "curl")
	other, _ := f.svc.CreateSessionWith(ctx, u.ID, "10.0.0.2", "Mozilla/5.0 (iPhone) Safari/605")
	if err := f.svc.ChangePassword(ctx, sc, keep.ID, "wrong", "a brand new passphrase"); err == nil || !strings.Contains(err.Error(), "current password") {
		t.Fatalf("wrong current: %v", err)
	}
	if err := f.svc.ChangePassword(ctx, sc, keep.ID, "correct horse", "short"); err == nil || !strings.Contains(err.Error(), "at least") {
		t.Fatalf("short: %v", err)
	}
	if err := f.svc.ChangePassword(ctx, sc, keep.ID, "correct horse", "a brand new passphrase"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Session(ctx, other.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("other session after password change: %v", err)
	}
	if _, err := f.svc.Session(ctx, keep.ID); err != nil {
		t.Fatalf("own session after password change: %v", err)
	}
	if _, err := f.svc.VerifyPassword(ctx, "j", "a brand new passphrase"); err != nil {
		t.Fatal(err)
	}

	s2, _ := f.svc.CreateSessionWith(ctx, u.ID, "10.0.0.2", "curl")
	s3, _ := f.svc.CreateSessionWith(ctx, u.ID, "10.0.0.3", "curl")
	if list, err := f.svc.Sessions(ctx, u.ID); err != nil || len(list) != 3 {
		t.Fatalf("sessions: %d %v", len(list), err)
	}
	if err := f.svc.DeleteOwnSession(ctx, "someone-else", s2.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("another user's session: %v", err)
	}
	if err := f.svc.DeleteOwnSession(ctx, u.ID, s2.ID); err != nil {
		t.Fatal(err)
	}
	if n, err := f.svc.DeleteOtherSessions(ctx, sc, keep.ID); err != nil || n != 1 {
		t.Fatalf("others: %d %v", n, err)
	}
	if _, err := f.svc.Session(ctx, s3.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("s3 survived")
	}
	if list, _ := f.svc.Sessions(ctx, u.ID); len(list) != 1 || list[0].ID != keep.ID {
		t.Fatalf("left: %+v", list)
	}
}

func TestAccountTOTPAndRecoveryCodes(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	u, _ := f.svc.CreateLocalUser(ctx, f.admin, "j", "", "", "correct horse", false)
	sc := domain.Scope{UserID: u.ID, Actor: "user:j"}
	proxied, _ := f.svc.EnsureProxyUser(ctx, "anne", "", "")
	if _, err := f.svc.BeginTOTP(ctx, domain.Scope{UserID: proxied.ID, Actor: "user:anne"}, "anne"); err == nil {
		t.Fatal("proxy user set up two-factor")
	}

	// the pending secret is reused until a correct code turns it on
	setup, err := f.svc.BeginTOTP(ctx, sc, "j@vink.test")
	if err != nil || len(setup.Secret) != 32 || !strings.HasPrefix(setup.URI, "otpauth://totp/vink:j@vink.test?") {
		t.Fatalf("begin: %+v %v", setup, err)
	}
	if again, _ := f.svc.BeginTOTP(ctx, sc, "j@vink.test"); again.Secret != setup.Secret {
		t.Fatal("a second begin made a new secret")
	}
	if _, err := f.svc.ConfirmTOTP(ctx, sc, "000000"); err == nil || !strings.Contains(err.Error(), "didn’t work") {
		t.Fatalf("wrong code: %v", err)
	}
	if got, _ := f.svc.UserByID(ctx, u.ID); got.TOTPOn() {
		t.Fatal("on before a correct code")
	}
	code, _ := totp.Code(setup.Secret, f.clock.Now())
	codes, err := f.svc.ConfirmTOTP(ctx, sc, code)
	if err != nil || len(codes) != RecoveryCodeCount {
		t.Fatalf("confirm: %v %v", codes, err)
	}
	shape := regexp.MustCompile(`^[A-Z2-9]{4}-[A-Z2-9]{4}$`)
	for _, c := range codes {
		if !shape.MatchString(c) || strings.ContainsAny(c, "IO01") {
			t.Errorf("code %q", c)
		}
	}
	if got, _ := f.svc.UserByID(ctx, u.ID); !got.TOTPOn() {
		t.Fatal("not on")
	}
	if left, total, _ := f.svc.RecoveryCodesLeft(ctx, u.ID); left != 10 || total != 10 {
		t.Fatalf("codes left: %d of %d", left, total)
	}
	if _, err := f.svc.BeginTOTP(ctx, sc, "j"); err == nil {
		t.Fatal("begin while on")
	}

	// the code step: the setup code is spent, the next step passes, a reused code fails
	id, err := f.svc.StartChallenge(ctx, u.ID, "10.0.0.1", "curl", "/next")
	if err != nil {
		t.Fatal(err)
	}
	if ch, err := f.svc.Challenge(ctx, id); err != nil || ch.Subject != "j" || ch.Next != "/next" || ch.ExpiresAt.Sub(f.clock.Now()) != ChallengeTTL {
		t.Fatalf("challenge: %+v %v", ch, err)
	}
	if _, err := f.svc.CompleteChallenge(ctx, id, code); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("setup code reused: %v", err)
	}
	f.clock.Add(30 * time.Second)
	next, _ := totp.Code(setup.Secret, f.clock.Now())
	res, err := f.svc.CompleteChallenge(ctx, id, next)
	if err != nil || res.Method != "totp" || res.Next != "/next" || res.User.ID != u.ID {
		t.Fatalf("complete: %+v %v", res, err)
	}
	if _, err := f.svc.Challenge(ctx, id); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("challenge open after use")
	}
	id2, _ := f.svc.StartChallenge(ctx, u.ID, "", "", "/")
	if _, err := f.svc.CompleteChallenge(ctx, id2, next); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("replay: %v", err)
	}

	// the window: one step back still passes, two do not
	f.clock.Add(60 * time.Second)
	old, _ := totp.CodeAt(setup.Secret, totp.Step(f.clock.Now())-1)
	if _, err := f.svc.CompleteChallenge(ctx, id2, old); err != nil {
		t.Fatalf("one step back: %v", err)
	}
	id3, _ := f.svc.StartChallenge(ctx, u.ID, "", "", "/")
	f.clock.Add(60 * time.Second)
	stale, _ := totp.CodeAt(setup.Secret, totp.Step(f.clock.Now())-2)
	if _, err := f.svc.CompleteChallenge(ctx, id3, stale); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("two steps back: %v", err)
	}

	// a recovery code, in any case or spacing, works once
	res, err = f.svc.CompleteChallenge(ctx, id3, strings.ToLower(strings.ReplaceAll(codes[0], "-", " ")))
	if err != nil || res.Method != "recovery" || res.Left != 9 {
		t.Fatalf("recovery: %+v %v", res, err)
	}
	id4, _ := f.svc.StartChallenge(ctx, u.ID, "", "", "/")
	if _, err := f.svc.CompleteChallenge(ctx, id4, codes[0]); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("recovery code twice: %v", err)
	}

	// five wrong codes end the attempt
	for i := 0; i < ChallengeAttempts-2; i++ {
		if _, err := f.svc.CompleteChallenge(ctx, id4, "000000"); !errors.Is(err, domain.ErrUnauthorized) {
			t.Fatalf("wrong %d: %v", i, err)
		}
	}
	if _, err := f.svc.CompleteChallenge(ctx, id4, "000000"); !errors.Is(err, ErrCodeLocked) {
		t.Fatalf("lock: %v", err)
	}
	if _, err := f.svc.Challenge(ctx, id4); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("locked challenge still open")
	}
	id5, _ := f.svc.StartChallenge(ctx, u.ID, "", "", "/")
	f.clock.Add(ChallengeTTL + time.Second)
	if _, err := f.svc.Challenge(ctx, id5); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("expired challenge still open")
	}

	// new codes replace the set; turning off needs the password
	fresh, err := f.svc.NewRecoveryCodes(ctx, sc)
	if err != nil || len(fresh) != 10 {
		t.Fatal(err)
	}
	id6, _ := f.svc.StartChallenge(ctx, u.ID, "", "", "/")
	if _, err := f.svc.CompleteChallenge(ctx, id6, codes[1]); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("old set still works: %v", err)
	}
	if res, err := f.svc.CompleteChallenge(ctx, id6, fresh[0]); err != nil || res.Left != 9 {
		t.Fatalf("new set: %+v %v", res, err)
	}
	if err := f.svc.DisableTOTP(ctx, sc, "wrong"); err == nil {
		t.Fatal("off without the password")
	}
	if err := f.svc.DisableTOTP(ctx, sc, "correct horse"); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.svc.UserByID(ctx, u.ID); got.TOTPOn() {
		t.Fatal("still on")
	}
	if left, total, _ := f.svc.RecoveryCodesLeft(ctx, u.ID); left != 0 || total != 0 {
		t.Fatalf("codes after off: %d of %d", left, total)
	}

	page, err := f.svc.AuditLog(ctx, f.admin, AuditFilter{Since: start, Actor: "j"})
	if err != nil {
		t.Fatal(err)
	}
	var actions []string
	for _, e := range page.Entries {
		actions = append(actions, e.Action)
	}
	for _, want := range []string{"user.totp_on", "user.recovery_codes", "user.totp_off"} {
		if !strings.Contains(strings.Join(actions, " "), want) {
			t.Errorf("missing %s in %v", want, actions)
		}
	}
}
