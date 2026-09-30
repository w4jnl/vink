package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/w4jnl/vink/internal/domain"
)

func TestMaintenanceHoldsMonitorsAndSilencesAlerts(t *testing.T) {
	f := newFixture(t)
	withChecker(t, f)
	ctx := context.Background()
	// the clock starts Sunday 2026-09-27 12:00 UTC, 14:00 in Amsterdam
	w, err := f.svc.CreateMaintenance(ctx, f.member, &domain.Maintenance{Name: "weekly patching", MatchTags: []string{"prod"}, Weekly: true, Days: []time.Weekday{time.Sunday}, From: "13:00", To: "17:00"})
	if err != nil {
		t.Fatal(err)
	}
	if w.Timezone != "Europe/Amsterdam" || w.RRule() != "FREQ=WEEKLY;BYDAY=SU" {
		t.Fatalf("created: %+v", w)
	}
	if active, _ := f.svc.ActiveMaintenance(ctx, f.member, f.clock.Now()); len(active) != 1 {
		t.Fatalf("active windows: %d", len(active))
	}
	f.channelAndRoute(t, []domain.State{domain.StateDown, domain.StateUp})
	f.heartbeat(t, "job", "1h", "1m", "prod")
	f.heartbeat(t, "other", "1h", "1m", "lab")

	// deadline and grace pass inside the window: held at late, looked at again when it ends
	f.clock.Set(start.Add(62 * time.Minute))
	for _, slug := range []string{"job", "other"} {
		m, _ := f.svc.MonitorBySlug(ctx, f.member, slug)
		if err := f.svc.Tick(ctx, m.ID, f.clock.Now()); err != nil {
			t.Fatal(err)
		}
	}
	job, _ := f.svc.MonitorBySlug(ctx, f.member, "job")
	other, _ := f.svc.MonitorBySlug(ctx, f.member, "other")
	windowEnd := time.Date(2026, 9, 27, 15, 0, 0, 0, time.UTC)
	if job.State != domain.StateLate || job.NextDueAt == nil || !job.NextDueAt.Equal(windowEnd) {
		t.Fatalf("held monitor: %s next=%v", job.State, job.NextDueAt)
	}
	if other.State != domain.StateDown {
		t.Fatalf("uncovered monitor: %s", other.State)
	}
	events, _ := f.svc.ListEvents(ctx, f.member, "job", 5)
	if events[0].To != domain.StateLate || !strings.Contains(events[0].Reason, "(maintenance)") {
		t.Fatalf("held event: %+v", events[0])
	}
	if open, _ := f.svc.ListIncidents(ctx, f.member, true, 10, time.Time{}); len(open) != 1 || open[0].MonitorSlug != "other" {
		t.Fatalf("incidents: %+v", open)
	}
	// the window ends: the next tick takes it down
	f.clock.Set(windowEnd.Add(time.Second))
	if err := f.svc.Tick(ctx, job.ID, f.clock.Now()); err != nil {
		t.Fatal(err)
	}
	job, _ = f.svc.MonitorBySlug(ctx, f.member, "job")
	if job.State != domain.StateDown {
		t.Fatalf("after the window: %s", job.State)
	}
	if open, _ := f.svc.ListIncidents(ctx, f.member, true, 10, time.Time{}); len(open) != 2 {
		t.Fatalf("incidents after: %d", len(open))
	}
}

func TestMaintenanceEndNowAndPullChecks(t *testing.T) {
	f := newFixture(t)
	withChecker(t, f)
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) }))
	defer srv.Close()
	w, err := f.svc.CreateMaintenance(ctx, f.member, &domain.Maintenance{Name: "weekly patching", MatchTags: []string{"prod"}, Weekly: true, Days: []time.Weekday{time.Sunday}, From: "13:00", To: "17:00"})
	if err != nil {
		t.Fatal(err)
	}
	m, err := f.svc.CreateMonitor(ctx, f.member, &domain.Monitor{Slug: "web", Kind: domain.KindHTTP, Tags: []string{"prod"}, Pull: &domain.PullSpec{FailureThreshold: 1, Confirm: domain.Confirm{Retries: 0, Delay: domain.MustDuration("1s")}, HTTP: &domain.HTTPCheck{URL: srv.URL}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.RunCheck(ctx, m.ID, f.clock.Now()); err != nil {
		t.Fatal(err)
	}
	m, _ = f.svc.MonitorBySlug(ctx, f.member, "web")
	if m.State != domain.StateLate {
		t.Fatalf("covered pull monitor: %s", m.State)
	}
	if open, _ := f.svc.ListIncidents(ctx, f.member, true, 10, time.Time{}); len(open) != 0 {
		t.Fatal("no incident during maintenance")
	}
	if _, err := f.svc.EndMaintenance(ctx, f.viewer, w.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("viewer end now: %v", err)
	}
	ended, err := f.svc.EndMaintenance(ctx, f.member, w.ID)
	if err != nil || ended.EndedUntil == nil {
		t.Fatalf("end now: %v %+v", err, ended)
	}
	if active, _ := f.svc.ActiveMaintenance(ctx, f.member, f.clock.Now()); len(active) != 0 {
		t.Fatal("still active after end now")
	}
	if _, err := f.svc.EndMaintenance(ctx, f.member, w.ID); err == nil || !strings.Contains(err.Error(), "not active") {
		t.Fatalf("end an inactive window: %v", err)
	}
	f.clock.Set(f.clock.Now().Add(time.Minute))
	if err := f.svc.RunCheck(ctx, m.ID, f.clock.Now()); err != nil {
		t.Fatal(err)
	}
	m, _ = f.svc.MonitorBySlug(ctx, f.member, "web")
	if m.State != domain.StateDown {
		t.Fatalf("after end now: %s", m.State)
	}
	// next week's occurrence is still on the calendar, after a once window that starts sooner
	soon := f.clock.Now().Add(2 * time.Hour)
	later := soon.Add(time.Hour)
	if _, err := f.svc.CreateMaintenance(ctx, f.member, &domain.Maintenance{Name: "disk swap", StartsAt: &soon, EndsAt: &later}); err != nil {
		t.Fatal(err)
	}
	list, _ := f.svc.ListMaintenance(ctx, f.member)
	if len(list) != 2 || list[0].Name != "disk swap" || list[1].Name != "weekly patching" {
		t.Fatalf("order: %v %v", list[0].Name, list[1].Name)
	}
	if _, err := f.svc.UpdateMaintenance(ctx, f.member, w.ID, &domain.Maintenance{Name: "patching", Weekly: true, Days: []time.Weekday{time.Saturday}, From: "01:00", To: "03:00"}); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.DeleteMaintenance(ctx, f.member, w.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Maintenance(ctx, f.member, w.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("deleted: %v", err)
	}
}
