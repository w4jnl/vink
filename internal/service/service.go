// Package service is the one layer that touches the store. It owns
// authorization (every method takes a Scope), transactions, and the
// observe path that feeds the state machine. Transports call it; it never
// knows about HTTP.
package service

import (
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/w4jnl/vink/internal/db"
	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/engine"
)

// Config carries the instance settings the service needs.
type Config struct {
	// PingBaseURL is the base for ping URLs, without a trailing slash.
	PingBaseURL string
	// BodyLimit caps captured ping bodies.
	BodyLimit int64
	// AutoCreatePeriod and AutoCreateGrace are the defaults for ?create=1.
	AutoCreatePeriod domain.Duration
	AutoCreateGrace  domain.Duration
	// PingKeyGrace is how long a rotated ping key keeps working.
	PingKeyGrace time.Duration
}

// DefaultConfig returns the design document's defaults.
func DefaultConfig() Config {
	return Config{
		PingBaseURL:      "http://localhost:8080",
		BodyLimit:        64 * 1024,
		AutoCreatePeriod: domain.MustDuration("1d"),
		AutoCreateGrace:  domain.MustDuration("1h"),
		PingKeyGrace:     24 * time.Hour,
	}
}

// Service is the application core.
type Service struct {
	db  *db.DB
	bus *engine.Bus
	log *slog.Logger
	cfg Config
	now func() time.Time
}

// New wires a service. The clock is time.Now unless SetClock is called.
func New(d *db.DB, bus *engine.Bus, log *slog.Logger, cfg Config) *Service {
	if log == nil {
		log = slog.Default()
	}
	if bus == nil {
		bus = engine.NewBus()
	}
	return &Service{db: d, bus: bus, log: log, cfg: cfg, now: func() time.Time { return time.Now().UTC() }}
}

// SetClock replaces the clock, for tests.
func (s *Service) SetClock(now func() time.Time) { s.now = now }

// Now returns the service clock.
func (s *Service) Now() time.Time { return s.now() }

// Config returns the instance settings.
func (s *Service) Config() Config { return s.cfg }

// Bus returns the in-process bus.
func (s *Service) Bus() *engine.Bus { return s.bus }

// DB exposes the store for the auth layer and tests.
func (s *Service) DB() *db.DB { return s.db }

// PingURL renders the canonical ping URL for a monitor.
func (s *Service) PingURL(p *domain.Project, slug string) string {
	return s.cfg.PingBaseURL + "/ping/" + p.PingKey + "/" + slug
}

func requireProject(sc domain.Scope) error {
	if sc.ProjectID == "" || sc.OrgID == "" {
		return domain.ErrForbidden
	}
	return nil
}

func requireEdit(sc domain.Scope) error {
	if err := requireProject(sc); err != nil {
		return err
	}
	if !sc.CanEdit() {
		return domain.ErrForbidden
	}
	return nil
}

func requireOperate(sc domain.Scope) error {
	if err := requireProject(sc); err != nil {
		return err
	}
	if !sc.CanOperate() {
		return domain.ErrForbidden
	}
	return nil
}

func requireInstanceAdmin(sc domain.Scope) error {
	if !sc.InstanceAdmin {
		return domain.ErrForbidden
	}
	return nil
}

// NewPingKey returns 22 lowercase base32 characters (110 bits).
func NewPingKey() string {
	var b [14]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Errorf("crypto/rand: %w", err))
	}
	s := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:])
	return strings.ToLower(s[:22])
}

// notFoundIfNoRows maps sql.ErrNoRows to the domain sentinel.
func notFoundIfNoRows(err error, what string) error {
	if db.IsNotFound(err) {
		return domain.NotFound(what)
	}
	return err
}

// conflictIfUnique maps a unique violation to the domain sentinel.
func conflictIfUnique(err error, msg string) error {
	if db.IsUniqueViolation(err) {
		return domain.Conflict(msg)
	}
	return err
}

func strp(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func ptrs(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func ptri(v int64) *int64 { return &v }

var errNoTx = errors.New("service: nil transaction")
