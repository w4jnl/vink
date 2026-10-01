package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersionCommand(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run(context.Background(), []string{"version"}, nil, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "vink dev") || !strings.Contains(out.String(), "heartbeat and uptime monitor") {
		t.Errorf("lockup missing: %q", out.String())
	}
}

func TestVersionJSON(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run(context.Background(), []string{"version", "--json"}, nil, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	var m map[string]string
	if err := json.Unmarshal(out.Bytes(), &m); err != nil {
		t.Fatalf("not JSON: %q", out.String())
	}
	if m["version"] != "dev" || !strings.HasPrefix(m["go"], "go") {
		t.Errorf("unexpected payload %v", m)
	}
}

func TestBadColorFlag(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run(context.Background(), []string{"--color", "rainbow", "version"}, nil, &out, &errb); code != 1 {
		t.Fatalf("expected exit 1, got %d", code)
	}
	if !strings.Contains(errb.String(), "invalid --color") {
		t.Errorf("error message: %q", errb.String())
	}
}

func TestConfigFileFromEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vink.toml")
	if err := os.WriteFile(path, []byte("[server]\nlisten = \":1234\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "other.toml")
	if err := os.WriteFile(other, []byte("[server]\nlisten = \":5678\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VINK_CONFIG_FILE", path)
	run1 := func(args ...string) string {
		var out, errb bytes.Buffer
		if code := run(context.Background(), append([]string{"--color", "never"}, args...), strings.NewReader(""), &out, &errb); code != 0 {
			t.Fatalf("%v: %d %s", args, code, errb.String())
		}
		return out.String()
	}
	if out := run1("serve", "--print-config"); !strings.Contains(out, "listen = ':1234'") {
		t.Fatalf("env file: %s", out)
	}
	if out := run1("serve", "--print-config", "--config", other); !strings.Contains(out, "listen = ':5678'") {
		t.Fatalf("--config wins: %s", out)
	}
	t.Setenv("VINK_CONFIG_FILE", "")
	if out := run1("serve", "--print-config"); !strings.Contains(out, "listen = ':8080'") {
		t.Fatalf("defaults: %s", out)
	}
}
