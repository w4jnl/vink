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
