package service

import (
	"context"
	"crypto/rand"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/w4jnl/vink/internal/audit"
	"github.com/w4jnl/vink/internal/db"
	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/secrets"
	"github.com/w4jnl/vink/internal/totp"
)

// A person's own account: profile, password, two-factor sign-in with
// recovery codes, the sign-in code step, and their sessions.

const (
	// RecoveryCodeCount is how many codes a set holds.
	RecoveryCodeCount = 10
	// ChallengeTTL is how long the code step waits after the password.
	ChallengeTTL = 5 * time.Minute
	// ChallengeAttempts is how many wrong codes end the attempt.
	ChallengeAttempts = 5
)

// ErrCodeLocked says the sign-in attempt took too many wrong codes and is
// over; the person starts again with the password.
var ErrCodeLocked = errors.New("too many wrong codes")

// recoveryAlphabet leaves out the letters and digits people confuse.
const recoveryAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// UpdateProfile changes a local account's name and email.
func (s *Service) UpdateProfile(ctx context.Context, sc domain.Scope, name, email string) error {
	u, err := s.UserByID(ctx, sc.UserID)
	if err != nil {
		return err
	}
	if u.Source != "local" {
		return validation("profile", "name and email come from the identity provider")
	}
	name, email = strings.TrimSpace(name), strings.TrimSpace(email)
	if name == "" {
		name = u.Subject
	}
	if utf8.RuneCountInString(name) > 80 {
		return validation("acc_name", "at most 80 characters")
	}
	if email != "" && (!strings.Contains(email, "@") || strings.ContainsAny(email, " \t\n")) {
		return validation("acc_email", "does not look like an address")
	}
	if name == u.DisplayName && email == u.Email {
		return nil
	}
	return s.db.Tx(ctx, func(q *db.Queries) error {
		if err := q.UpdateUserProfile(ctx, db.UpdateUserProfileParams{Email: email, DisplayName: name, ID: u.ID}); err != nil {
			return err
		}
		var fields []string
		if name != u.DisplayName {
			fields = append(fields, "name")
		}
		if email != u.Email {
			fields = append(fields, "email")
		}
		return s.record(ctx, q, sc, audit.Entry{Action: "user.profile", Target: u.Subject, TargetID: u.ID, Detail: map[string]any{"fields": fields}})
	})
}

// ChangePassword sets a new password after checking the current one and
// signs out every other session.
func (s *Service) ChangePassword(ctx context.Context, sc domain.Scope, sessionID, current, next string) error {
	u, err := s.UserByID(ctx, sc.UserID)
	if err != nil {
		return err
	}
	if u.Source != "local" {
		return validation("pw_old", "only local accounts have a password")
	}
	if _, err := s.VerifyPassword(ctx, u.Subject, current); err != nil {
		return validation("pw_old", "that is not your current password")
	}
	if utf8.RuneCountInString(next) < MinPasswordLen {
		return validation("pw_new", "must be at least "+strconv.Itoa(MinPasswordLen)+" characters")
	}
	hash, err := secrets.HashPassword(next)
	if err != nil {
		return err
	}
	return s.db.Tx(ctx, func(q *db.Queries) error {
		if err := q.SetUserPassword(ctx, db.SetUserPasswordParams{PasswordHash: &hash, ID: u.ID}); err != nil {
			return err
		}
		if _, err := q.DeleteOtherSessions(ctx, db.DeleteOtherSessionsParams{UserID: u.ID, ID: sessionID}); err != nil {
			return err
		}
		return s.record(ctx, q, sc, audit.Entry{Action: "user.password", Target: u.Subject, TargetID: u.ID})
	})
}

// --- two-factor -----------------------------------------------------------

// TOTPSetup is a pending or live two-factor secret, decrypted.
type TOTPSetup struct {
	Secret string
	// URI is what the authenticator app scans.
	URI string
}

// totpSecret opens the stored secret; "" when none.
func (s *Service) totpSecret(row db.User) (string, error) {
	if row.TotpSecret == nil || *row.TotpSecret == "" {
		return "", nil
	}
	plain, err := s.keyring.Open(*row.TotpSecret)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// BeginTOTP makes (or returns the pending) secret for the setup panel; it
// goes live only when ConfirmTOTP sees a correct code. issuerAccount is
// shown in the app, usually subject@host.
func (s *Service) BeginTOTP(ctx context.Context, sc domain.Scope, account string) (*TOTPSetup, error) {
	row, err := s.db.Read().GetUser(ctx, sc.UserID)
	if err != nil {
		return nil, notFoundIfNoRows(err, "user")
	}
	if row.Source != "local" {
		return nil, validation("totp", "two-factor comes from the identity provider")
	}
	if row.TotpEnabledAt != nil {
		return nil, validation("totp", "two-factor is already on")
	}
	secret, err := s.totpSecret(row)
	if err != nil {
		return nil, err
	}
	if secret == "" {
		secret, err = totp.NewSecret()
		if err != nil {
			return nil, err
		}
		sealed, err := s.keyring.Seal([]byte(secret))
		if err != nil {
			return nil, err
		}
		if err := s.db.Write().SetUserTOTPSecret(ctx, db.SetUserTOTPSecretParams{TotpSecret: &sealed, ID: row.ID}); err != nil {
			return nil, err
		}
	}
	return &TOTPSetup{Secret: secret, URI: totp.URI("vink", account, secret)}, nil
}

// ConfirmTOTP turns two-factor on when the code matches the pending secret
// and returns the recovery codes, shown once.
func (s *Service) ConfirmTOTP(ctx context.Context, sc domain.Scope, code string) ([]string, error) {
	row, err := s.db.Read().GetUser(ctx, sc.UserID)
	if err != nil {
		return nil, notFoundIfNoRows(err, "user")
	}
	if row.TotpEnabledAt != nil {
		return nil, validation("otp", "two-factor is already on")
	}
	secret, err := s.totpSecret(row)
	if err != nil {
		return nil, err
	}
	if secret == "" {
		return nil, validation("otp", "start the setup first")
	}
	now := s.now()
	step, ok := totp.Verify(secret, code, now, 0)
	if !ok {
		return nil, validation("otp", "that code didn’t work; use the code on screen now, it changes every 30 seconds")
	}
	codes, hashes, err := newRecoveryCodes()
	if err != nil {
		return nil, err
	}
	err = s.db.Tx(ctx, func(q *db.Queries) error {
		if err := q.EnableUserTOTP(ctx, db.EnableUserTOTPParams{TotpEnabledAt: ptri(domain.Millis(now)), TotpLastStep: step, ID: row.ID}); err != nil {
			return err
		}
		if err := storeRecoveryCodes(ctx, q, row.ID, codes, hashes, now); err != nil {
			return err
		}
		return s.record(ctx, q, sc, audit.Entry{Action: "user.totp_on", Target: row.Subject, TargetID: row.ID})
	})
	if err != nil {
		return nil, err
	}
	return codes, nil
}

// NewRecoveryCodes replaces the set.
func (s *Service) NewRecoveryCodes(ctx context.Context, sc domain.Scope) ([]string, error) {
	u, err := s.UserByID(ctx, sc.UserID)
	if err != nil {
		return nil, err
	}
	if !u.TOTPOn() {
		return nil, validation("totp", "two-factor is off")
	}
	codes, hashes, err := newRecoveryCodes()
	if err != nil {
		return nil, err
	}
	now := s.now()
	err = s.db.Tx(ctx, func(q *db.Queries) error {
		if err := storeRecoveryCodes(ctx, q, u.ID, codes, hashes, now); err != nil {
			return err
		}
		return s.record(ctx, q, sc, audit.Entry{Action: "user.recovery_codes", Target: u.Subject, TargetID: u.ID})
	})
	if err != nil {
		return nil, err
	}
	return codes, nil
}

// DisableTOTP turns two-factor off after checking the password.
func (s *Service) DisableTOTP(ctx context.Context, sc domain.Scope, password string) error {
	u, err := s.UserByID(ctx, sc.UserID)
	if err != nil {
		return err
	}
	if !u.TOTPOn() {
		return nil
	}
	if _, err := s.VerifyPassword(ctx, u.Subject, password); err != nil {
		return validation("password", "that is not your password")
	}
	return s.db.Tx(ctx, func(q *db.Queries) error {
		if err := q.ResetUserTOTP(ctx, u.ID); err != nil {
			return err
		}
		if err := q.DeleteRecoveryCodes(ctx, u.ID); err != nil {
			return err
		}
		return s.record(ctx, q, sc, audit.Entry{Action: "user.totp_off", Target: u.Subject, TargetID: u.ID})
	})
}

// RecoveryCodesLeft counts the unused codes of a set.
func (s *Service) RecoveryCodesLeft(ctx context.Context, userID string) (left, total int, err error) {
	c, err := s.db.Read().CountRecoveryCodes(ctx, userID)
	if err != nil {
		return 0, 0, err
	}
	return int(c.Unused), int(c.Total), nil
}

func newRecoveryCodes() (codes, hashes []string, err error) {
	seen := map[string]bool{}
	for len(codes) < RecoveryCodeCount {
		b := make([]byte, 8)
		if _, err := rand.Read(b); err != nil {
			return nil, nil, err
		}
		raw := make([]byte, 8)
		for i, v := range b {
			raw[i] = recoveryAlphabet[int(v)%len(recoveryAlphabet)]
		}
		code := string(raw[:4]) + "-" + string(raw[4:])
		if seen[code[:4]] {
			continue
		}
		seen[code[:4]] = true
		hash, err := secrets.HashPassword(code)
		if err != nil {
			return nil, nil, err
		}
		codes = append(codes, code)
		hashes = append(hashes, hash)
	}
	return codes, hashes, nil
}

func storeRecoveryCodes(ctx context.Context, q *db.Queries, userID string, codes, hashes []string, now time.Time) error {
	if err := q.DeleteRecoveryCodes(ctx, userID); err != nil {
		return err
	}
	for i, code := range codes {
		if err := q.InsertRecoveryCode(ctx, db.InsertRecoveryCodeParams{UserID: userID, Prefix: code[:4], Hash: hashes[i], CreatedAt: domain.Millis(now)}); err != nil {
			return err
		}
	}
	return nil
}

// normalizeRecovery accepts "7kq2 m9xd", "7KQ2-M9XD" and the like.
func normalizeRecovery(code string) string {
	code = strings.ToUpper(strings.NewReplacer(" ", "", "-", "").Replace(strings.TrimSpace(code)))
	if len(code) != 8 {
		return ""
	}
	return code[:4] + "-" + code[4:]
}

// --- the sign-in code step ------------------------------------------------

// LoginChallenge is a password that checked out, waiting for the code.
type LoginChallenge struct {
	ID        string
	UserID    string
	Subject   string
	ExpiresAt time.Time
	Attempts  int
	Next      string
}

// StartChallenge opens the code step for a user whose password matched.
func (s *Service) StartChallenge(ctx context.Context, userID, ip, userAgent, next string) (string, error) {
	id, err := secrets.RandomToken(32)
	if err != nil {
		return "", err
	}
	now := s.now()
	err = s.db.Write().CreateLoginChallenge(ctx, db.CreateLoginChallengeParams{
		ID: id, UserID: userID, CreatedAt: domain.Millis(now), ExpiresAt: domain.Millis(now.Add(ChallengeTTL)), Ip: ip, UserAgent: userAgent, NextPath: next,
	})
	return id, err
}

// Challenge loads an open challenge; expired or unknown ones are not found.
func (s *Service) Challenge(ctx context.Context, id string) (*LoginChallenge, error) {
	if id == "" {
		return nil, domain.NotFound("sign-in")
	}
	row, err := s.db.Read().GetLoginChallenge(ctx, db.GetLoginChallengeParams{ID: id, ExpiresAt: domain.Millis(s.now())})
	if err != nil {
		return nil, notFoundIfNoRows(err, "sign-in")
	}
	u, err := s.UserByID(ctx, row.UserID)
	if err != nil {
		return nil, err
	}
	return &LoginChallenge{ID: row.ID, UserID: row.UserID, Subject: u.Subject, ExpiresAt: domain.FromMillis(row.ExpiresAt), Attempts: int(row.Attempts), Next: row.NextPath}, nil
}

// CodeResult says how a challenge was passed.
type CodeResult struct {
	User *domain.User
	// Method is totp or recovery.
	Method string
	// Left is how many recovery codes remain after a recovery sign-in.
	Left int
	Next string
}

// CompleteChallenge checks a code from the app, or a recovery code, and
// closes the challenge. A wrong code counts; after ChallengeAttempts the
// challenge is gone and ErrCodeLocked comes back.
func (s *Service) CompleteChallenge(ctx context.Context, id, code string) (*CodeResult, error) {
	ch, err := s.Challenge(ctx, id)
	if err != nil {
		return nil, err
	}
	row, err := s.db.Read().GetUser(ctx, ch.UserID)
	if err != nil {
		return nil, notFoundIfNoRows(err, "user")
	}
	u := userFromRow(row)
	if u.Disabled() {
		_ = s.db.Write().DeleteLoginChallenge(ctx, id)
		return nil, domain.ErrUnauthorized
	}
	secret, err := s.totpSecret(row)
	if err != nil {
		return nil, err
	}
	now := s.now()
	res := &CodeResult{User: u, Next: ch.Next}
	// a wrong code must still commit the attempt count, so the outcome
	// travels out of the transaction as a value, not as its error
	var outcome error
	err = s.db.Tx(ctx, func(q *db.Queries) error {
		if step, ok := totp.Verify(secret, code, now, row.TotpLastStep); ok && row.TotpEnabledAt != nil {
			res.Method = "totp"
			if err := q.SetUserTOTPLastStep(ctx, db.SetUserTOTPLastStepParams{TotpLastStep: step, ID: u.ID}); err != nil {
				return err
			}
			return q.DeleteLoginChallenge(ctx, id)
		}
		if rc := normalizeRecovery(code); rc != "" {
			rows, err := q.ListRecoveryCodes(ctx, u.ID)
			if err != nil {
				return err
			}
			for _, r := range rows {
				if r.Prefix != rc[:4] || r.UsedAt != nil || !secrets.VerifyPassword(r.Hash, rc) {
					continue
				}
				n, err := q.UseRecoveryCode(ctx, db.UseRecoveryCodeParams{UsedAt: ptri(domain.Millis(now)), UserID: u.ID, Prefix: r.Prefix})
				if err != nil {
					return err
				}
				if n == 0 {
					break
				}
				c, err := q.CountRecoveryCodes(ctx, u.ID)
				if err != nil {
					return err
				}
				res.Method, res.Left = "recovery", int(c.Unused)
				return q.DeleteLoginChallenge(ctx, id)
			}
		}
		attempts, err := q.BumpLoginChallenge(ctx, id)
		if err != nil {
			return err
		}
		outcome = domain.ErrUnauthorized
		if attempts >= ChallengeAttempts {
			outcome = ErrCodeLocked
			return q.DeleteLoginChallenge(ctx, id)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if outcome != nil {
		return nil, outcome
	}
	return res, nil
}

// --- sessions -------------------------------------------------------------

// Sessions lists a person's live sessions, most recently seen first.
func (s *Service) Sessions(ctx context.Context, userID string) ([]Session, error) {
	rows, err := s.db.Read().ListUserSessions(ctx, db.ListUserSessionsParams{UserID: userID, ExpiresAt: domain.Millis(s.now())})
	if err != nil {
		return nil, err
	}
	out := make([]Session, 0, len(rows))
	for _, r := range rows {
		out = append(out, *sessionFromRow(r))
	}
	return out, nil
}

// DeleteOwnSession ends one of the person's sessions.
func (s *Service) DeleteOwnSession(ctx context.Context, userID, sessionID string) error {
	sess, err := s.Session(ctx, sessionID)
	if err != nil {
		return err
	}
	if sess.UserID != userID {
		return domain.NotFound("session")
	}
	return s.db.Write().DeleteSession(ctx, sessionID)
}

// DeleteOtherSessions signs the person out everywhere but here.
func (s *Service) DeleteOtherSessions(ctx context.Context, sc domain.Scope, keepID string) (int64, error) {
	u, err := s.UserByID(ctx, sc.UserID)
	if err != nil {
		return 0, err
	}
	var n int64
	err = s.db.Tx(ctx, func(q *db.Queries) error {
		n, err = q.DeleteOtherSessions(ctx, db.DeleteOtherSessionsParams{UserID: u.ID, ID: keepID})
		if err != nil || n == 0 {
			return err
		}
		return s.record(ctx, q, sc, audit.Entry{Action: "user.signout", Target: u.Subject, TargetID: u.ID, Detail: map[string]any{"others": n}})
	})
	return n, err
}
