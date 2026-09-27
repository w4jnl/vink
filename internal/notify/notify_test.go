package notify

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/smtp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/w4jnl/vink/internal/domain"
)

func sample() Notification {
	at := time.Date(2026, 9, 27, 14, 7, 31, 0, time.UTC)
	return Notification{
		Event:    domain.Event{ID: "e1", At: at, From: domain.StateLate, To: domain.StateDown, Reason: "grace over"},
		Monitor:  domain.Monitor{ID: "m1", Slug: "nightly-backup", Name: "Nightly backup", Kind: domain.KindHeartbeat, Tags: []string{"backup", "prod"}, State: domain.StateDown, StateSince: at},
		Project:  domain.Project{Slug: "homelab", Name: "Homelab", Timezone: "Europe/Amsterdam"},
		Incident: &domain.Incident{ID: "i1", OpenedAt: at},
		Links:    Links{Monitor: "https://vink.example.com/o/w4j/p/homelab/m/nightly-backup", Incident: "https://vink.example.com/o/w4j/p/homelab/incidents", Ack: "https://vink.example.com/a/tok"},
	}
}

func TestTitleTextPayload(t *testing.T) {
	n := sample()
	if n.Title() != "[vink] DOWN nightly-backup (homelab)" {
		t.Errorf("title %q", n.Title())
	}
	n.Repeat = true
	if n.Title() != "[vink] STILL DOWN nightly-backup (homelab)" || !strings.Contains(n.Text(), "still down") {
		t.Errorf("repeat: %q %q", n.Title(), n.Text())
	}
	n.Repeat = false
	text := n.Text()
	for _, want := range []string{"Nightly backup is down.", "reason    grace over", "tags      backup, prod", "acknowledge: https://vink.example.com/a/tok"} {
		if !strings.Contains(text, want) {
			t.Errorf("text missing %q:\n%s", want, text)
		}
	}
	p := n.Payload()
	if p.Event != "down" || p.Monitor.Slug != "nightly-backup" || p.Incident == nil || p.Incident.ID != "i1" || p.Links.Ack == "" {
		t.Errorf("payload: %+v", p)
	}
	tn := Notification{Test: true, Project: domain.Project{Slug: "p", Name: "P"}}
	if tn.Kind() != "test" || !strings.Contains(tn.Text(), "test notification") {
		t.Errorf("test notification: %q", tn.Text())
	}
}

func TestRegistry(t *testing.T) {
	r, err := NewRegistry(Options{AllowPrivateTargets: true})
	if err != nil {
		t.Fatal(err)
	}
	if kinds := r.Kinds(); len(kinds) != 3 || kinds[0] != domain.ChannelNtfy {
		t.Errorf("kinds: %v", kinds)
	}
	if err := r.Validate("gotify", []byte(`{}`)); err == nil {
		t.Error("unregistered kind must fail validation")
	}
	if err := r.Send(context.Background(), "gotify", []byte(`{}`), sample()); err == nil {
		t.Error("unregistered kind must fail send")
	}
}

func TestWebhook(t *testing.T) {
	var (
		mu      sync.Mutex
		gotBody string
		gotHdr  http.Header
		gotMeth string
		status  = 200
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotBody, gotHdr, gotMeth = string(b), r.Header.Clone(), r.Method
		st := status
		mu.Unlock()
		w.WriteHeader(st)
		_, _ = w.Write([]byte("nope"))
	}))
	defer srv.Close()
	r, _ := NewRegistry(Options{AllowPrivateTargets: true})
	cfg := []byte(`{"url":"` + srv.URL + `/hook","headers":{"Authorization":"Bearer x"}}`)
	if err := r.Validate(domain.ChannelWebhook, cfg); err != nil {
		t.Fatal(err)
	}
	if err := r.Send(context.Background(), domain.ChannelWebhook, cfg, sample()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	var p Payload
	if err := json.Unmarshal([]byte(gotBody), &p); err != nil || p.Event != "down" || p.Title == "" {
		t.Errorf("default JSON body: %v %s", err, gotBody)
	}
	if gotHdr.Get("Authorization") != "Bearer x" || gotHdr.Get("Content-Type") != "application/json" || gotHdr.Get("X-Vink-Event") != "down" || gotMeth != "POST" {
		t.Errorf("headers: %v method %s", gotHdr, gotMeth)
	}
	mu.Unlock()

	// template body, PUT
	tcfg := []byte(`{"url":"` + srv.URL + `","method":"put","body_template":"{{.Title}} :: {{upper .Event}} :: {{json .Monitor.Tags}}"}`)
	if err := r.Send(context.Background(), domain.ChannelWebhook, tcfg, sample()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if gotBody != `[vink] DOWN nightly-backup (homelab) :: DOWN :: ["backup","prod"]` || gotMeth != "PUT" {
		t.Errorf("template body: %q %s", gotBody, gotMeth)
	}
	status = 500
	mu.Unlock()
	if err := r.Send(context.Background(), domain.ChannelWebhook, cfg, sample()); err == nil || !strings.Contains(err.Error(), "500") || !strings.Contains(err.Error(), "nope") {
		t.Errorf("non-2xx must fail with status and body: %v", err)
	}
	for name, bad := range map[string]string{
		"no url":        `{}`,
		"ftp":           `{"url":"ftp://x"}`,
		"bad method":    `{"url":"https://x","method":"GET"}`,
		"bad template":  `{"url":"https://x","body_template":"{{.Nope"}`,
		"unknown field": `{"url":"https://x","colour":"red"}`,
	} {
		if err := r.Validate(domain.ChannelWebhook, []byte(bad)); err == nil {
			t.Errorf("%s: expected validation error", name)
		} else if _, ok := domain.AsValidation(err); !ok {
			t.Errorf("%s: expected ValidationError, got %v", name, err)
		}
	}
}

func TestPrivateTargetsRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	defer srv.Close()
	r, _ := NewRegistry(Options{AllowPrivateTargets: false})
	err := r.Send(context.Background(), domain.ChannelWebhook, []byte(`{"url":"`+srv.URL+`"}`), sample())
	if err == nil || !strings.Contains(err.Error(), "private target") {
		t.Fatalf("expected private target refusal, got %v", err)
	}
}

func TestNtfy(t *testing.T) {
	var (
		mu   sync.Mutex
		path string
		hdr  http.Header
		body string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		path, hdr, body = r.URL.Path, r.Header.Clone(), string(b)
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer srv.Close()
	r, _ := NewRegistry(Options{AllowPrivateTargets: true})
	cfg := []byte(`{"url":"` + srv.URL + `/","topic":"vink","token":"tk"}`)
	if err := r.Validate(domain.ChannelNtfy, cfg); err != nil {
		t.Fatal(err)
	}
	if err := r.Send(context.Background(), domain.ChannelNtfy, cfg, sample()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if path != "/vink" || hdr.Get("Title") != "[vink] DOWN nightly-backup (homelab)" || hdr.Get("Priority") != "4" || hdr.Get("Authorization") != "Bearer tk" {
		t.Errorf("ntfy request: %s %v", path, hdr)
	}
	if hdr.Get("Click") == "" || !strings.HasPrefix(hdr.Get("Actions"), "view, Acknowledge, ") || !strings.Contains(hdr.Get("Tags"), "down") || !strings.Contains(body, "Nightly backup is down") {
		t.Errorf("ntfy headers/body: %v %q", hdr, body)
	}
	for name, bad := range map[string]string{"no topic": `{"url":"https://x"}`, "slash topic": `{"url":"https://x","topic":"a/b"}`, "priority": `{"url":"https://x","topic":"a","priority":9}`} {
		if err := r.Validate(domain.ChannelNtfy, []byte(bad)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

// fakeSMTP is a minimal SMTP server that records one message.
type fakeSMTP struct {
	ln   net.Listener
	mu   sync.Mutex
	from string
	rcpt []string
	data string
	fail bool
}

func newFakeSMTP(t *testing.T) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{ln: ln}
	go f.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return f
}

func (f *fakeSMTP) serve() {
	for {
		conn, err := f.ln.Accept()
		if err != nil {
			return
		}
		go f.handle(conn)
	}
}

func (f *fakeSMTP) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	r := bufio.NewReader(conn)
	w := func(s string) { _, _ = conn.Write([]byte(s + "\r\n")) }
	w("220 fake ESMTP")
	inData := false
	var data strings.Builder
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if inData {
			if line == "." {
				inData = false
				f.mu.Lock()
				f.data = data.String()
				f.mu.Unlock()
				w("250 ok")
				continue
			}
			data.WriteString(line + "\n")
			continue
		}
		up := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(up, "EHLO"):
			w("250-fake")
			w("250 AUTH PLAIN")
		case strings.HasPrefix(up, "HELO"):
			w("250 fake")
		case strings.HasPrefix(up, "AUTH"):
			w("235 ok")
		case strings.HasPrefix(up, "MAIL FROM:"):
			f.mu.Lock()
			f.from = line[len("MAIL FROM:"):]
			f.mu.Unlock()
			w("250 ok")
		case strings.HasPrefix(up, "RCPT TO:"):
			f.mu.Lock()
			f.rcpt = append(f.rcpt, line[len("RCPT TO:"):])
			fail := f.fail
			f.mu.Unlock()
			if fail {
				w("550 no such user")
			} else {
				w("250 ok")
			}
		case up == "DATA":
			inData = true
			w("354 go")
		case up == "QUIT":
			w("221 bye")
			return
		default:
			w("250 ok")
		}
	}
}

func TestSMTP(t *testing.T) {
	fake := newFakeSMTP(t)
	_, port, _ := net.SplitHostPort(fake.ln.Addr().String())
	var portNum int
	for _, c := range port {
		portNum = portNum*10 + int(c-'0')
	}
	r, _ := NewRegistry(Options{AllowPrivateTargets: true, SMTP: SMTPConfig{Host: "127.0.0.1", Port: portNum, TLS: "none", From: "vink@example.com", Username: "u", Password: "p"}})
	cfg := []byte(`{"to":["ops@example.com","J <j@example.com>"]}`)
	if err := r.Validate(domain.ChannelSMTP, cfg); err != nil {
		t.Fatal(err)
	}
	if err := r.Send(context.Background(), domain.ChannelSMTP, cfg, sample()); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.from != "<vink@example.com>" || len(fake.rcpt) != 2 || fake.rcpt[1] != "<j@example.com>" {
		t.Errorf("envelope: %s %v", fake.from, fake.rcpt)
	}
	for _, want := range []string{"Subject: [vink] DOWN nightly-backup (homelab)", "Content-Type: multipart/alternative", "text/plain; charset=utf-8", "text/html; charset=utf-8", "Nightly backup is down.", "Acknowledge the incident", "Auto-Submitted: auto-generated"} {
		if !strings.Contains(fake.data, want) {
			t.Errorf("message missing %q:\n%s", want, fake.data)
		}
	}
	if !strings.Contains(fake.data, "text/plain") || strings.Index(fake.data, "text/plain") > strings.Index(fake.data, "text/html") {
		t.Error("plain text part must come first")
	}
}

func TestSMTPErrors(t *testing.T) {
	fake := newFakeSMTP(t)
	fake.fail = true
	_, port, _ := net.SplitHostPort(fake.ln.Addr().String())
	var portNum int
	for _, c := range port {
		portNum = portNum*10 + int(c-'0')
	}
	r, _ := NewRegistry(Options{SMTP: SMTPConfig{Host: "127.0.0.1", Port: portNum, TLS: "none"}})
	err := r.Send(context.Background(), domain.ChannelSMTP, []byte(`{"to":["x@example.com"]}`), sample())
	if err == nil || !strings.Contains(err.Error(), "RCPT TO") {
		t.Errorf("rejected recipient: %v", err)
	}
	noHost, _ := NewRegistry(Options{})
	if err := noHost.Validate(domain.ChannelSMTP, []byte(`{"to":["x@example.com"]}`)); err == nil || !strings.Contains(err.Error(), "no mail server") {
		t.Errorf("validate without host: %v", err)
	}
	for name, bad := range map[string]string{"empty": `{}`, "bad addr": `{"to":["not an address"]}`, "bad from": `{"to":["x@example.com"],"from":"nope"}`} {
		if err := r.Validate(domain.ChannelSMTP, []byte(bad)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	// STARTTLS required but not offered
	strict, _ := NewRegistry(Options{SMTP: SMTPConfig{Host: "127.0.0.1", Port: portNum, TLS: "starttls"}})
	if err := strict.Send(context.Background(), domain.ChannelSMTP, []byte(`{"to":["x@example.com"]}`), sample()); err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Errorf("starttls required: %v", err)
	}
	_ = smtp.PlainAuth
}
