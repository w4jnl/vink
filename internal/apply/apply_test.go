package apply

import (
	"os"
	"strings"
	"testing"

	"github.com/w4jnl/vink/internal/domain"
)

const sample = `version: 1
project: {slug: homelab, name: Homelab, timezone: Europe/Amsterdam}
channels:
  - {name: mail, kind: smtp, to: [ops@example.com]}
  - {name: ntfy, kind: ntfy, url: https://ntfy.example.com, topic: vink, token: ${NTFY_TOKEN}}
routes:
  - {match_tags: [prod], channels: [mail, ntfy], on: [down, up], repeat_every: 4h}
  - {match_tags: [], channels: [mail], on: [down, up]}
maintenance:
  - {name: weekly patching, match_tags: [prod], rrule: "FREQ=WEEKLY;BYDAY=SU", from: "02:00", to: "04:00"}
monitors:
  - {slug: nightly-backup, kind: heartbeat, schedule: {cron: "0 3 * * *"}, grace: 30m, tags: [backup, prod]}
  - {slug: api-health, kind: http, interval: 30s, http: {url: https://api.example.com/healthz, expect_status: [200, 300-399], expect_body: {jsonpath: {path: $.status, equals: ok}}}}
status_pages:
  - {slug: homelab, title: Homelab status, match_tags: [prod], public: true}
`

func TestExpandParseEncode(t *testing.T) {
	if _, err := Expand([]byte(sample), func(string) (string, bool) { return "", false }); err == nil || !strings.Contains(err.Error(), "NTFY_TOKEN") {
		t.Fatalf("unset variable: %v", err)
	}
	data, err := Expand([]byte(sample), func(k string) (string, bool) { return "tok-" + k, true })
	if err != nil {
		t.Fatal(err)
	}
	f, err := Parse(data, true)
	if err != nil {
		t.Fatal(err)
	}
	if f.Project.Timezone != "Europe/Amsterdam" || len(f.Channels) != 2 || f.Channels[1].Config["token"] != "tok-NTFY_TOKEN" || len(f.Routes) != 2 || f.Routes[0].RepeatEvery.String() != "4h" {
		t.Fatalf("parsed: %+v", f)
	}
	m := f.Monitors[1].ToDomain()
	if m.Kind != domain.KindHTTP || m.Pull == nil || m.Pull.Interval.String() != "30s" || len(m.Pull.HTTP.ExpectStatus) != 2 || m.Pull.HTTP.ExpectStatus[1].Hi != 399 || m.Pull.HTTP.ExpectBody.JSONPath.Equals != "ok" {
		t.Fatalf("http monitor: %+v %+v", m.Pull, m.Pull.HTTP)
	}
	hb := f.Monitors[0].ToDomain()
	if hb.Heartbeat == nil || hb.Heartbeat.Schedule.Cron != "0 3 * * *" || hb.Heartbeat.Grace.String() != "30m" {
		t.Fatalf("heartbeat: %+v", hb.Heartbeat)
	}
	w, err := f.Maintenance[0].ToDomain()
	if err != nil || !w.Weekly || w.From != "02:00" {
		t.Fatalf("maintenance: %+v %v", w, err)
	}
	out, err := Encode(f)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"version: 1", "- {name: mail, kind: smtp, to: [ops@example.com]}", "match_tags: [prod]", "repeat_every: 4h", "rrule: FREQ=WEEKLY;BYDAY=SU", "schedule: {cron: 0 3 * * *}", "grace: 30m", "interval: 30s", "expect_status: [200, 300-399]", "title: Homelab status"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("encoded lacks %q:\n%s", want, out)
		}
	}
	back, err := Parse(out, true)
	if err != nil {
		t.Fatalf("re-parse: %v\n%s", err, out)
	}
	if len(back.Monitors) != 2 || back.Monitors[1].HTTP.URL != "https://api.example.com/healthz" || back.Channels[1].Config["token"] != "tok-NTFY_TOKEN" {
		t.Fatalf("round trip: %+v", back)
	}
}

func TestSchemaCatchesMistakes(t *testing.T) {
	cases := []struct{ name, doc, want string }{
		{"typo", "version: 1\nmonitors:\n  - {slug: x, grace_period: 5m}\n", "grace_period"},
		{"bad slug", "version: 1\nmonitors:\n  - {slug: Bad Slug}\n", "/monitors/0/slug"},
		{"bad duration", "version: 1\nmonitors:\n  - {slug: x, grace: soon}\n", "/monitors/0/grace"},
		{"route without channels", "version: 1\nroutes:\n  - {match_tags: [prod]}\n", "channels"},
		{"bad version", "version: 2\n", "version"},
		{"json too", `{"version": 1, "monitors": [{"slug": "ok"}]}`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse([]byte(c.doc), true)
			if c.want == "" {
				if err != nil {
					t.Fatalf("unexpected: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want %q in error, got %v", c.want, err)
			}
		})
	}
}

func TestMonitorFromLeavesDefaultsOut(t *testing.T) {
	m := &domain.Monitor{Slug: "web", Name: "web", Kind: domain.KindHTTP, Pull: &domain.PullSpec{HTTP: &domain.HTTPCheck{URL: "https://x"}}}
	m.Normalize()
	out := MonitorFrom(m)
	if out.Name != "" || out.Interval != 0 || out.Timeout != 0 || out.Confirm != nil || out.FailureThreshold != 0 || out.HTTP.Method != "" || out.HTTP.ExpectStatus != nil || out.HTTP.VerifyTLS != nil {
		t.Fatalf("defaults must stay out: %+v %+v", out, out.HTTP)
	}
	enc, _ := Encode(&File{Version: 1, Monitors: []Monitor{out}})
	if strings.Contains(string(enc), "interval") || !strings.Contains(string(enc), "url: https://x") {
		t.Fatalf("encoded: %s", enc)
	}
}

func TestSchemaCopyInDocs(t *testing.T) {
	docs, err := os.ReadFile("../../docs/apply-schema.json")
	if err != nil {
		t.Skip("docs copy not present")
	}
	if string(docs) != string(Schema()) {
		t.Fatal("docs/apply-schema.json differs from the embedded schema; run cp internal/apply/apply-schema.json docs/apply-schema.json")
	}
}
