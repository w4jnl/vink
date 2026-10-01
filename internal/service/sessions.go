package service

import (
	"context"
	"time"

	"github.com/w4jnl/vink/internal/db"
	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/secrets"
)

// SessionTTL is the sliding lifetime of a browser session.
const SessionTTL = 30 * 24 * time.Hour

// Session is a browser session row.
type Session struct {
	ID            string
	UserID        string
	CSRF          string
	ExpiresAt     time.Time
	LastProjectID string
	CreatedAt     time.Time
	UserAgent     string
	IP            string
	LastSeenAt    *time.Time
}

// CreateSession starts a session for the user and returns it.
func (s *Service) CreateSession(ctx context.Context, userID string) (*Session, error) {
	return s.CreateSessionWith(ctx, userID, "", "")
}

// CreateSessionWith starts a session and records where it came from.
func (s *Service) CreateSessionWith(ctx context.Context, userID, ip, userAgent string) (*Session, error) {
	id, err := secrets.RandomToken(32)
	if err != nil {
		return nil, err
	}
	csrf, err := secrets.RandomToken(32)
	if err != nil {
		return nil, err
	}
	now := s.now()
	if len(userAgent) > 200 {
		userAgent = userAgent[:200]
	}
	seen := now
	sess := &Session{ID: id, UserID: userID, CSRF: csrf, ExpiresAt: now.Add(SessionTTL), CreatedAt: now, UserAgent: userAgent, IP: ip, LastSeenAt: &seen}
	if err := s.db.Write().CreateSession(ctx, db.CreateSessionParams{ID: id, UserID: userID, Csrf: csrf, CreatedAt: domain.Millis(now), ExpiresAt: domain.Millis(sess.ExpiresAt), UserAgent: userAgent, Ip: ip, LastSeenAt: ptri(domain.Millis(now))}); err != nil {
		return nil, err
	}
	return sess, nil
}

// Session loads a live session, extending it when less than 29 days
// remain so the lifetime slides without a write on every request.
func (s *Service) Session(ctx context.Context, id string) (*Session, error) {
	now := s.now()
	row, err := s.db.Read().GetSession(ctx, db.GetSessionParams{ID: id, ExpiresAt: domain.Millis(now)})
	if err != nil {
		return nil, notFoundIfNoRows(err, "session")
	}
	sess := &Session{ID: row.ID, UserID: row.UserID, CSRF: row.Csrf, ExpiresAt: domain.FromMillis(row.ExpiresAt), LastProjectID: strp(row.LastProjectID), CreatedAt: domain.FromMillis(row.CreatedAt), UserAgent: row.UserAgent, IP: row.Ip, LastSeenAt: domain.FromMillisPtr(row.LastSeenAt)}
	if sess.ExpiresAt.Sub(now) < SessionTTL-24*time.Hour {
		sess.ExpiresAt = now.Add(SessionTTL)
		if err := s.db.Write().TouchSession(ctx, db.TouchSessionParams{ExpiresAt: domain.Millis(sess.ExpiresAt), ID: id}); err != nil {
			s.log.Warn("touch session", "err", err)
		}
	}
	return sess, nil
}

// SeenSession records activity on a session at most once a minute, with
// the address it came from.
func (s *Service) SeenSession(ctx context.Context, sess *Session, ip string) {
	now := s.now()
	if sess.LastSeenAt != nil && now.Sub(*sess.LastSeenAt) < time.Minute && sess.IP == ip {
		return
	}
	if err := s.db.Write().TouchSessionSeen(ctx, db.TouchSessionSeenParams{LastSeenAt: ptri(domain.Millis(now)), Ip: ip, ID: sess.ID}); err != nil {
		s.log.Warn("touch session", "err", err)
		return
	}
	sess.LastSeenAt, sess.IP = &now, ip
}

// DeleteSession ends a session.
func (s *Service) DeleteSession(ctx context.Context, id string) error {
	return s.db.Write().DeleteSession(ctx, id)
}

// SetSessionProject remembers the last project for the "/" redirect.
func (s *Service) SetSessionProject(ctx context.Context, sessionID, projectID string) error {
	return s.db.Write().SetSessionProject(ctx, db.SetSessionProjectParams{LastProjectID: ptrs(projectID), ID: sessionID})
}

// DeleteExpiredSessions is the housekeeping call.
func (s *Service) DeleteExpiredSessions(ctx context.Context) (int64, error) {
	return s.db.Write().DeleteExpiredSessions(ctx, domain.Millis(s.now()))
}
