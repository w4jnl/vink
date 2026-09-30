package domain

import "testing"

func TestMonitorYAML(t *testing.T) {
	m := &Monitor{Slug: "nightly-backup", Name: "Nightly backup", Kind: KindHeartbeat, Tags: []string{"backup", "prod"},
		Heartbeat: &HeartbeatSpec{Schedule: Schedule{Cron: "0 3 * * *"}, Timezone: "Europe/Amsterdam", Grace: MustDuration("30m"), FailureThreshold: 1, RecoveryThreshold: 1}}
	want := "slug: nightly-backup\nname: Nightly backup\nkind: heartbeat\ntags: [backup, prod]\nschedule: {cron: \"0 3 * * *\"}\ntimezone: Europe/Amsterdam\ngrace: 30m"
	if got := MonitorYAML(m); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	m2 := &Monitor{Slug: "x", Name: "x", Kind: KindHeartbeat, Heartbeat: &HeartbeatSpec{Schedule: Schedule{Period: MustDuration("1h")}, Grace: DefaultGrace, MaxRuntime: MustDuration("2h"), FailureThreshold: 2, RecoveryThreshold: 1, Methods: []string{"POST"}, BodyLimit: 1024}}
	want2 := "slug: x\nkind: heartbeat\nschedule: {period: 1h}\nmax_runtime: 2h\nfailure_threshold: 2\nmethods: [POST]\nbody_limit: 1024"
	if got := MonitorYAML(m2); got != want2 {
		t.Errorf("got:\n%s\nwant:\n%s", got, want2)
	}
	for in, want := range map[string]string{"plain": "plain", "": `""`, "a: b": `"a: b"`, "true": `"true"`, "12": `"12"`, "-x": `"-x"`, "say \"hi\"": `"say \"hi\""`, "ok now": "ok now"} {
		if got := yamlScalar(in); got != want {
			t.Errorf("yamlScalar(%q) = %s, want %s", in, got, want)
		}
	}
}
