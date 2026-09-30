//go:build e2e

package e2e

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestHomelabSmoke is the phase 1 gate: the homelab replacing Uptime
// Kuma. An HTTP, a TCP and a TLS monitor come from one apply file with a
// webhook channel, a maintenance window and a public status page. The
// web service breaks and the page, the badge and the webhook say so; the
// database drops while its window is active and stays quiet; the export
// applies back to an empty diff; /metrics counts the checks.
func TestHomelabSmoke(t *testing.T) {
	// targets: a health endpoint that can break, a TCP port, a TLS server
	var healthy atomic.Bool
	healthy.Store(true)
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !healthy.Load() {
			w.WriteHeader(503)
			return
		}
		fmt.Fprint(w, `{"status":"ok"}`)
	}))
	defer web.Close()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	defer tlsSrv.Close()
	caPath := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: tlsSrv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := x509.ParseCertificate(tlsSrv.Certificate().Raw); err != nil {
		t.Fatal(err)
	}
	_, tcpPort, _ := net.SplitHostPort(ln.Addr().String())
	_, tlsPort, _ := net.SplitHostPort(strings.TrimPrefix(tlsSrv.URL, "https://"))

	in := startInstance(t, "VINK_OUTBOUND_CA_PEM="+caPath)
	receiver := &hook{}
	hookSrv := httptest.NewServer(receiver)
	defer hookSrv.Close()

	// the homelab in one file
	file := fmt.Sprintf(`version: 1
channels:
  - {name: hook, kind: webhook, url: %s}
routes:
  - {match_tags: [], channels: [hook], on: [down, up]}
maintenance:
  - {name: database move, match_tags: [db], starts_at: %s, ends_at: %s}
monitors:
  - {slug: web, name: Web app, kind: http, tags: [web], interval: 10s, timeout: 2s, failure_threshold: 1, confirm: {retries: 0, delay: 1s},
     http: {url: %s/health, expect_body: {jsonpath: {path: $.status, equals: ok}}}}
  - {slug: db, name: Database, kind: tcp, tags: [db], interval: 10s, timeout: 2s, failure_threshold: 1, confirm: {retries: 0, delay: 1s}, tcp: {host: 127.0.0.1, port: %s}}
  - {slug: cert, name: Certificate, kind: tls, tags: [web], interval: 10s, timeout: 2s, tls: {host: 127.0.0.1, port: %s, servername: example.com}}
status_pages:
  - {slug: homelab, title: Homelab status, match_tags: [web, db], public: true}
`, hookSrv.URL, time.Now().UTC().Format(time.RFC3339), time.Now().UTC().Add(10*time.Minute).Format(time.RFC3339), web.URL, tcpPort, tlsPort)
	path := filepath.Join(in.dir, "homelab.yaml")
	if err := os.WriteFile(path, []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := in.vink("", "apply", "-f", path)
	if err != nil || !strings.Contains(out, "+ monitor web") || !strings.Contains(out, "+ maintenance database move") || !strings.Contains(out, "+ page homelab") {
		t.Fatalf("apply: %s %v", out, err)
	}
	start := time.Now()
	waitFor(t, 30*time.Second, func() bool { return in.state("web") == "up" && in.state("db") == "up" && in.state("cert") == "up" })
	t.Logf("three monitors up after %s", time.Since(start).Round(time.Second))
	cert := in.api("GET", "/monitors/cert", nil)
	if cert["target"] != "127.0.0.1:"+tlsPort {
		t.Fatalf("cert monitor: %v", cert)
	}
	// the database's window is active, so the page says Maintenance
	code, body, hdr := in.get("/s/homelab")
	if code != 200 || !strings.Contains(body, "vk-banner--maintenance") || !strings.Contains(body, "Web app") || !strings.Contains(body, "Database") || !strings.Contains(body, "Certificate") || hdr.Get("Cache-Control") != "public, max-age=30" || strings.Contains(body, "<script") {
		t.Fatalf("status page: %d %s\n%s", code, hdr.Get("Cache-Control"), body)
	}

	// the web app breaks: down within an interval, a webhook, the page and the badge say so
	healthy.Store(false)
	waitFor(t, 30*time.Second, func() bool { return in.state("web") == "down" })
	t.Logf("web down after %s", time.Since(start).Round(time.Second))
	waitFor(t, 15*time.Second, func() bool { return len(receiver.kinds()) >= 1 })
	if kinds := receiver.kinds(); kinds[0] != "down web" {
		t.Fatalf("webhook: %v", kinds)
	}
	if code, body, _ := in.get("/s/homelab"); code != 200 || !strings.Contains(body, "1 service down") {
		t.Fatalf("status page while down: %d", code)
	}
	if code, body, hdr := in.get("/s/homelab/badge/web.svg"); code != 200 || !strings.Contains(body, ">down<") || !strings.HasPrefix(hdr.Get("Content-Type"), "image/svg+xml") {
		t.Fatalf("badge: %d %s", code, body)
	}
	obs := in.api("GET", "/monitors/web/observations?limit=1", nil)
	if items, _ := obs["items"].([]any); len(items) != 1 || !strings.Contains(fmt.Sprint(items[0]), "503 Service Unavailable") {
		t.Fatalf("last observation: %v", obs)
	}

	// the database drops inside its maintenance window: late, no incident, no webhook
	_ = ln.Close()
	waitFor(t, 30*time.Second, func() bool { return in.state("db") == "late" })
	t.Logf("db late (maintenance) after %s", time.Since(start).Round(time.Second))
	time.Sleep(12 * time.Second) // another attempt inside the window changes nothing
	if s := in.state("db"); s != "late" {
		t.Fatalf("db during maintenance: %s", s)
	}
	inc := in.api("GET", "/incidents?open=1", nil)
	if items, _ := inc["items"].([]any); len(items) != 1 || !strings.Contains(fmt.Sprint(items[0]), "web") {
		t.Fatalf("incidents during maintenance: %v", inc)
	}
	for _, k := range receiver.kinds() {
		if strings.HasSuffix(k, " db") {
			t.Fatalf("maintenance must silence db: %v", receiver.kinds())
		}
	}

	// the web app is fixed: check now brings it up and the recovery is sent
	healthy.Store(true)
	if out, err := in.vink("", "check", "web"); err != nil || !strings.Contains(out, "checked web: up") {
		t.Fatalf("check now: %s %v", out, err)
	}
	waitFor(t, 15*time.Second, func() bool { return len(receiver.kinds()) >= 2 })
	if kinds := receiver.kinds(); kinds[1] != "up web" {
		t.Fatalf("recovery webhook: %v", kinds)
	}

	// export applies back to an empty diff, with the secret-free file
	exported := filepath.Join(in.dir, "export.yaml")
	if _, err := in.vink("", "export", "-o", exported); err != nil {
		t.Fatalf("export: %v", err)
	}
	if out, err := in.vink("", "apply", "-f", exported, "--prune"); err != nil || !strings.Contains(out, "0 created, 0 updated, 0 recreated, 0 deleted") {
		t.Fatalf("round trip: %s %v", out, err)
	}

	// the CLI and metrics see it all
	if out, err := in.vink("", "ls"); err != nil || !strings.Contains(out, "web") || !strings.Contains(out, "cert") {
		t.Fatalf("ls: %s %v", out, err)
	}
	if code, body, _ := in.get("/metrics"); code != 200 || !strings.Contains(body, `vink_checks_total{kind="http",ok="true",project="homelab"}`) || !strings.Contains(body, `vink_monitors{kind="tls",org="homelab",project="homelab",state="up"} 1`) {
		t.Fatalf("metrics: %d\n%s", code, body)
	}
	t.Logf("done in %s", time.Since(start).Round(time.Second))
}
