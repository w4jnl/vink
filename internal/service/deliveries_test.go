package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/engine"
	"github.com/w4jnl/vink/internal/notify"
)

// receiver is a webhook sink.
type receiver struct {
	mu     sync.Mutex
	bodies []map[string]any
	fail   bool
	srv    *httptest.Server
}

func newReceiver(t *testing.T) *receiver {
	t.Helper()
	rc := &receiver{}
	rc.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		rc.mu.Lock()
		rc.bodies = append(rc.bodies, m)
		fail := rc.fail
		rc.mu.Unlock()
		if fail {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(200)
	}))
	t.Cleanup(rc.srv.Close)
	return rc
}

func (rc *receiver) count() int {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return len(rc.bodies)
}

func (rc *receiver) last() map[string]any {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if len(rc.bodies) == 0 {
		return nil
	}
	return rc.bodies[len(rc.bodies)-1]
}

func withNotifier(t *testing.T, f *fixture) {
	t.Helper()
	reg, err := notify.NewRegistry(notify.Options{AllowPrivateTargets: true})
	if err != nil {
		t.Fatal(err)
	}
	f.svc.SetNotifier(reg)
}

func TestDownToWebhookThroughDispatcher(t *testing.T) {
	f := newFixture(t)
	withNotifier(t, f)
	rc := newReceiver(t)
	ctx := context.Background()
	ch, err := f.svc.CreateChannel(ctx, f.member, &domain.Channel{Name: "hook", Kind: domain.ChannelWebhook, Config: json.RawMessage(`{"url":"` + rc.srv.URL + `"}`), Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	// the default route came with the first channel; add a repeating one
	if _, err := f.svc.CreateRoute(ctx, f.member, &domain.Route{ChannelIDs: []string{ch.ID}, On: []domain.State{domain.StateDown}, RepeatEvery: 10 * time.Minute}); err != nil {
		t.Fatal(err)
	}
	f.heartbeat(t, "job", "1h", "5m", "prod")
	tgt, _ := f.svc.ResolvePing(ctx, f.project.PingKey, "job", "", false)
	if _, _, err := f.svc.RecordPing(ctx, tgt, PingObservation{Signal: domain.SignalOK}); err != nil {
		t.Fatal(err)
	}
	m, _ := f.svc.MonitorBySlug(ctx, f.member, "job")
	now := f.clock.Add(70 * time.Minute)
	if err := f.svc.Tick(ctx, m.ID, now); err != nil {
		t.Fatal(err)
	}
	disp := engine.NewDispatcher(f.svc, f.svc.log, f.clock.Now)
	sent, failed, err := disp.RunOnce(ctx)
	if err != nil || sent != 2 || failed != 0 {
		t.Fatalf("dispatch: sent=%d failed=%d err=%v", sent, failed, err)
	}
	if rc.count() != 2 {
		t.Fatalf("receiver got %d posts", rc.count())
	}
	body := rc.last()
	if body["event"] != "down" || body["title"] != "[vink] DOWN job (prod)" {
		t.Fatalf("payload: %v", body)
	}
	links := body["links"].(map[string]any)
	if !strings.Contains(links["monitor"].(string), "/o/homelab/p/prod/m/job") || !strings.Contains(links["ack"].(string), "/a/") {
		t.Fatalf("links: %v", links)
	}
	// nothing more to do right away
	if sent, _, _ := disp.RunOnce(ctx); sent != 0 {
		t.Fatal("re-run must not resend")
	}
	// repeat after the interval, only on the repeating route
	f.clock.Add(11 * time.Minute)
	sent, _, _ = disp.RunOnce(ctx)
	if sent != 1 || rc.last()["repeat"] != true || rc.last()["title"] != "[vink] STILL DOWN job (prod)" {
		t.Fatalf("repeat: sent=%d last=%v", sent, rc.last())
	}
	// ack via the signed link silences repeats
	ackURL := links["ack"].(string)
	token := ackURL[strings.LastIndex(ackURL, "/")+1:]
	inc, err := f.svc.AckIncidentByToken(ctx, token)
	if err != nil || inc.AckedAt == nil || inc.AckedBy != "link:ack" {
		t.Fatalf("ack by token: %+v %v", inc, err)
	}
	f.clock.Add(11 * time.Minute)
	if sent, _, _ := disp.RunOnce(ctx); sent != 0 {
		t.Fatal("acked incident must not repeat")
	}
	if _, err := f.svc.AckIncidentByToken(ctx, "bogus"); err == nil {
		t.Fatal("bogus token accepted")
	}
	// recovery: up delivery on the default route only
	f.clock.Add(time.Minute)
	if _, _, err := f.svc.RecordPing(ctx, tgt, PingObservation{Signal: domain.SignalOK}); err != nil {
		t.Fatal(err)
	}
	sent, _, _ = disp.RunOnce(ctx)
	if sent != 1 || rc.last()["event"] != "up" {
		t.Fatalf("up: sent=%d last=%v", sent, rc.last())
	}
	recent, _ := f.svc.RecentDeliveries(ctx, f.member, 10)
	if len(recent) != 4 {
		t.Fatalf("recent deliveries: %d", len(recent))
	}
}

func TestDeliveryFailureBackoffAndDisabledChannel(t *testing.T) {
	f := newFixture(t)
	withNotifier(t, f)
	rc := newReceiver(t)
	rc.fail = true
	ctx := context.Background()
	ch, _ := f.svc.CreateChannel(ctx, f.member, &domain.Channel{Name: "hook", Kind: domain.ChannelWebhook, Config: json.RawMessage(`{"url":"` + rc.srv.URL + `"}`), Enabled: true})
	f.heartbeat(t, "job", "1h", "5m")
	tgt, _ := f.svc.ResolvePing(ctx, f.project.PingKey, "job", "", false)
	_, _, _ = f.svc.RecordPing(ctx, tgt, PingObservation{Signal: domain.SignalFail})
	disp := engine.NewDispatcher(f.svc, f.svc.log, f.clock.Now)
	sent, failed, _ := disp.RunOnce(ctx)
	if sent != 0 || failed != 0 {
		t.Fatalf("first attempt: sent=%d failed=%d", sent, failed)
	}
	due, _ := f.svc.DueDeliveries(ctx, f.clock.Now().Add(31*time.Second), 10)
	if len(due) != 1 || due[0].Attempt != 1 || !strings.Contains(due[0].Error, "503") {
		t.Fatalf("rescheduled row: %+v", due)
	}
	// disabling the channel makes the delivery fail with a clear error
	if _, err := f.svc.UpdateChannel(ctx, f.member, ch.ID, &domain.Channel{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	f.clock.Add(time.Minute)
	_, _, _ = disp.RunOnce(ctx)
	due, _ = f.svc.DueDeliveries(ctx, f.clock.Now().Add(3*time.Minute), 10)
	if len(due) != 1 || !strings.Contains(due[0].Error, "disabled") {
		t.Fatalf("disabled channel: %+v", due)
	}
}

func TestChannelTest(t *testing.T) {
	f := newFixture(t)
	withNotifier(t, f)
	rc := newReceiver(t)
	ctx := context.Background()
	ch, _ := f.svc.CreateChannel(ctx, f.member, &domain.Channel{Name: "hook", Kind: domain.ChannelWebhook, Config: json.RawMessage(`{"url":"` + rc.srv.URL + `"}`), Enabled: true})
	if err := f.svc.TestChannel(ctx, f.member, ch.ID); err != nil {
		t.Fatal(err)
	}
	if rc.last()["event"] != "test" {
		t.Fatalf("test payload: %v", rc.last())
	}
	rc.fail = true
	if err := f.svc.TestChannel(ctx, f.member, ch.ID); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("failing test must surface the error: %v", err)
	}
	if err := f.svc.TestChannel(ctx, f.viewer, ch.ID); err == nil {
		t.Fatal("viewer must not test channels")
	}
	// registry validation is applied on create
	_, err := f.svc.CreateChannel(ctx, f.member, &domain.Channel{Name: "bad", Kind: domain.ChannelWebhook, Config: json.RawMessage(`{"url":"ftp://x"}`), Enabled: true})
	if _, ok := domain.AsValidation(err); !ok {
		t.Fatalf("expected validation error from the registry, got %v", err)
	}
}
