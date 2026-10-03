package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/w4jnl/vink/internal/domain"
)

func TestCreateMonitorDefaultsAndPlan(t *testing.T) {
	f := newFixture(t)
	m := f.heartbeat(t, "nightly-backup", "1h", "5m", "backup", "Prod")
	if m.State != domain.StateNew || m.Paused || m.Name != "nightly-backup" {
		t.Fatalf("created: %+v", m)
	}
	// the wake-up is the deadline plus the default 30 s tolerance
	if m.NextDueAt == nil || !m.NextDueAt.Equal(start.Add(time.Hour+30*time.Second)) || m.Heartbeat.Tolerance != domain.DefaultTolerance {
		t.Fatalf("next due = %v, tolerance %s", m.NextDueAt, m.Heartbeat.Tolerance)
	}
	if len(m.Tags) != 2 || m.Tags[1] != "prod" {
		t.Fatalf("tags = %v", m.Tags)
	}
	if m.Heartbeat.FailureThreshold != 1 || m.Heartbeat.Grace.String() != "5m" {
		t.Fatalf("spec defaults: %+v", m.Heartbeat)
	}
	if f.drainEvents() != 1 {
		t.Error("expected one bus event")
	}
	if f.svc.PingURL(f.project, m.Slug) != "http://localhost:8080/ping/"+f.project.PingKey+"/nightly-backup" {
		t.Errorf("ping url = %s", f.svc.PingURL(f.project, m.Slug))
	}
}

func TestCreateMonitorErrors(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.heartbeat(t, "job", "1h", "5m")
	_, err := f.svc.CreateMonitor(ctx, f.member, &domain.Monitor{Slug: "job", Heartbeat: &domain.HeartbeatSpec{Schedule: domain.Schedule{Period: domain.MustDuration("1h")}}})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate slug: %v", err)
	}
	_, err = f.svc.CreateMonitor(ctx, f.member, &domain.Monitor{Slug: "bad", Heartbeat: &domain.HeartbeatSpec{Schedule: domain.Schedule{Period: domain.MustDuration("10s")}}})
	if _, ok := domain.AsValidation(err); !ok {
		t.Fatalf("short period: %v", err)
	}
	_, err = f.svc.CreateMonitor(ctx, f.viewer, &domain.Monitor{Slug: "v", Heartbeat: &domain.HeartbeatSpec{Schedule: domain.Schedule{Period: domain.MustDuration("1h")}}})
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("viewer: %v", err)
	}
	// quota
	q := int64(1)
	if err := f.svc.DB().Write().SetOrgQuotas(ctx, dbSetQuotas(f.org.ID, &q)); err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.CreateMonitor(ctx, f.member, &domain.Monitor{Slug: "over", Heartbeat: &domain.HeartbeatSpec{Schedule: domain.Schedule{Period: domain.MustDuration("1h")}}})
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("quota: %v", err)
	}
}

func TestListGetUpdateDelete(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.heartbeat(t, "b-job", "1h", "5m", "prod")
	f.heartbeat(t, "a-job", "2h", "10m", "backup", "prod")
	all, err := f.svc.ListMonitors(ctx, f.member, MonitorFilter{})
	if err != nil || len(all) != 2 || all[0].Slug != "a-job" {
		t.Fatalf("list: %v %v", all, err)
	}
	byTag, _ := f.svc.ListMonitors(ctx, f.member, MonitorFilter{Tag: "backup"})
	if len(byTag) != 1 || byTag[0].Slug != "a-job" {
		t.Fatalf("tag filter: %v", byTag)
	}
	byQ, _ := f.svc.ListMonitors(ctx, f.member, MonitorFilter{Query: "B-J"})
	if len(byQ) != 1 || byQ[0].Slug != "b-job" {
		t.Fatalf("query filter: %v", byQ)
	}
	byState, _ := f.svc.ListMonitors(ctx, f.member, MonitorFilter{State: domain.StateDown})
	if len(byState) != 0 {
		t.Fatalf("state filter: %v", byState)
	}
	counts, _ := f.svc.MonitorCounts(ctx, f.member)
	if counts[domain.StateNew] != 2 {
		t.Fatalf("counts: %v", counts)
	}

	// cross-project scope sees nothing
	other := f.member
	other.ProjectID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	if _, err := f.svc.MonitorBySlug(ctx, other, "a-job"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cross-project get: %v", err)
	}
	if l, _ := f.svc.ListMonitors(ctx, other, MonitorFilter{}); len(l) != 0 {
		t.Fatal("cross-project list must be empty")
	}

	// update: name, tags, schedule change moves the deadline
	upd, err := f.svc.UpdateMonitor(ctx, f.member, "a-job", &domain.Monitor{Name: "A job", Tags: []string{"x"},
		Heartbeat: &domain.HeartbeatSpec{Schedule: domain.Schedule{Period: domain.MustDuration("3h")}, Grace: domain.MustDuration("1h")}})
	if err != nil {
		t.Fatal(err)
	}
	if upd.Name != "A job" || upd.Tags[0] != "x" || !upd.NextDueAt.Equal(start.Add(3*time.Hour+30*time.Second)) {
		t.Fatalf("update: %+v next=%v", upd, upd.NextDueAt)
	}
	if _, err := f.svc.UpdateMonitor(ctx, f.member, "a-job", &domain.Monitor{Slug: "renamed", Name: "n"}); err == nil {
		t.Fatal("slug change must be rejected")
	}
	if _, err := f.svc.UpdateMonitor(ctx, f.member, "missing", &domain.Monitor{Name: "n"}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("update missing: %v", err)
	}
	if _, err := f.svc.UpdateMonitor(ctx, f.viewer, "a-job", &domain.Monitor{Name: "n"}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("viewer update: %v", err)
	}

	if err := f.svc.DeleteMonitor(ctx, f.member, "b-job"); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.DeleteMonitor(ctx, f.member, "b-job"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
	if l, _ := f.svc.ListMonitors(ctx, f.member, MonitorFilter{}); len(l) != 1 {
		t.Fatal("delete did not remove")
	}
}

func TestPauseAndResume(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.heartbeat(t, "job", "1h", "5m")
	f.clock.Add(10 * time.Minute)
	p, err := f.svc.PauseMonitor(ctx, f.member, "job")
	if err != nil {
		t.Fatal(err)
	}
	if !p.Paused || p.State != domain.StatePaused || p.NextDueAt != nil {
		t.Fatalf("paused: %+v", p)
	}
	// idempotent
	if again, _ := f.svc.PauseMonitor(ctx, f.member, "job"); !again.Paused {
		t.Fatal("pause twice")
	}
	// paused monitors never tick to late
	f.clock.Add(5 * time.Hour)
	if ids, _ := f.svc.ListDue(ctx, f.clock.Now(), 10); len(ids) != 0 {
		t.Fatalf("paused monitor listed as due: %v", ids)
	}
	resumed := f.clock.Now()
	r, err := f.svc.ResumeMonitor(ctx, f.member, "job")
	if err != nil {
		t.Fatal(err)
	}
	if r.Paused || r.State != domain.StateNew || !r.BaseAt.Equal(resumed) || !r.NextDueAt.Equal(resumed.Add(time.Hour+30*time.Second)) {
		t.Fatalf("resumed: %+v next=%v", r, r.NextDueAt)
	}
	events, _ := f.svc.ListEvents(ctx, f.member, "job", 10)
	if len(events) != 2 || events[0].Reason != "resumed" || events[1].To != domain.StatePaused {
		t.Fatalf("events: %+v", events)
	}
	if _, err := f.svc.PauseMonitor(ctx, f.viewer, "job"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("viewer pause: %v", err)
	}
}
