package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestVersionCommand(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run(context.Background(), []string{"version"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "vink dev") || !strings.Contains(out.String(), "heartbeat and uptime monitor") {
		t.Errorf("lockup missing: %q", out.String())
	}
}

func TestVersionJSON(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run(context.Background(), []string{"version", "--json"}, &out, &errb); code != 0 {
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
	if code := run(context.Background(), []string{"--color", "rainbow", "version"}, &out, &errb); code != 1 {
		t.Fatalf("expected exit 1, got %d", code)
	}
	if !strings.Contains(errb.String(), "invalid --color") {
		t.Errorf("error message: %q", errb.String())
	}
}
