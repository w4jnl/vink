package main

import (
	"context"
	"strings"
	"testing"

	"github.com/w4jnl/vink/internal/domain"
)

// TestCLIPingCreateSettings: vink ping and vink run make the monitor set
// up by their flags, say when it existed already, and refuse settings
// without --create.
func TestCLIPingCreateSettings(t *testing.T) {
	e := newCLIEnv(t)
	t.Setenv("VINK_PING_KEY", e.project.PingKey)
	t.Setenv("VINK_PING_URL", e.srv.URL+"/ping/")
	ctx := context.Background()

	out, errs, code := e.run("", "ping", "nightly", "--create", "--name", "Nightly backup", "--cron", "0 3 * * *", "--tz", "Europe/Amsterdam",
		"--grace", "30m", "--tolerance", "1m", "--max-runtime", "2h", "--tag", "backup", "--tag", "prod")
	if code != 0 || out != "ok\n" || errs != "" {
		t.Fatalf("create: %d %q %q", code, out, errs)
	}
	m, err := e.svc.MonitorBySlug(ctx, e.scope, "nightly")
	if err != nil {
		t.Fatal(err)
	}
	hb := m.Heartbeat
	if m.Name != "Nightly backup" || hb.Schedule.Cron != "0 3 * * *" || hb.Timezone != "Europe/Amsterdam" || hb.Grace != domain.MustDuration("30m") ||
		hb.Tolerance != domain.MustDuration("1m") || hb.MaxRuntime != domain.MustDuration("2h") || strings.Join(m.Tags, ",") != "backup,prod" {
		t.Fatalf("monitor %+v %+v", m, hb)
	}

	// a second ping finds it, says so, and changes nothing
	out, errs, code = e.run("", "ping", "nightly", "--create", "--grace", "1d")
	if code != 0 || out != "ok\n" || !strings.Contains(errs, "monitor nightly exists; the create settings were not applied") {
		t.Fatalf("existing: %d %q %q", code, out, errs)
	}
	if m, _ := e.svc.MonitorBySlug(ctx, e.scope, "nightly"); m.Heartbeat.Grace != domain.MustDuration("30m") {
		t.Fatalf("grace changed to %s", m.Heartbeat.Grace)
	}
	// plain --create on an existing monitor says nothing
	if _, errs, _ := e.run("", "ping", "nightly", "--create"); errs != "" {
		t.Errorf("plain create on an existing monitor: %q", errs)
	}

	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"ping", "other", "--grace", "30m"}, "add --create"},
		{[]string{"ping", "other", "--create", "--grace", "soon"}, `--grace: "soon" is not a duration`},
		{[]string{"ping", "other", "--create", "--period", "1h", "--cron", "0 3 * * *"}, "none of the others can be"},
		{[]string{"ping", "other", "--create", "--grace", "5m", "--tolerance", "10m"}, "ping rejected (400): create: tolerance"},
	} {
		if _, errs, code := e.run("", tc.args...); code == 0 || !strings.Contains(errs, tc.want) {
			t.Errorf("%v: %d %q", tc.args, code, errs)
		}
	}
	if _, err := e.svc.MonitorBySlug(ctx, e.scope, "other"); err == nil {
		t.Error("a refused create made the monitor")
	}

	// vink run creates from its start ping
	if _, errs, code := e.run("", "run", "hourly", "--create", "--period", "1h", "--grace", "10m", "--tag", "cron", "--", "true"); code != 0 {
		t.Fatalf("run: %d %q", code, errs)
	}
	m, err = e.svc.MonitorBySlug(ctx, e.scope, "hourly")
	if err != nil || m.Heartbeat.Schedule.Period != domain.MustDuration("1h") || m.Heartbeat.Grace != domain.MustDuration("10m") || strings.Join(m.Tags, ",") != "cron" {
		t.Fatalf("run monitor: %+v %v", m, err)
	}
	if _, errs, code := e.run("", "run", "hourly", "--create", "--grace", "2h", "--", "true"); code != 0 || !strings.Contains(errs, "monitor hourly exists") {
		t.Fatalf("run existing: %d %q", code, errs)
	}
}
