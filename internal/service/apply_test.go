package service

import (
	"context"
	"strings"
	"testing"

	"github.com/w4jnl/vink/internal/apply"
	"github.com/w4jnl/vink/internal/domain"
)

const applySample = `version: 1
project: {slug: prod, name: Production, timezone: Europe/Amsterdam}
channels:
  - {name: mail, kind: smtp, to: [ops@example.com]}
  - {name: hook, kind: webhook, url: https://hooks.example.com/x, headers: {X-Token: abc}}
routes:
  - {match_tags: [prod], channels: [mail, hook], on: [down, up], repeat_every: 4h}
  - {match_tags: [], channels: [mail], on: [down, up]}
maintenance:
  - {name: weekly patching, match_tags: [prod], rrule: "FREQ=WEEKLY;BYDAY=SU", from: "02:00", to: "04:00"}
monitors:
  - {slug: nightly-backup, kind: heartbeat, schedule: {cron: "0 3 * * *"}, grace: 30m, tags: [backup, prod]}
  - {slug: api-health, name: Public API, kind: http, interval: 30s, tags: [prod], http: {url: https://api.example.com/healthz, expect_body: {jsonpath: {path: $.status, equals: ok}}}}
status_pages:
  - {slug: homelab, title: Homelab status, match_tags: [prod], public: true}
`

func parseSample(t *testing.T, text string) *apply.File {
	t.Helper()
	f, err := apply.Parse([]byte(text), true)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestApplyCreatesUpdatesPrunesAndRoundTrips(t *testing.T) {
	f := newFixture(t)
	withChecker(t, f)
	ctx := context.Background()
	file := parseSample(t, applySample)
	diff, err := f.svc.Apply(ctx, f.member, file, ApplyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(diff.Created) != 7 || len(diff.Updated) != 0 || len(diff.Unchanged) != 2 || diff.Unchanged[1] != "route * → mail" {
		// the first channel brings a default route, which the file's catch-all route already matches
		t.Fatalf("first apply: %+v", diff)
	}
	// the same file again changes nothing
	diff, err = f.svc.Apply(ctx, f.member, file, ApplyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if diff.Changes() != 0 || len(diff.Unchanged) != 9 {
		t.Fatalf("second apply: %+v", diff)
	}
	// export round-trips to an empty diff, with secrets redacted and kept
	exported, err := f.svc.Export(ctx, f.member, false)
	if err != nil {
		t.Fatal(err)
	}
	text, err := apply.Encode(exported)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(text), "headers: '***'") && !strings.Contains(string(text), "headers: \"***\"") && !strings.Contains(string(text), "headers: '***'") {
		if !strings.Contains(string(text), "***") {
			t.Fatalf("export must redact secrets:\n%s", text)
		}
	}
	back := parseSample(t, string(text))
	diff, err = f.svc.Apply(ctx, f.member, back, ApplyOptions{Prune: true})
	if err != nil {
		t.Fatalf("apply export: %v\n%s", err, text)
	}
	if diff.Changes() != 0 {
		t.Fatalf("export round trip: %+v\n%s", diff, text)
	}
	if ch, _ := f.svc.ListChannels(ctx, f.member); !strings.Contains(string(ch[0].Config)+string(ch[1].Config), "abc") {
		t.Fatal("the redacted secret must be kept on apply")
	}
	if _, err := f.svc.Export(ctx, f.viewer, true); err == nil {
		t.Fatal("secrets need edit rights")
	}
	withSecrets, _ := f.svc.Export(ctx, f.member, true)
	if text, _ := apply.Encode(withSecrets); !strings.Contains(string(text), "abc") {
		t.Fatal("export with secrets must include them")
	}
	// a kind change recreates, a dry run changes nothing, prune deletes
	changed := strings.Replace(applySample, "kind: http, interval: 30s, tags: [prod], http: {url: https://api.example.com/healthz, expect_body: {jsonpath: {path: $.status, equals: ok}}}", "kind: tcp, tags: [prod], tcp: {host: api.example.com, port: 443}", 1)
	changed = strings.Replace(changed, "  - {slug: nightly-backup, kind: heartbeat, schedule: {cron: \"0 3 * * *\"}, grace: 30m, tags: [backup, prod]}\n", "", 1)
	dry, err := f.svc.Apply(ctx, f.member, parseSample(t, changed), ApplyOptions{DryRun: true, Prune: true})
	if err != nil {
		t.Fatal(err)
	}
	if !dry.DryRun || len(dry.Recreated) != 1 || dry.Recreated[0] != "monitor api-health" || len(dry.Deleted) != 1 || dry.Deleted[0] != "monitor nightly-backup" {
		t.Fatalf("dry run: %+v", dry)
	}
	if m, err := f.svc.MonitorBySlug(ctx, f.member, "api-health"); err != nil || m.Kind != domain.KindHTTP {
		t.Fatalf("dry run must not change anything: %v %+v", err, m)
	}
	if _, err := f.svc.MonitorBySlug(ctx, f.member, "nightly-backup"); err != nil {
		t.Fatal("dry run must not prune")
	}
	real, err := f.svc.Apply(ctx, f.member, parseSample(t, changed), ApplyOptions{Prune: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(real.Recreated) != 1 || len(real.Deleted) != 1 {
		t.Fatalf("real apply: %+v", real)
	}
	if m, _ := f.svc.MonitorBySlug(ctx, f.member, "api-health"); m.Kind != domain.KindTCP {
		t.Fatalf("recreated: %+v", m)
	}
	if _, err := f.svc.MonitorBySlug(ctx, f.member, "nightly-backup"); err == nil {
		t.Fatal("pruned monitor still exists")
	}
	// mistakes come back as field errors with the list index, and nothing is applied
	bad := strings.Replace(applySample, "channels: [mail, hook]", "channels: [mail, nope]", 1)
	if _, err := f.svc.Apply(ctx, f.member, parseSample(t, bad), ApplyOptions{}); err == nil || !strings.Contains(err.Error(), "routes[0].channels") {
		t.Fatalf("unknown channel: %v", err)
	}
	if _, err := f.svc.Apply(ctx, f.viewer, file, ApplyOptions{}); err == nil {
		t.Fatal("viewers cannot apply")
	}
	wrong := strings.Replace(applySample, "slug: prod, ", "slug: other, ", 1)
	if _, err := f.svc.Apply(ctx, f.member, parseSample(t, wrong), ApplyOptions{}); err == nil || !strings.Contains(err.Error(), "project.slug") {
		t.Fatalf("project slug mismatch: %v", err)
	}
}
