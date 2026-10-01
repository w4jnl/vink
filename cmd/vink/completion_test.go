package main

import (
	"context"
	"strings"
	"testing"

	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/service"
)

func TestCompletion(t *testing.T) {
	e := newCLIEnv(t)
	ctx := context.Background()
	e.monitor("nightly")
	if _, err := e.svc.CreateMonitor(ctx, e.scope, &domain.Monitor{Slug: "api", Name: "API", Kind: domain.KindHeartbeat, Tags: []string{"prod", "web"},
		Heartbeat: &domain.HeartbeatSpec{Schedule: domain.Schedule{Period: domain.MustDuration("1h")}, Grace: domain.MustDuration("5m")}}); err != nil {
		t.Fatal(err)
	}
	tgt, _ := e.svc.ResolvePing(ctx, e.project.PingKey, "api", "", false)
	if _, _, err := e.svc.RecordPing(ctx, tgt, service.PingObservation{Signal: domain.SignalFail}); err != nil {
		t.Fatal(err)
	}

	// the scripts exist for every shell
	for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
		if out, errs, code := e.run("", "completion", shell); code != 0 || len(out) < 1000 {
			t.Fatalf("completion %s: %d %s", shell, code, errs)
		}
	}

	// monitor slugs with their name and state, from the server
	out, errs, code := e.run("", "__complete", "get", "")
	if code != 0 || !strings.Contains(out, "api\tAPI · down\n") || !strings.Contains(out, "nightly\tnightly · new\n") || !strings.HasSuffix(out, ":4\n") {
		t.Fatalf("get: %d %q %s", code, out, errs)
	}
	for _, cmd := range []string{"logs", "pause", "resume", "check", "ping", "run"} {
		if out, _, _ := e.run("", "__complete", cmd, ""); !strings.Contains(out, "nightly\t") {
			t.Errorf("%s completes no monitors: %q", cmd, out)
		}
	}
	// the command after `run <slug> --` is the shell's to complete
	if out, _, _ := e.run("", "__complete", "run", "nightly", ""); strings.Contains(out, "api\t") || !strings.HasSuffix(out, ":0\n") {
		t.Errorf("run's command: %q", out)
	}
	// open incidents, tags, states
	if out, _, _ := e.run("", "__complete", "ack", ""); !strings.Contains(out, "\tapi · since ") {
		t.Errorf("ack: %q", out)
	}
	if out, _, _ := e.run("", "__complete", "ls", "--tag", ""); !strings.Contains(out, "prod\n") || !strings.Contains(out, "web\n") {
		t.Errorf("tags: %q", out)
	}
	if out, _, _ := e.run("", "__complete", "ls", "--state", ""); !strings.Contains(out, "down\n") || !strings.Contains(out, "paused\n") {
		t.Errorf("states: %q", out)
	}
	if out, _, _ := e.run("", "__complete", "admin", "user", "grant", "bob", "--role", ""); !strings.Contains(out, "owner\n") || !strings.Contains(out, "viewer\n") {
		t.Errorf("roles: %q", out)
	}
	if out, _, _ := e.run("", "__complete", "admin", "org", "key", "create", "--access", ""); !strings.Contains(out, "ro\n") || !strings.Contains(out, "rw\n") {
		t.Errorf("access: %q", out)
	}

	// contexts from the config file, also for --context
	e.run("", "ctx", "add", "test", "--server", e.srv.URL, "--key", e.rwKey)
	if out, _, _ := e.run("", "__complete", "ctx", "use", ""); !strings.Contains(out, "test\t"+e.srv.URL+"\n") {
		t.Errorf("contexts: %q", out)
	}
	if out, _, _ := e.run("", "__complete", "ls", "--context", ""); !strings.Contains(out, "test\t") {
		t.Errorf("--context: %q", out)
	}

	// a server that does not answer completes nothing, quietly
	t.Setenv("VINK_SERVER", "http://127.0.0.1:1")
	// (cobra itself reports the directive on stderr; the shells drop it)
	out, errs, code = e.run("", "__complete", "get", "")
	if code != 0 || out != ":4\n" || strings.Contains(errs, "error") || strings.Contains(errs, "refused") {
		t.Fatalf("server down: %d %q %q", code, out, errs)
	}
}
