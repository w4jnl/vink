package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestParseColorMode(t *testing.T) {
	for in, want := range map[string]ColorMode{"": ColorAuto, "auto": ColorAuto, "ALWAYS": ColorAlways, "never": ColorNever} {
		got, err := ParseColorMode(in)
		if err != nil || got != want {
			t.Errorf("ParseColorMode(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := ParseColorMode("sometimes"); err == nil {
		t.Error("expected error for invalid mode")
	}
}

func TestNewJSONByDefault(t *testing.T) {
	var buf bytes.Buffer
	l := New(Options{Level: "info", Format: "json", Out: &buf})
	l.Debug("hidden")
	l.Info("hello", "k", "v")
	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("not JSON: %q: %v", buf.String(), err)
	}
	if rec["msg"] != "hello" || rec["k"] != "v" {
		t.Errorf("unexpected record %v", rec)
	}
}

func TestNewDebugIsTextWithoutColorOnBuffer(t *testing.T) {
	var buf bytes.Buffer
	l := New(Options{Debug: true, Format: "json", Out: &buf})
	l.Debug("visible", "k", 1)
	s := buf.String()
	if !strings.Contains(s, "visible") || !strings.Contains(s, "k=1") {
		t.Errorf("debug text output missing fields: %q", s)
	}
	if strings.Contains(s, "\x1b[") {
		t.Errorf("colour escapes written to a non-terminal: %q", s)
	}
	if strings.HasPrefix(strings.TrimSpace(s), "{") {
		t.Errorf("debug mode should not emit JSON: %q", s)
	}
}

func TestColorAlwaysOnBuffer(t *testing.T) {
	var buf bytes.Buffer
	l := New(Options{Debug: true, Color: ColorAlways, Out: &buf})
	l.Info("coloured")
	if !strings.Contains(buf.String(), "\x1b[") {
		t.Errorf("expected escapes with --color always: %q", buf.String())
	}
}

func TestColorNoColorEnv(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	if ColorEnabled(ColorAuto, &bytes.Buffer{}) {
		t.Error("NO_COLOR set: auto must be off")
	}
	if !ColorEnabled(ColorAlways, &bytes.Buffer{}) {
		t.Error("always must win over NO_COLOR")
	}
}

func TestContextLogger(t *testing.T) {
	l := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	ctx := WithLogger(context.Background(), l)
	if FromContext(ctx) != l {
		t.Error("FromContext did not return the stored logger")
	}
	if FromContext(context.Background()) == nil {
		t.Error("FromContext without a logger must fall back to the default")
	}
}
