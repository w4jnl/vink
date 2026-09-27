package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStaticHashedURLsAndCaching(t *testing.T) {
	s, err := NewStatic()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Hash()) != 12 {
		t.Fatalf("hash %q", s.Hash())
	}
	h := s.Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", s.URL("tokens.css"), nil))
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") || !strings.Contains(rec.Body.String(), "--accent") {
		t.Fatalf("tokens.css: %d %v", rec.Code, rec.Header())
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/static/oldhash/tokens.css", nil))
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("old hash: %d %v", rec.Code, rec.Header())
	}
	for _, p := range []string{"/static/", "/static/" + s.Hash() + "/", "/static/" + s.Hash() + "/missing.css"} {
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d", p, rec.Code)
		}
	}
	for _, f := range []string{"bundle.css", "htmx.min.js", "vink.js", "app.css", "favicon.svg", "favicon-down.svg", "fonts/JetBrainsMono-400.woff2", "fonts/JetBrainsMono-700.woff2", "fonts/JetBrainsMono-800.woff2"} {
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", s.URL(f), nil))
		if rec.Code != 200 {
			t.Errorf("%s: %d", f, rec.Code)
		}
	}
}

// TestStaticCopiesMatchDesignSystem fails when the embedded copies drift
// from docs/design-system and assets/brand.
func TestStaticCopiesMatchDesignSystem(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	pairs := map[string]string{ //nolint:gosec // G101: file paths, not credentials
		"static/tokens.css":                    "docs/design-system/tokens.css",
		"static/bundle.css":                    "docs/design-system/components/bundle.css",
		"static/fonts/JetBrainsMono-400.woff2": "docs/design-system/fonts/JetBrainsMono-400.woff2",
		"static/fonts/JetBrainsMono-700.woff2": "docs/design-system/fonts/JetBrainsMono-700.woff2",
		"static/fonts/JetBrainsMono-800.woff2": "docs/design-system/fonts/JetBrainsMono-800.woff2",
		"static/favicon.svg":                   "assets/brand/favicon.svg",
		"static/favicon-down.svg":              "assets/brand/favicon-down.svg",
	}
	for embedded, source := range pairs {
		a, err := staticFiles.ReadFile(embedded)
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(filepath.Join(root, source))
		if err != nil {
			t.Fatal(err)
		}
		if string(a) != string(b) {
			t.Errorf("%s differs from %s; copy it again", embedded, source)
		}
	}
}
