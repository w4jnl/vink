-- Account: two-factor secrets, recovery codes, sign-in challenges and a
-- person's own sessions. All keyed by user, never by org.

-- name: SetUserTOTPSecret :exec
UPDATE users SET totp_secret = ?, totp_enabled_at = NULL, totp_last_step = 0 WHERE id = ?;

-- name: EnableUserTOTP :exec
UPDATE users SET totp_enabled_at = ?, totp_last_step = ? WHERE id = ?;

-- name: SetUserTOTPLastStep :exec
UPDATE users SET totp_last_step = ? WHERE id = ?;

-- name: InsertRecoveryCode :exec
INSERT INTO recovery_codes (user_id, prefix, hash, created_at) VALUES (?, ?, ?, ?);

-- name: ListRecoveryCodes :many
SELECT prefix, hash, used_at FROM recovery_codes WHERE user_id = ? ORDER BY prefix;

-- name: UseRecoveryCode :execrows
UPDATE recovery_codes SET used_at = ? WHERE user_id = ? AND prefix = ? AND used_at IS NULL;

-- name: CountRecoveryCodes :one
SELECT CAST(COUNT(*) AS INTEGER) AS total,
  CAST(COALESCE(SUM(CASE WHEN used_at IS NULL THEN 1 ELSE 0 END), 0) AS INTEGER) AS unused
FROM recovery_codes WHERE user_id = ?;

-- name: CreateLoginChallenge :exec
INSERT INTO login_challenges (id, user_id, created_at, expires_at, attempts, ip, user_agent, next_path)
VALUES (?, ?, ?, ?, 0, ?, ?, ?);

-- name: GetLoginChallenge :one
SELECT * FROM login_challenges WHERE id = ? AND expires_at > ?;

-- name: BumpLoginChallenge :one
UPDATE login_challenges SET attempts = attempts + 1 WHERE id = ? RETURNING attempts;

-- name: DeleteLoginChallenge :exec
DELETE FROM login_challenges WHERE id = ?;

-- name: DeleteExpiredLoginChallenges :execrows
DELETE FROM login_challenges WHERE expires_at <= ?;

-- name: ListUserSessions :many
SELECT * FROM sessions WHERE user_id = ? AND expires_at > ? ORDER BY COALESCE(last_seen_at, created_at) DESC;

-- name: DeleteOtherSessions :execrows
DELETE FROM sessions WHERE user_id = ? AND id != ?;
