// Package service is the one layer that touches the store. It owns
// authorization (every method takes a Scope), transactions, and the
// observe path that feeds the state machine. Transports call it; it never
// knows about HTTP.
package service

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/w4jnl/vink/internal/checks"
	"github.com/w4jnl/vink/internal/db"
	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/engine"
	"github.com/w4jnl/vink/internal/metrics"
	"github.com/w4jnl/vink/internal/notify"
	"github.com/w4jnl/vink/internal/secrets"
)

// Config carries the instance settings the service needs.
type Config struct {
	// BaseURL is the UI base for links in notifications.
	BaseURL string
	// PingBaseURL is the base for ping URLs, without a trailing slash.
	PingBaseURL string
	// BodyLimit caps captured ping bodies.
	BodyLimit int64
	// AutoCreatePeriod and AutoCreateGrace are the defaults for ?create=1.
	AutoCreatePeriod domain.Duration
	AutoCreateGrace  domain.Duration
	// PingKeyGrace is how long a rotated ping key keeps working.
	PingKeyGrace time.Duration
	// MinInterval is the instance floor for pull check intervals.
	MinInterval domain.Duration
	// Keyring encrypts channel configs. nil means an ephemeral key, which
	// is only acceptable in tests.
	Keyring *secrets.Keyring
}

// DefaultConfig returns the design document's defaults.
func DefaultConfig() Config {
	return Config{
		BaseURL:          "http://localhost:8080",
		PingBaseURL:      "http://localhost:8080",
		BodyLimit:        64 * 1024,
		AutoCreatePeriod: domain.MustDuration("1d"),
		AutoCreateGrace:  domain.MustDuration("1h"),
		PingKeyGrace:     24 * time.Hour,
		MinInterval:      domain.MinInterval,
	}
}

// store is what the service reads and writes through: the database, or
// one transaction of it while apply runs.
type store interface {
	Read() *db.Queries
	Write() *db.Queries
	Tx(ctx context.Context, fn func(q *db.Queries) error) error
}

// txStore binds every read and write to one open transaction.
type txStore struct{ q *db.Queries }

func (t txStore) Read() *db.Queries  { return t.q }
func (t txStore) Write() *db.Queries { return t.q }
func (t txStore) Tx(_ context.Context, fn func(q *db.Queries) error) error {
	return fn(t.q)
}

// Service is the application core.
type Service struct {
	db       store
	sqlDB    *db.DB
	bus      *engine.Bus
	log      *slog.Logger
	cfg      Config
	now      func() time.Time
	keyCache keyCache
	keyring  *secrets.Keyring
	// validateChannel is set by the notifier registry; nil accepts any
	// JSON object.
	validateChannel func(kind domain.ChannelKind, cfg []byte) error
	notifier        *notify.Registry
	checker         *checks.Registry
	checkNow        func(ctx context.Context, monitorID string) error
	metrics         *metrics.Metrics
	agentCache      agentCache
	agentPresence   func(agentID string) bool
	agentSince      func(agentID string) (time.Time, bool)
	// noAudit silences per-resource audit rows while an apply runs; the
	// apply writes one row of its own.
	noAudit bool
	// policy is where proxy and OIDC people's roles come from, set by
	// ApplyAuthPolicy at start; shared with transaction-bound copies.
	policy *policyHolder
}

// New wires a service. The clock is time.Now unless SetClock is called.
func New(d *db.DB, bus *engine.Bus, log *slog.Logger, cfg Config) *Service {
	if log == nil {
		log = slog.Default()
	}
	if bus == nil {
		bus = engine.NewBus()
	}
	s := &Service{db: d, sqlDB: d, bus: bus, log: log, cfg: cfg, now: func() time.Time { return time.Now().UTC() }, keyring: cfg.Keyring, policy: &policyHolder{}}
	if s.keyring == nil {
		var key [32]byte
		if _, err := rand.Read(key[:]); err != nil {
			panic(fmt.Errorf("crypto/rand: %w", err))
		}
		s.keyring, _ = secrets.FromBytes(key[:])
	}
	return s
}

// SetChannelValidator installs the notifier registry's config check.
func (s *Service) SetChannelValidator(fn func(kind domain.ChannelKind, cfg []byte) error) {
	s.validateChannel = fn
}

// Keyring returns the instance key, for signed links.
func (s *Service) Keyring() *secrets.Keyring { return s.keyring }

// SetClock replaces the clock, for tests.
func (s *Service) SetClock(now func() time.Time) { s.now = now }

// Now returns the service clock.
func (s *Service) Now() time.Time { return s.now() }

// Config returns the instance settings.
func (s *Service) Config() Config { return s.cfg }

// Bus returns the in-process bus.
func (s *Service) Bus() *engine.Bus { return s.bus }

// DB exposes the store for the auth layer and tests.
func (s *Service) DB() *db.DB { return s.sqlDB }

// inTx returns a service whose every read and write runs on q, so a
// sequence of ordinary calls commits or rolls back together.
func (s *Service) inTx(q *db.Queries) *Service {
	return &Service{
		db: txStore{q}, sqlDB: s.sqlDB, bus: s.bus, log: s.log, cfg: s.cfg, now: s.now, keyring: s.keyring,
		validateChannel: s.validateChannel, notifier: s.notifier, checker: s.checker, checkNow: s.checkNow, metrics: s.metrics,
		policy: s.policy,
	}
}

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

// engineChanged is the bus event for a project-wide change the loops
// should look at, such as a maintenance window ending.
func engineChanged(projectID string) engine.MonitorChanged {
	return engine.MonitorChanged{ProjectID: projectID}
}
