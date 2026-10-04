package middleware

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestIDAndLogger(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	h := Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		AddLogFields(r.Context(), slog.String("project_id", "p1"))
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("ok"))
	}), RequestID, Logger(log))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/x", nil))
	if rec.Code != 201 || rec.Header().Get("X-Request-Id") == "" {
		t.Fatalf("status %d id %q", rec.Code, rec.Header().Get("X-Request-Id"))
	}
	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("log line: %q", buf.String())
	}
	if line["status"] != float64(201) || line["project_id"] != "p1" || line["req_id"] != rec.Header().Get("X-Request-Id") || line["method"] != "POST" {
		t.Errorf("log line: %v", line)
	}
}

func TestRealIP(t *testing.T) {
	cases := []struct {
		name, remote, xff, want string
		trusted                 []string
	}{
		{"no proxy", "203.0.113.5:1234", "198.51.100.1", "203.0.113.5", nil},
		{"trusted proxy takes xff", "10.0.0.1:1234", "198.51.100.1", "198.51.100.1", []string{"10.0.0.0/8"}},
		{"chain of trusted hops", "10.0.0.1:1234", "198.51.100.1, 10.0.0.2", "198.51.100.1", []string{"10.0.0.0/8"}},
		{"untrusted peer ignores xff", "203.0.113.5:1234", "198.51.100.1", "203.0.113.5", []string{"10.0.0.0/8"}},
		{"garbage xff falls back", "10.0.0.1:1234", "not-an-ip", "10.0.0.1", []string{"10.0.0.0/8"}},
		{"ipv6 peer", "[::1]:1234", "", "::1", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got string
			h := RealIP(c.trusted)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got = ClientIP(r) }))
			req := httptest.NewRequest("GET", "/", nil)
			req.RemoteAddr = c.remote
			if c.xff != "" {
				req.Header.Set("X-Forwarded-For", c.xff)
			}
			h.ServeHTTP(httptest.NewRecorder(), req)
			if got != c.want {
				t.Errorf("ClientIP = %q, want %q", got, c.want)
			}
		})
	}
}

func TestRecover(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	h := Recover(log)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 500 || !strings.Contains(buf.String(), "boom") {
		t.Fatalf("status %d log %s", rec.Code, buf.String())
	}
}

func TestMountUnderRewritesRedirects(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if Prefix(r) != "/vink" {
			t.Errorf("prefix on the context: %q", Prefix(r))
		}
		switch r.URL.Path {
		case "/here":
			http.Redirect(w, r, Href(r, "/there"), http.StatusSeeOther) //nolint:gosec // G710: a fixed path of our own
		case "/missed":
			http.Redirect(w, r, "/there", http.StatusSeeOther) // a literal that missed the helper
		case "/outside":
			http.Redirect(w, r, "https://example.com/x", http.StatusSeeOther)
		case "/protocol":
			w.Header().Set("Location", "//evil.example/x")
			w.WriteHeader(http.StatusSeeOther)
		case "/hx":
			w.Header().Set("HX-Redirect", "/o/x")
			w.WriteHeader(http.StatusNoContent)
		default:
			_, _ = w.Write([]byte("path=" + r.URL.Path)) //nolint:gosec // G705: a test echo of the stripped path
		}
	})
	h := MountUnder("/vink", inner)
	want := map[string]string{"/vink/here": "/vink/there", "/vink/missed": "/vink/there", "/vink/outside": "https://example.com/x", "/vink/protocol": "//evil.example/x"}
	for path, loc := range want {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if got := rec.Header().Get("Location"); got != loc {
			t.Errorf("%s: Location %q, want %q", path, got, loc)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/vink/hx", nil))
	if rec.Header().Get("HX-Redirect") != "/vink/o/x" {
		t.Errorf("HX-Redirect: %q", rec.Header().Get("HX-Redirect"))
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/vink/a%2Fb?q=1", nil))
	if rec.Body.String() != "path=/a/b" {
		t.Errorf("stripped path: %q", rec.Body.String())
	}
	if MountUnder("", inner) == nil || Prefix(httptest.NewRequest("GET", "/", nil)) != "" {
		t.Error("no prefix means the handler as it is and an empty prefix")
	}
	pr := httptest.NewRequest("GET", "/x", nil)
	pr = pr.WithContext(WithPrefix(pr.Context(), "/vink"))
	for in, want := range map[string]string{"/o/x": "/vink/o/x", "/vink/o/x": "/vink/o/x", "/vink": "/vink", "/": "/vink/", "/vinkish": "/vink/vinkish"} {
		if got := Href(pr, in); got != want {
			t.Errorf("Href(%q) = %q, want %q", in, got, want)
		}
	}
}
