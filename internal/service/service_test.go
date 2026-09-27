package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/w4jnl/vink/internal/db"
	"github.com/w4jnl/vink/internal/db/dbtest"
	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/engine"
)

// clock is a settable test clock.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time  { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Set(t time.Time) { c.mu.Lock(); c.t = t; c.mu.Unlock() }
func (c *clock) Add(d time.Duration) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
	return c.t
}

type fixture struct {
	svc     *Service
	clock   *clock
	admin   domain.Scope
	org     *domain.Org
	project *domain.Project
	member  domain.Scope
	viewer  domain.Scope
	events  <-chan engine.MonitorChanged
}

var start = time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

func newFixture(t *testing.T) *fixture {
	t.Helper()
	d := dbtest.Open(t)
	bus := engine.NewBus()
	svc := New(d, bus, slog.New(slog.NewTextHandler(io.Discard, nil)), DefaultConfig())
	c := &clock{t: start}
	svc.SetClock(c.Now)
	ctx := context.Background()
	admin := domain.Scope{InstanceAdmin: true, Role: domain.RoleOwner, Actor: "test:admin"}
	org, err := svc.CreateOrg(ctx, admin, "homelab", "Homelab")
	if err != nil {
		t.Fatal(err)
	}
	project, err := svc.CreateProject(ctx, admin, org.ID, "prod", "Production", "Europe/Amsterdam")
	if err != nil {
		t.Fatal(err)
	}
	events, cancel := bus.Subscribe()
	t.Cleanup(cancel)
	return &fixture{
		svc: svc, clock: c, admin: admin, org: org, project: project, events: events,
		member: domain.Scope{OrgID: org.ID, ProjectID: project.ID, UserID: "u1", Role: domain.RoleMember, Actor: "user:j"},
		viewer: domain.Scope{OrgID: org.ID, ProjectID: project.ID, UserID: "u2", Role: domain.RoleViewer, Actor: "user:v"},
	}
}

func (f *fixture) heartbeat(t *testing.T, slug, period, grace string, tags ...string) *domain.Monitor {
	t.Helper()
	m, err := f.svc.CreateMonitor(context.Background(), f.member, &domain.Monitor{
		Slug: slug, Name: slug, Kind: domain.KindHeartbeat, Tags: tags,
		Heartbeat: &domain.HeartbeatSpec{Schedule: domain.Schedule{Period: domain.MustDuration(period)}, Grace: domain.MustDuration(grace)},
	})
	if err != nil {
		t.Fatalf("create %s: %v", slug, err)
	}
	return m
}

// channelAndRoute inserts a webhook channel and a catch-all route directly.
func (f *fixture) channelAndRoute(t *testing.T, on []domain.State, tags ...string) (channelID, routeID string) {
	t.Helper()
	ctx := context.Background()
	now := domain.Millis(f.clock.Now())
	ch, err := f.svc.DB().Write().CreateChannel(ctx, db.CreateChannelParams{
		ID: domain.NewID(), ProjectID: f.project.ID, OrgID: f.org.ID, Name: "hook", Kind: "webhook", Config: `{"url":"http://example.invalid"}`, Enabled: true, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	rt, err := f.svc.DB().Write().CreateRoute(ctx, db.CreateRouteParams{
		ID: domain.NewID(), ProjectID: f.project.ID, MatchTags: tagsJSON(tags), ChannelID: ch.ID, OnStates: statesJSON(on), RepeatEveryS: 0, Priority: 0, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return ch.ID, rt.ID
}

func (f *fixture) drainEvents() int {
	n := 0
	for {
		select {
		case <-f.events:
			n++
		default:
			return n
		}
	}
}

func TestNewPingKey(t *testing.T) {
	k := NewPingKey()
	if len(k) != 22 {
		t.Fatalf("len = %d", len(k))
	}
	for _, r := range k {
		if (r < 'a' || r > 'z') && (r < '2' || r > '7') {
			t.Fatalf("unexpected char %q in %s", r, k)
		}
	}
	if NewPingKey() == k {
		t.Fatal("keys must differ")
	}
}

func TestOrgAndProjectScoping(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.svc.CreateOrg(ctx, f.member, "x", "X"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("member creating org: %v", err)
	}
	if _, err := f.svc.CreateOrg(ctx, f.admin, "homelab", "dup"); err == nil {
		t.Fatal("duplicate org slug must conflict")
	}
	if _, err := f.svc.CreateOrg(ctx, f.admin, "Bad Slug", ""); err == nil {
		t.Fatal("bad slug must fail validation")
	}
	if _, err := f.svc.CreateProject(ctx, f.member, f.org.ID, "two", "", ""); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("member creating project: %v", err)
	}
	orgAdmin := f.member
	orgAdmin.Role = domain.RoleAdmin
	p2, err := f.svc.CreateProject(ctx, orgAdmin, f.org.ID, "two", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if p2.Timezone != "UTC" || p2.Name != "two" || len(p2.PingKey) != 22 {
		t.Errorf("project defaults: %+v", p2)
	}
	if _, err := f.svc.CreateProject(ctx, f.admin, f.org.ID, "two", "", ""); err == nil {
		t.Fatal("duplicate project slug in org must conflict")
	}
	if _, err := f.svc.CreateProject(ctx, f.admin, f.org.ID, "tz", "", "Mars/Base"); err == nil {
		t.Fatal("bad timezone must fail")
	}
	got, err := f.svc.ProjectByPingKey(ctx, f.project.PingKey)
	if err != nil || got.ID != f.project.ID {
		t.Fatalf("by ping key: %v %v", got, err)
	}
	if _, err := f.svc.ProjectByPingKey(ctx, "nope"); err == nil {
		t.Fatal("unknown key must be not found")
	}
	// rotate: old key valid during grace, not after
	rot, err := f.svc.RotatePingKey(ctx, orgAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if rot.PingKey == f.project.PingKey || rot.PingKeyPrev != f.project.PingKey {
		t.Fatalf("rotate: %+v", rot)
	}
	if _, err := f.svc.ProjectByPingKey(ctx, f.project.PingKey); err != nil {
		t.Fatalf("old key inside grace: %v", err)
	}
	f.clock.Add(25 * time.Hour)
	if _, err := f.svc.ProjectByPingKey(ctx, f.project.PingKey); err == nil {
		t.Fatal("old key after grace must fail")
	}
	if _, err := f.svc.RotatePingKey(ctx, f.member); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("member rotating: %v", err)
	}
	list, err := f.svc.ProjectsForUser(ctx, "nobody", true)
	if err != nil || len(list) != 2 {
		t.Fatalf("instance admin projects: %d %v", len(list), err)
	}
}
