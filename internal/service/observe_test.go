package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/w4jnl/vink/internal/db"
	"github.com/w4jnl/vink/internal/domain"
)

func dbSetQuotas(orgID string, monitors *int64) db.SetOrgQuotasParams {
	return db.SetOrgQuotasParams{QuotaMonitors: monitors, ID: orgID}
}

func TestResolvePing(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m := f.heartbeat(t, "job", "1h", "5m")
	tgt, err := f.svc.ResolvePing(ctx, f.project.PingKey, "job", "", false)
	if err != nil || tgt.Monitor.ID != m.ID || tgt.Created {
		t.Fatalf("resolve: %+v %v", tgt, err)
	}
	byID, err := f.svc.ResolvePing(ctx, "", "", m.ID, false)
	if err != nil || byID.Monitor.ID != m.ID {
		t.Fatalf("by id: %v", err)
	}
	for name, args := range map[string][3]string{
		"bad key":      {"nope", "job", ""},
		"bad slug":     {f.project.PingKey, "missing", ""},
		"bad id":       {f.project.PingKey, "", "01ARZ3NDEKTSV4RRFFQ69G5FAV"},
		"invalid slug": {f.project.PingKey, "Not A Slug", ""},
	} {
		if _, err := f.svc.ResolvePing(ctx, args[0], args[1], args[2], false); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// create=1 makes a monitor with the instance defaults
	created, err := f.svc.ResolvePing(ctx, f.project.PingKey, "auto-job", "", true)
	if err != nil || !created.Created {
		t.Fatalf("auto-create: %+v %v", created, err)
	}
	if created.Monitor.Heartbeat.Schedule.Period.String() != "1d" || created.Monitor.Heartbeat.Grace.String() != "1h" {
		t.Fatalf("auto-create defaults: %+v", created.Monitor.Heartbeat)
	}
	again, err := f.svc.ResolvePing(ctx, f.project.PingKey, "auto-job", "", true)
	if err != nil || again.Created || again.Monitor.ID != created.Monitor.ID {
		t.Fatalf("second create resolve: %+v %v", again, err)
	}
	if _, err := f.svc.ResolvePing(ctx, "nope", "auto-job", "", true); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("create with bad key: %v", err)
	}
}

func TestHeartbeatLifecycleThroughStore(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.heartbeat(t, "job", "1h", "5m", "prod")
	chID, _ := f.channelAndRoute(t, []domain.State{domain.StateDown, domain.StateUp, domain.StateLate}, "prod")
	f.drainEvents()

	tgt, err := f.svc.ResolvePing(ctx, f.project.PingKey, "job", "", false)
	if err != nil {
		t.Fatal(err)
	}
	// first ok with a body
	obs, d, err := f.svc.RecordPing(ctx, tgt, PingObservation{Signal: domain.SignalOK, Method: "POST", RemoteAddr: "10.0.0.5", UserAgent: "curl", Body: []byte("backup done"), ContentType: "text/plain"})
	if err != nil {
		t.Fatal(err)
	}
	if !d.Changed || d.To != domain.StateUp || !obs.OK || !obs.HasBody {
		t.Fatalf("first ok: %+v %+v", d, obs)
	}
	if f.drainEvents() != 1 {
		t.Error("bus event after ping")
	}
	body, ct, err := f.svc.ObservationBody(ctx, f.member, obs.ID)
	if err != nil || string(body) != "backup done" || ct != "text/plain" {
		t.Fatalf("body: %q %q %v", body, ct, err)
	}
	m, _ := f.svc.MonitorBySlug(ctx, f.member, "job")
	if m.State != domain.StateUp || m.LastOkAt == nil || !m.NextDueAt.Equal(start.Add(time.Hour+30*time.Second)) || m.LastObsAt == nil {
		t.Fatalf("after ok: %+v", m)
	}

	// scheduler: nothing due before the deadline, nor at it (the tolerance)
	if ids, _ := f.svc.ListDue(ctx, f.clock.Add(time.Hour), 10); len(ids) != 0 {
		t.Fatalf("due early: %v", ids)
	}
	next, ok, _ := f.svc.NextDueAt(ctx)
	if !ok || !next.Equal(start.Add(time.Hour+30*time.Second)) {
		t.Fatalf("next due at = %v %v", next, ok)
	}
	// tolerance over: up -> late, late delivery enqueued
	now := f.clock.Add(30 * time.Second)
	ids, _ := f.svc.ListDue(ctx, now, 10)
	if len(ids) != 1 {
		t.Fatalf("due at deadline: %v", ids)
	}
	if err := f.svc.Tick(ctx, ids[0], now); err != nil {
		t.Fatal(err)
	}
	m, _ = f.svc.MonitorBySlug(ctx, f.member, "job")
	if m.State != domain.StateLate || !m.NextDueAt.Equal(start.Add(65*time.Minute)) {
		t.Fatalf("after deadline: state=%s next=%v", m.State, m.NextDueAt)
	}
	// grace: late -> down, incident opened, down delivery enqueued
	now = f.clock.Add(5*time.Minute - 30*time.Second)
	if err := f.svc.Tick(ctx, m.ID, now); err != nil {
		t.Fatal(err)
	}
	m, _ = f.svc.MonitorBySlug(ctx, f.member, "job")
	if m.State != domain.StateDown || m.NextDueAt != nil {
		t.Fatalf("after grace: state=%s next=%v", m.State, m.NextDueAt)
	}
	inc, err := f.svc.DB().Read().GetOpenIncidentForMonitor(ctx, db.GetOpenIncidentForMonitorParams{ProjectID: f.project.ID, MonitorID: m.ID})
	if err != nil {
		t.Fatalf("incident not opened: %v", err)
	}
	// a second tick while down changes nothing and opens no second incident
	if err := f.svc.Tick(ctx, m.ID, f.clock.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	// recovery: down -> up, incident resolved, up delivery enqueued
	f.clock.Add(time.Minute)
	_, d, err = f.svc.RecordPing(ctx, tgt, PingObservation{Signal: domain.SignalExit, ExitCode: ptri(0)})
	if err != nil || !d.Changed || d.To != domain.StateUp || d.Reason != "recovered" {
		t.Fatalf("recovery: %+v %v", d, err)
	}
	resolved, _ := f.svc.DB().Read().GetIncident(ctx, db.GetIncidentParams{ProjectID: f.project.ID, ID: inc.ID})
	if resolved.ResolvedAt == nil || resolved.CloseEventID == nil {
		t.Fatalf("incident not resolved: %+v", resolved)
	}

	events, _ := f.svc.ListEvents(ctx, f.member, "job", 10)
	if len(events) != 4 {
		t.Fatalf("expected 4 events, got %d: %+v", len(events), events)
	}
	want := []domain.State{domain.StateUp, domain.StateDown, domain.StateLate, domain.StateUp}
	for i, e := range events {
		if e.To != want[i] {
			t.Errorf("event %d to=%s want %s", i, e.To, want[i])
		}
	}
	dels, _ := f.svc.DB().Read().ListRecentDeliveries(ctx, db.ListRecentDeliveriesParams{ProjectID: f.project.ID, Limit: 10})
	if len(dels) != 3 {
		t.Fatalf("expected late, down and up deliveries, got %d", len(dels))
	}
	kinds := map[string]bool{}
	for _, dl := range dels {
		kinds[dl.Kind] = true
		if dl.ChannelID != chID || dl.DeliveredAt != nil || dl.Attempt != 0 {
			t.Errorf("delivery row: %+v", dl)
		}
	}
	if !kinds["late"] || !kinds["down"] || !kinds["up"] {
		t.Errorf("delivery kinds: %v", kinds)
	}

	page, err := f.svc.ListObservations(ctx, f.member, "job", HistoryPage{Limit: 10})
	if err != nil || len(page) != 2 || page[0].Signal != domain.SignalExit || page[1].HasBody != true {
		t.Fatalf("observations: %v %v", page, err)
	}
	// cursor: page of 1, then the rest
	first, _ := f.svc.ListObservations(ctx, f.member, "job", HistoryPage{Limit: 1})
	rest, _ := f.svc.ListObservations(ctx, f.member, "job", HistoryPage{Limit: 1, CursorAt: first[0].At, CursorID: first[0].ID})
	if len(first) != 1 || len(rest) != 1 || rest[0].ID == first[0].ID {
		t.Fatalf("cursor paging: %v %v", first, rest)
	}
}

func TestRouteTagsAndUpWithoutIncident(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.heartbeat(t, "job", "1h", "5m", "db")
	f.channelAndRoute(t, []domain.State{domain.StateDown, domain.StateUp}, "prod")
	tgt, _ := f.svc.ResolvePing(ctx, f.project.PingKey, "job", "", false)
	// new -> up: no incident, no up delivery even for a matching route
	if _, _, err := f.svc.RecordPing(ctx, tgt, PingObservation{Signal: domain.SignalOK}); err != nil {
		t.Fatal(err)
	}
	// fail: up -> down, but the route wants tag prod and the monitor has db
	if _, d, err := f.svc.RecordPing(ctx, tgt, PingObservation{Signal: domain.SignalFail}); err != nil || d.To != domain.StateDown {
		t.Fatalf("fail: %+v %v", d, err)
	}
	dels, _ := f.svc.DB().Read().ListRecentDeliveries(ctx, db.ListRecentDeliveriesParams{ProjectID: f.project.ID, Limit: 10})
	if len(dels) != 0 {
		t.Fatalf("non-matching route must not deliver: %+v", dels)
	}
}

func TestStartRunDurationAndTimeout(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m, err := f.svc.CreateMonitor(ctx, f.member, &domain.Monitor{Slug: "long", Kind: domain.KindHeartbeat,
		Heartbeat: &domain.HeartbeatSpec{Schedule: domain.Schedule{Period: domain.MustDuration("1h")}, Grace: domain.MustDuration("5m"), MaxRuntime: domain.MustDuration("10m")}})
	if err != nil {
		t.Fatal(err)
	}
	tgt, _ := f.svc.ResolvePing(ctx, f.project.PingKey, "long", "", false)
	if _, _, err := f.svc.RecordPing(ctx, tgt, PingObservation{Signal: domain.SignalOK}); err != nil {
		t.Fatal(err)
	}
	f.clock.Add(time.Minute)
	if _, _, err := f.svc.RecordPing(ctx, tgt, PingObservation{Signal: domain.SignalStart, RunID: "r1"}); err != nil {
		t.Fatal(err)
	}
	got, _ := f.svc.MonitorBySlug(ctx, f.member, "long")
	if got.RunStartedAt == nil || !got.NextDueAt.Equal(f.clock.Now().Add(10*time.Minute)) {
		t.Fatalf("after start: run=%v next=%v", got.RunStartedAt, got.NextDueAt)
	}
	f.clock.Add(4 * time.Minute)
	obs, _, err := f.svc.RecordPing(ctx, tgt, PingObservation{Signal: domain.SignalOK, RunID: "r1"})
	if err != nil || obs.DurationMs == nil || *obs.DurationMs != (4*time.Minute).Milliseconds() {
		t.Fatalf("duration: %+v %v", obs, err)
	}
	// a start that never finishes times out into a synthetic fail
	f.clock.Add(time.Minute)
	if _, _, err := f.svc.RecordPing(ctx, tgt, PingObservation{Signal: domain.SignalStart}); err != nil {
		t.Fatal(err)
	}
	now := f.clock.Add(10 * time.Minute)
	ids, _ := f.svc.ListDue(ctx, now, 10)
	if len(ids) != 1 || ids[0] != m.ID {
		t.Fatalf("due for run timeout: %v", ids)
	}
	if err := f.svc.Tick(ctx, m.ID, now); err != nil {
		t.Fatal(err)
	}
	got, _ = f.svc.MonitorBySlug(ctx, f.member, "long")
	if got.State != domain.StateDown {
		t.Fatalf("after run timeout: %s", got.State)
	}
	page, _ := f.svc.ListObservations(ctx, f.member, "long", HistoryPage{Limit: 1})
	if len(page) != 1 || page[0].Source != "local" || page[0].Detail["reason"] != "run_timeout" {
		t.Fatalf("synthetic observation: %+v", page)
	}
}

func TestPausedMonitorStoresPingsWithoutFlipping(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.heartbeat(t, "job", "1h", "5m")
	if _, err := f.svc.PauseMonitor(ctx, f.member, "job"); err != nil {
		t.Fatal(err)
	}
	tgt, _ := f.svc.ResolvePing(ctx, f.project.PingKey, "job", "", false)
	_, d, err := f.svc.RecordPing(ctx, tgt, PingObservation{Signal: domain.SignalOK})
	if err != nil || d.Changed {
		t.Fatalf("paused ping: %+v %v", d, err)
	}
	page, _ := f.svc.ListObservations(ctx, f.member, "job", HistoryPage{})
	if len(page) != 1 {
		t.Fatal("observation must be stored while paused")
	}
}

func TestTickOnDeletedMonitorIsNoop(t *testing.T) {
	f := newFixture(t)
	if err := f.svc.Tick(context.Background(), "01ARZ3NDEKTSV4RRFFQ69G5FAV", f.clock.Now()); err != nil {
		t.Fatal(err)
	}
}
