package service

import (
	"context"
	"testing"
	"time"

	"github.com/w4jnl/vink/internal/domain"
)

// timeline seeds a heartbeat with a run, a failure, a recovery and a
// run timeout, so every kind of row and both tables have something.
func timeline(t *testing.T, f *fixture) {
	t.Helper()
	ctx := context.Background()
	if _, err := f.svc.CreateMonitor(ctx, f.member, &domain.Monitor{Slug: "job", Kind: domain.KindHeartbeat, Tags: []string{"prod"},
		Heartbeat: &domain.HeartbeatSpec{Schedule: domain.Schedule{Period: domain.MustDuration("1h")}, Grace: domain.MustDuration("5m"), MaxRuntime: domain.MustDuration("10m")}}); err != nil {
		t.Fatal(err)
	}
	tgt, _ := f.svc.ResolvePing(ctx, f.project.PingKey, "job", "", false)
	ping := func(o PingObservation) {
		t.Helper()
		f.clock.Add(time.Minute)
		if _, _, err := f.svc.RecordPing(ctx, tgt, o); err != nil {
			t.Fatal(err)
		}
	}
	one := int64(1)
	ping(PingObservation{Signal: domain.SignalOK})                                // new → up
	ping(PingObservation{Signal: domain.SignalStart, RunID: "r1"})                // run
	ping(PingObservation{Signal: domain.SignalLog, RunID: "r1", Msg: "step 1"})   // run
	ping(PingObservation{Signal: domain.SignalExit, ExitCode: &one, RunID: "r1"}) // up → down
	ping(PingObservation{Signal: domain.SignalOK})                                // down → up
	ping(PingObservation{Signal: domain.SignalFail, Msg: "disk full"})            // up → down
	ping(PingObservation{Signal: domain.SignalOK})                                // down → up
	ping(PingObservation{Signal: domain.SignalStart})                             // a start that never finishes
	m, _ := f.svc.MonitorBySlug(ctx, f.member, "job")
	if err := f.svc.Tick(ctx, m.ID, f.clock.Add(10*time.Minute)); err != nil { // run timeout: synthetic fail, up → down
		t.Fatal(err)
	}
}

func kinds(items []HistoryItem) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		switch {
		case it.Event != nil:
			out = append(out, "event:"+string(it.Event.From)+"→"+string(it.Event.To))
		case it.Obs.Signal == domain.SignalExit:
			out = append(out, "obs:exit")
		default:
			out = append(out, "obs:"+string(it.Obs.Signal))
		}
	}
	return out
}

func TestHistoryMergesObservationsAndEventsNewestFirst(t *testing.T) {
	f := newFixture(t)
	timeline(t, f)
	ctx := context.Background()
	res, err := f.svc.History(ctx, f.member, "job", HistoryPage{})
	if err != nil {
		t.Fatal(err)
	}
	// 9 observations (8 pings + the synthetic fail) and 6 events
	if len(res.Items) != 15 || res.More {
		t.Fatalf("items=%d more=%v: %v", len(res.Items), res.More, kinds(res.Items))
	}
	want := []string{
		"event:up→down", "obs:fail", // run timeout: the event sits above the observation it came from
		"obs:start",
		"event:down→up", "obs:ok",
		"event:up→down", "obs:fail",
		"event:down→up", "obs:ok",
		"event:up→down", "obs:exit",
		"obs:log", "obs:start",
		"event:new→up", "obs:ok",
	}
	got := kinds(res.Items)
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row %d = %s, want %s\nall: %v", i, got[i], want[i], got)
		}
	}
	for i := 1; i < len(res.Items); i++ {
		a, b := res.Items[i-1], res.Items[i]
		if b.At.After(a.At) || (b.At.Equal(a.At) && b.ID > a.ID) {
			t.Fatalf("rows %d and %d out of order: %v %s / %v %s", i-1, i, a.At, a.ID, b.At, b.ID)
		}
	}
	if res.Items[0].Event.Reason != "run timeout" || res.Items[1].Obs.Detail["reason"] != "run_timeout" {
		t.Fatalf("run timeout rows: %+v %+v", res.Items[0].Event, res.Items[1].Obs.Detail)
	}
	// the viewer sees the same; the other tenant sees nothing
	if _, err := f.svc.History(ctx, f.viewer, "job", HistoryPage{}); err != nil {
		t.Fatalf("viewer: %v", err)
	}
	other := domain.Scope{OrgID: f.org.ID, ProjectID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", Role: domain.RoleMember}
	if _, err := f.svc.History(ctx, other, "job", HistoryPage{}); err == nil {
		t.Fatal("another project's monitor must be not found")
	}
}

func TestHistoryKinds(t *testing.T) {
	f := newFixture(t)
	timeline(t, f)
	ctx := context.Background()
	cases := []struct {
		kind string
		want []string
	}{
		{KindOK, []string{"obs:ok", "obs:ok", "obs:ok"}},
		{KindFail, []string{"obs:fail", "obs:fail", "obs:exit"}},
		{KindRun, []string{"obs:start", "obs:log", "obs:start"}},
		{KindChange, []string{"event:up→down", "event:down→up", "event:up→down", "event:down→up", "event:up→down", "event:new→up"}},
	}
	for _, c := range cases {
		t.Run(c.kind, func(t *testing.T) {
			res, err := f.svc.History(ctx, f.member, "job", HistoryPage{Kind: c.kind})
			if err != nil {
				t.Fatal(err)
			}
			got := kinds(res.Items)
			if len(got) != len(c.want) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
			for i := range c.want {
				if got[i] != c.want[i] {
					t.Fatalf("row %d = %s, want %s (all %v)", i, got[i], c.want[i], got)
				}
			}
		})
	}
	if _, err := f.svc.History(ctx, f.member, "job", HistoryPage{Kind: "weird"}); err == nil {
		t.Fatal("an unknown kind must be refused")
	}
	// the observation and event pages take the same kind and window
	obs, _ := f.svc.ListObservations(ctx, f.member, "job", HistoryPage{Kind: KindFail})
	if len(obs) != 3 {
		t.Fatalf("ListObservations fail: %d", len(obs))
	}
	events, _ := f.svc.ListEventsPage(ctx, f.member, "job", HistoryPage{Limit: 2})
	if len(events) != 2 || events[0].Reason != "run timeout" {
		t.Fatalf("ListEventsPage: %+v", events)
	}
}

func TestHistoryPagesWithOneCursorOverBothTables(t *testing.T) {
	f := newFixture(t)
	timeline(t, f)
	ctx := context.Background()
	all, _ := f.svc.History(ctx, f.member, "job", HistoryPage{Limit: 200})
	var walked []HistoryItem
	p := HistoryPage{Limit: 2}
	for pages := 0; ; pages++ {
		res, err := f.svc.History(ctx, f.member, "job", p)
		if err != nil {
			t.Fatal(err)
		}
		walked = append(walked, res.Items...)
		if !res.More {
			if len(res.Items) == 0 && pages > 0 {
				t.Fatal("a page before the end must not be empty")
			}
			break
		}
		if len(res.Items) != 2 {
			t.Fatalf("a page with more must be full: %d", len(res.Items))
		}
		p.CursorAt, p.CursorID = res.NextAt, res.NextID
		if pages > 20 {
			t.Fatal("paging does not end")
		}
	}
	if len(walked) != len(all.Items) {
		t.Fatalf("walked %d rows, want %d", len(walked), len(all.Items))
	}
	for i := range all.Items {
		if walked[i].ID != all.Items[i].ID {
			t.Fatalf("row %d: walked %s, want %s", i, walked[i].ID, all.Items[i].ID)
		}
	}
	// the cursor also pages a single kind and a window
	first, _ := f.svc.History(ctx, f.member, "job", HistoryPage{Kind: KindChange, Limit: 4})
	rest, _ := f.svc.History(ctx, f.member, "job", HistoryPage{Kind: KindChange, Limit: 4, CursorAt: first.NextAt, CursorID: first.NextID})
	if !first.More || len(first.Items) != 4 || rest.More || len(rest.Items) != 2 {
		t.Fatalf("change pages: %d/%v then %d/%v", len(first.Items), first.More, len(rest.Items), rest.More)
	}
}

func TestHistoryWindowAndCap(t *testing.T) {
	f := newFixture(t)
	timeline(t, f)
	ctx := context.Background()
	// the first ping was at start+1m; a window from start+6m excludes the first five rows' minute
	since := start.Add(6 * time.Minute)
	res, _ := f.svc.History(ctx, f.member, "job", HistoryPage{Since: since})
	for _, it := range res.Items {
		if it.At.Before(since) {
			t.Fatalf("row before since: %v", it.At)
		}
	}
	all, _ := f.svc.History(ctx, f.member, "job", HistoryPage{})
	if len(res.Items) >= len(all.Items) || len(res.Items) == 0 {
		t.Fatalf("since: %d of %d rows", len(res.Items), len(all.Items))
	}
	until := start.Add(3 * time.Minute)
	res, _ = f.svc.History(ctx, f.member, "job", HistoryPage{Until: until})
	if len(res.Items) == 0 {
		t.Fatal("until: no rows")
	}
	for _, it := range res.Items {
		if it.At.After(until) {
			t.Fatalf("row after until: %v", it.At)
		}
	}
	if _, _, _, _, limit := (HistoryPage{Limit: 1000}).bounds(); limit != 200 {
		t.Fatalf("cap = %d", limit)
	}
	if _, _, _, _, limit := (HistoryPage{}).bounds(); limit != 50 {
		t.Fatalf("default = %d", limit)
	}
}
