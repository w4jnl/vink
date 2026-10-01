package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestAgentCommandFlags(t *testing.T) {
	runCLI := func(args ...string) (string, int) {
		var out, errb bytes.Buffer
		code := run(context.Background(), append([]string{"--color", "never"}, args...), strings.NewReader(""), &out, &errb)
		return out.String() + errb.String(), code
	}
	if out, code := runCLI("agent"); code == 0 || !strings.Contains(out, "server") {
		t.Fatalf("missing server: %d %s", code, out)
	}
	t.Setenv("VINK_AGENT_TOKEN", "")
	if out, code := runCLI("agent", "--server", "wss://vink.example.com"); code == 0 || !strings.Contains(out, "token") {
		t.Fatalf("missing token: %d %s", code, out)
	}
	if out, code := runCLI("agent", "--server", "ftp://x", "--token", "vat_x"); code == 0 || !strings.Contains(out, "wss://") {
		t.Fatalf("bad server: %d %s", code, out)
	}
	if out, code := runCLI("agent", "--server", "wss://x", "--token", "vat_x", "--labels", "nope"); code == 0 || !strings.Contains(out, "key=value") {
		t.Fatalf("bad labels: %d %s", code, out)
	}
	if out, code := runCLI("agent", "--server", "wss://x", "--token", "vat_x", "--pin", "abc"); code == 0 || !strings.Contains(out, "64 hex") {
		t.Fatalf("bad pin: %d %s", code, out)
	}
}
