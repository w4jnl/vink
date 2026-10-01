package qr

import (
	"strings"
	"testing"
)

func TestSVG(t *testing.T) {
	svg, err := SVG("otpauth://totp/vink:j?secret=JBSWY3DPEHPK3PXP&issuer=vink")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`<svg viewBox="0 0 `, `shape-rendering="crispEdges" aria-hidden="true"><path fill="currentColor" d="M0 0h7v1h-7z`, `"></path></svg>`} {
		if !strings.Contains(svg, want) {
			t.Errorf("svg lacks %q", want)
		}
	}
	if strings.Contains(svg, "<script") || strings.Contains(svg, "http") {
		t.Error("svg must be plain paths")
	}
	if _, err := SVG(""); err == nil {
		t.Error("empty text encoded")
	}
}
