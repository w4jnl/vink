package service

import (
	"context"
	"testing"
	"time"

	"github.com/w4jnl/vink/internal/domain"
)

func TestPruneKeepsRecentAndEvents(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.heartbeat(t, "job", "1h", "5m")
	tgt, _ := f.svc.ResolvePing(ctx, f.project.PingKey, "job", "", false)
	old := start.Add(-100 * 24 * time.Hour)
	if _, _, err := f.svc.RecordPing(ctx, tgt, PingObservation{Signal: domain.SignalOK, At: old, Body: []byte("old body")}); err != nil {
		t.Fatal(err)
	}
	mid := start.Add(-20 * 24 * time.Hour)
	if _, _, err := f.svc.RecordPing(ctx, tgt, PingObservation{Signal: domain.SignalOK, At: mid, Body: []byte("mid body")}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.svc.RecordPing(ctx, tgt, PingObservation{Signal: domain.SignalOK, Body: []byte("new body")}); err != nil {
		t.Fatal(err)
	}
	pruned, err := f.svc.Prune(ctx, f.clock.Now(), 90, 14)
	if err != nil {
		t.Fatal(err)
	}
	if pruned.Observations != 1 || pruned.Bodies != 2 {
		t.Fatalf("pruned: %+v", pruned)
	}
	obs, _ := f.svc.ListObservations(ctx, f.member, "job", ObservationPage{Limit: 10})
	if len(obs) != 2 {
		t.Fatalf("observations left: %d", len(obs))
	}
	if _, _, err := f.svc.ObservationBody(ctx, f.member, obs[0].ID); err != nil {
		t.Fatalf("newest body must stay: %v", err)
	}
	if _, _, err := f.svc.ObservationBody(ctx, f.member, obs[1].ID); err == nil {
		t.Fatal("the 20-day-old body must go")
	}
	events, _ := f.svc.ListEvents(ctx, f.member, "job", 10)
	if len(events) == 0 {
		t.Fatal("events are kept")
	}
}
