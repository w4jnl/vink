package checks

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/outbound"
)

func newReg(t *testing.T, o Options) *Registry {
	t.Helper()
	o.Outbound.AllowPrivateTargets = true
	r, err := NewRegistry(o)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func pull(spec domain.PullSpec) *domain.PullSpec {
	spec.Normalize()
	return &spec
}

func httpSpec(h domain.HTTPCheck, timeout string) *domain.PullSpec {
	s := domain.PullSpec{HTTP: &h}
	if timeout != "" {
		s.Timeout = domain.MustDuration(timeout)
	}
	return pull(s)
}

func testServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/ok", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "all ok here") })
	mux.HandleFunc("/json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":"ok","items":[{"name":"a"}],"n":1,"deep":{"x y":true}}`)
	})
	mux.HandleFunc("/503", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) })
	mux.HandleFunc("/redirect", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/ok", http.StatusFound) })
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(3 * time.Second):
		case <-r.Context().Done():
		}
		w.WriteHeader(200)
	})
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		fmt.Fprintf(w, "%s %s ua=%s body=%s", r.Method, r.Header.Get("X-Test"), r.UserAgent(), body) //nolint:gosec // a test echo server
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestHTTPChecks(t *testing.T) {
	srv := testServer(t)
	reg := newReg(t, Options{UserAgent: "vink-test"})
	ctx := context.Background()
	f := false
	cases := []struct {
		name   string
		check  domain.HTTPCheck
		ok     bool
		reason string
		detail map[string]any
	}{
		{"status ok", domain.HTTPCheck{URL: srv.URL + "/ok"}, true, "", map[string]any{"status": 200}},
		{"503", domain.HTTPCheck{URL: srv.URL + "/503"}, false, "status 503", nil},
		{"expect 503", domain.HTTPCheck{URL: srv.URL + "/503", ExpectStatus: []domain.StatusRange{{Lo: 503, Hi: 503}}}, true, "", nil},
		{"want range", domain.HTTPCheck{URL: srv.URL + "/ok", ExpectStatus: []domain.StatusRange{{Lo: 300, Hi: 399}}}, false, "status 200, want 300-399", nil},
		{"contains", domain.HTTPCheck{URL: srv.URL + "/ok", ExpectBody: &domain.ExpectBody{Contains: "ok"}}, true, "", map[string]any{"matched": true}},
		{"lacks", domain.HTTPCheck{URL: srv.URL + "/ok", ExpectBody: &domain.ExpectBody{Contains: "nope"}}, false, `body lacks "nope"`, nil},
		{"not contains", domain.HTTPCheck{URL: srv.URL + "/ok", ExpectBody: &domain.ExpectBody{NotContains: "ok"}}, false, `body contains "ok"`, nil},
		{"jsonpath equals", domain.HTTPCheck{URL: srv.URL + "/json", ExpectBody: &domain.ExpectBody{JSONPath: &domain.JSONPathExpect{Path: "$.status", Equals: "ok"}}}, true, "", nil},
		{"jsonpath differs", domain.HTTPCheck{URL: srv.URL + "/json", ExpectBody: &domain.ExpectBody{JSONPath: &domain.JSONPathExpect{Path: "$.status", Equals: "down"}}}, false, `$.status is "ok", want "down"`, nil},
		{"jsonpath index", domain.HTTPCheck{URL: srv.URL + "/json", ExpectBody: &domain.ExpectBody{JSONPath: &domain.JSONPathExpect{Path: "$.items[0].name", Equals: "a"}}}, true, "", nil},
		{"jsonpath number", domain.HTTPCheck{URL: srv.URL + "/json", ExpectBody: &domain.ExpectBody{JSONPath: &domain.JSONPathExpect{Path: "$.n", Equals: 1}}}, true, "", nil},
		{"jsonpath quoted key", domain.HTTPCheck{URL: srv.URL + "/json", ExpectBody: &domain.ExpectBody{JSONPath: &domain.JSONPathExpect{Path: `$.deep["x y"]`, Equals: true}}}, true, "", nil},
		{"jsonpath missing", domain.HTTPCheck{URL: srv.URL + "/json", ExpectBody: &domain.ExpectBody{JSONPath: &domain.JSONPathExpect{Path: "$.missing", Equals: "x"}}}, false, "$.missing not found", nil},
		{"jsonpath not json", domain.HTTPCheck{URL: srv.URL + "/ok", ExpectBody: &domain.ExpectBody{JSONPath: &domain.JSONPathExpect{Path: "$.a", Equals: "x"}}}, false, "body is not JSON", nil},
		{"redirect followed", domain.HTTPCheck{URL: srv.URL + "/redirect"}, true, "", map[string]any{"status": 200, "final_url": srv.URL + "/ok"}},
		{"redirect kept", domain.HTTPCheck{URL: srv.URL + "/redirect", FollowRedirects: &f}, false, "status 302", nil},
		{"echo", domain.HTTPCheck{URL: srv.URL + "/echo", Method: "POST", Headers: map[string]string{"X-Test": "1"}, Body: "payload", ExpectBody: &domain.ExpectBody{Contains: "POST 1 ua=vink-test body=payload"}}, true, "", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := reg.Attempt(ctx, domain.KindHTTP, httpSpec(c.check, ""))
			if res.OK != c.ok || (c.reason != "" && res.Reason != c.reason) {
				t.Fatalf("got ok=%v reason=%q detail=%v", res.OK, res.Reason, res.Detail)
			}
			for k, v := range c.detail {
				if fmt.Sprint(res.Detail[k]) != fmt.Sprint(v) {
					t.Errorf("detail %s = %v, want %v", k, res.Detail[k], v)
				}
			}
			if res.LatencyMs < 0 || res.Detail["bytes"] == nil {
				t.Errorf("latency and bytes must be set: %+v", res)
			}
		})
	}
	t.Run("timeout", func(t *testing.T) {
		res := reg.Attempt(ctx, domain.KindHTTP, httpSpec(domain.HTTPCheck{URL: srv.URL + "/slow"}, "300ms"))
		if res.OK || res.Reason != "timeout after 300ms" {
			t.Fatalf("got ok=%v reason=%q", res.OK, res.Reason)
		}
	})
	t.Run("refused", func(t *testing.T) {
		res := reg.Attempt(ctx, domain.KindHTTP, httpSpec(domain.HTTPCheck{URL: "http://127.0.0.1:1/x"}, ""))
		if res.OK || !strings.Contains(res.Reason, "connection refused") {
			t.Fatalf("got ok=%v reason=%q", res.OK, res.Reason)
		}
	})
	t.Run("private target refused", func(t *testing.T) {
		strict, err := NewRegistry(Options{Outbound: outbound.Options{AllowPrivateTargets: false}})
		if err != nil {
			t.Fatal(err)
		}
		res := strict.Attempt(ctx, domain.KindHTTP, httpSpec(domain.HTTPCheck{URL: srv.URL + "/ok"}, ""))
		if res.OK || !strings.Contains(res.Reason, "refusing private target") {
			t.Fatalf("got ok=%v reason=%q", res.OK, res.Reason)
		}
	})
}

func TestHTTPTLS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "secure") }))
	defer srv.Close()
	reg := newReg(t, Options{})
	ctx := context.Background()
	f := false
	if res := reg.Attempt(ctx, domain.KindHTTP, httpSpec(domain.HTTPCheck{URL: srv.URL}, "")); res.OK || !strings.Contains(res.Reason, "x509") {
		t.Fatalf("unknown CA must fail: ok=%v %q", res.OK, res.Reason)
	}
	if res := reg.Attempt(ctx, domain.KindHTTP, httpSpec(domain.HTTPCheck{URL: srv.URL, VerifyTLS: &f}, "")); !res.OK {
		t.Fatalf("verify_tls false: %q", res.Reason)
	}
	certPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))
	res := reg.Attempt(ctx, domain.KindHTTP, httpSpec(domain.HTTPCheck{URL: srv.URL, CAPem: certPEM}, ""))
	if !res.OK || res.Detail["tls_days_left"] == nil {
		t.Fatalf("ca_pem: ok=%v %q %v", res.OK, res.Reason, res.Detail)
	}
}

func TestTCPCheck(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				_, _ = io.WriteString(c, "220 vink smtp\r\n")
				buf := make([]byte, 64)
				n, _ := c.Read(buf)
				if strings.Contains(string(buf[:n]), "PING") {
					_, _ = io.WriteString(c, "PONG\n")
				}
			}(c)
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	reg := newReg(t, Options{})
	ctx := context.Background()
	spec := func(send, expect string) *domain.PullSpec {
		return pull(domain.PullSpec{Timeout: domain.MustDuration("2s"), TCP: &domain.TCPCheck{Host: "127.0.0.1", Port: port, Send: send, Expect: expect}})
	}
	if res := reg.Attempt(ctx, domain.KindTCP, spec("", "")); !res.OK || res.Detail["addr"] == nil {
		t.Fatalf("connect: %+v", res)
	}
	if res := reg.Attempt(ctx, domain.KindTCP, spec("", "220")); !res.OK || res.Detail["banner"] != "220 vink smtp" {
		t.Fatalf("banner: %+v", res)
	}
	if res := reg.Attempt(ctx, domain.KindTCP, spec("", "999")); res.OK || res.Reason != `banner lacks "999"` {
		t.Fatalf("mismatch: %+v", res)
	}
	if res := reg.Attempt(ctx, domain.KindTCP, spec("PING\n", "PONG")); !res.OK {
		t.Fatalf("send/expect: %+v", res)
	}
	closed := pull(domain.PullSpec{TCP: &domain.TCPCheck{Host: "127.0.0.1", Port: 1}})
	if res := reg.Attempt(ctx, domain.KindTCP, closed); res.OK || !strings.Contains(res.Reason, "connection refused") {
		t.Fatalf("closed port: %+v", res)
	}
}

// dnsStub answers A, TXT and CNAME for vink.test and NXDOMAIN otherwise.
func dnsStub(t *testing.T) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	go func() {
		buf := make([]byte, 1500)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			var p dnsmessage.Parser
			hdr, err := p.Start(buf[:n])
			if err != nil {
				continue
			}
			q, err := p.Question()
			if err != nil {
				continue
			}
			name := strings.ToLower(q.Name.String())
			rh := func(n dnsmessage.Name, typ dnsmessage.Type) dnsmessage.ResourceHeader {
				return dnsmessage.ResourceHeader{Name: n, Type: typ, Class: dnsmessage.ClassINET, TTL: 60}
			}
			rcode := dnsmessage.RCodeSuccess
			var add func(b *dnsmessage.Builder)
			switch {
			case name == "vink.test." && q.Type == dnsmessage.TypeA:
				add = func(b *dnsmessage.Builder) {
					_ = b.AResource(rh(q.Name, dnsmessage.TypeA), dnsmessage.AResource{A: [4]byte{10, 0, 0, 2}})
					_ = b.AResource(rh(q.Name, dnsmessage.TypeA), dnsmessage.AResource{A: [4]byte{10, 0, 0, 1}})
				}
			case name == "txt.vink.test." && q.Type == dnsmessage.TypeTXT:
				add = func(b *dnsmessage.Builder) {
					_ = b.TXTResource(rh(q.Name, dnsmessage.TypeTXT), dnsmessage.TXTResource{TXT: []string{"hello"}})
				}
			case name == "alias.vink.test.":
				add = func(b *dnsmessage.Builder) {
					_ = b.CNAMEResource(rh(q.Name, dnsmessage.TypeCNAME), dnsmessage.CNAMEResource{CNAME: dnsmessage.MustNewName("vink.test.")})
					if q.Type == dnsmessage.TypeA {
						_ = b.AResource(rh(dnsmessage.MustNewName("vink.test."), dnsmessage.TypeA), dnsmessage.AResource{A: [4]byte{10, 0, 0, 1}})
					}
				}
			case strings.HasSuffix(name, "vink.test."):
				add = func(*dnsmessage.Builder) {}
			default:
				rcode = dnsmessage.RCodeNameError
				add = func(*dnsmessage.Builder) {}
			}
			b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: hdr.ID, Response: true, Authoritative: true, RecursionAvailable: true, RCode: rcode})
			b.EnableCompression()
			_ = b.StartQuestions()
			_ = b.Question(q)
			_ = b.StartAnswers()
			add(&b)
			out, err := b.Finish()
			if err != nil {
				continue
			}
			_, _ = pc.WriteTo(out, addr)
		}
	}()
	return pc.LocalAddr().String()
}

func TestDNSCheck(t *testing.T) {
	resolver := dnsStub(t)
	reg := newReg(t, Options{})
	ctx := context.Background()
	spec := func(name, typ string, expect ...string) *domain.PullSpec {
		return pull(domain.PullSpec{Timeout: domain.MustDuration("2s"), DNS: &domain.DNSCheck{Name: name, Type: typ, Resolver: resolver, Expect: expect}})
	}
	res := reg.Attempt(ctx, domain.KindDNS, spec("vink.test", "A"))
	if !res.OK || fmt.Sprint(res.Detail["answers"]) != "[10.0.0.1 10.0.0.2]" || res.Detail["resolver"] != resolver {
		t.Fatalf("A: %+v", res)
	}
	if res := reg.Attempt(ctx, domain.KindDNS, spec("vink.test", "A", "10.0.0.2")); !res.OK {
		t.Fatalf("expect present: %+v", res)
	}
	if res := reg.Attempt(ctx, domain.KindDNS, spec("vink.test", "A", "10.0.0.9")); res.OK || res.Reason != "answers 10.0.0.1, 10.0.0.2, want 10.0.0.9" {
		t.Fatalf("expect missing: %+v", res)
	}
	if res := reg.Attempt(ctx, domain.KindDNS, spec("txt.vink.test", "TXT", "hello")); !res.OK {
		t.Fatalf("TXT: %+v", res)
	}
	if res := reg.Attempt(ctx, domain.KindDNS, spec("alias.vink.test", "CNAME", "vink.test.")); !res.OK {
		t.Fatalf("CNAME: %+v", res)
	}
	// the Go resolver reports an empty answer as not found, like NXDOMAIN
	if res := reg.Attempt(ctx, domain.KindDNS, spec("empty.vink.test", "A")); res.OK || !strings.Contains(res.Reason, "no such host") {
		t.Fatalf("empty: %+v", res)
	}
	if res := reg.Attempt(ctx, domain.KindDNS, spec("nope.example", "A")); res.OK || !strings.Contains(res.Reason, "no such host") {
		t.Fatalf("nxdomain: %+v", res)
	}
}

// tlsServer serves a self-signed certificate for vink.test that expires
// at notAfter and returns its address and the PEM file to trust.
func tlsServer(t *testing.T, notBefore, notAfter time.Time) (addr, pemPath string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "vink.test"}, DNSNames: []string{"vink.test"},
		NotBefore: notBefore, NotAfter: notAfter, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true, IsCA: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				_ = c.(*tls.Conn).Handshake()
				_ = c.Close()
			}(c)
		}
	}()
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return ln.Addr().String(), path
}

func TestTLSCheck(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	spec := func(addr string) *domain.PullSpec {
		host, portStr, _ := net.SplitHostPort(addr)
		var port int
		_, _ = fmt.Sscan(portStr, &port)
		return pull(domain.PullSpec{Timeout: domain.MustDuration("2s"), TLS: &domain.TLSCheck{Host: host, Port: port, ServerName: "vink.test"}})
	}
	cases := []struct {
		name       string
		notAfter   time.Time
		ok, warn   bool
		reasonPart string
	}{
		{"long valid", now.Add(100 * 24 * time.Hour), true, false, ""},
		{"warn", now.Add(10*24*time.Hour + time.Hour), true, true, "expires in 10 d"},
		{"crit", now.Add(2*24*time.Hour + time.Hour), false, false, "expires in 2 d"},
		{"expired", now.Add(-2 * 24 * time.Hour), false, false, "expired"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			addr, pemPath := tlsServer(t, now.Add(-10*24*time.Hour), c.notAfter)
			reg := newReg(t, Options{Outbound: outbound.Options{CAPem: pemPath}})
			res := reg.Attempt(ctx, domain.KindTLS, spec(addr))
			if res.OK != c.ok || res.Warn != c.warn || !strings.Contains(res.Reason, c.reasonPart) {
				t.Fatalf("got ok=%v warn=%v reason=%q detail=%v", res.OK, res.Warn, res.Reason, res.Detail)
			}
			if c.ok && (res.Detail["days_left"] == nil || res.Detail["not_after"] == nil || res.Detail["subject"] != "vink.test") {
				t.Errorf("detail: %v", res.Detail)
			}
		})
	}
	t.Run("untrusted", func(t *testing.T) {
		addr, _ := tlsServer(t, now.Add(-time.Hour), now.Add(100*24*time.Hour))
		reg := newReg(t, Options{})
		res := reg.Attempt(ctx, domain.KindTLS, spec(addr))
		if res.OK || !strings.Contains(res.Reason, "x509") {
			t.Fatalf("got ok=%v reason=%q", res.OK, res.Reason)
		}
	})
}

func TestICMPCheck(t *testing.T) {
	reg := newReg(t, Options{})
	spec := pull(domain.PullSpec{Timeout: domain.MustDuration("2s"), ICMP: &domain.ICMPCheck{Host: "127.0.0.1", Count: 2}})
	res := reg.Attempt(context.Background(), domain.KindICMP, spec)
	if !res.OK && strings.Contains(res.Reason, "icmp unavailable") {
		t.Skip(res.Reason)
	}
	if !res.OK || fmt.Sprint(res.Detail["received"]) != "2" || res.Detail["addr"] != "127.0.0.1" {
		t.Fatalf("ping loopback: %+v", res)
	}
	strict, err := NewRegistry(Options{Outbound: outbound.Options{AllowPrivateTargets: false}})
	if err != nil {
		t.Fatal(err)
	}
	if res := strict.Attempt(context.Background(), domain.KindICMP, spec); res.OK || !strings.Contains(res.Reason, "refusing private target") {
		t.Fatalf("private guard: %+v", res)
	}
}

func TestRegistry(t *testing.T) {
	reg := newReg(t, Options{})
	if got := fmt.Sprint(reg.Kinds()); got != "[dns http icmp tcp tls]" {
		t.Errorf("kinds: %s", got)
	}
	if res := reg.Attempt(context.Background(), domain.KindHeartbeat, pull(domain.PullSpec{})); res.OK || res.Reason != "no checker for kind heartbeat" {
		t.Errorf("unknown kind: %+v", res)
	}
	if res := reg.Attempt(context.Background(), domain.KindHTTP, nil); res.OK || res.Reason != "monitor has no spec" {
		t.Errorf("nil spec: %+v", res)
	}
	if err := reg.Validate(domain.KindHTTP, []byte(`{"http":{"url":"https://example.com"}}`)); err != nil {
		t.Errorf("valid spec: %v", err)
	}
	if err := reg.Validate(domain.KindTCP, []byte(`{"tcp":{"host":"db","port":0}}`)); err == nil || !strings.Contains(err.Error(), "tcp.port") {
		t.Errorf("invalid spec: %v", err)
	}
	if err := reg.Validate(domain.KindHeartbeat, []byte(`{}`)); err == nil {
		t.Error("heartbeat is not a checker kind")
	}
}

func TestJSONPath(t *testing.T) {
	doc := map[string]any{"a": map[string]any{"b": []any{1.0, map[string]any{"c": "x"}}}, "k y": true}
	cases := []struct {
		path  string
		want  string
		found bool
		err   bool
	}{
		{"$", "map[a:map[b:[1 map[c:x]]] k y:true]", true, false},
		{"$.a.b[1].c", "x", true, false},
		{"$.a.b[0]", "1", true, false},
		{"$.a.b[-1].c", "x", true, false},
		{`$["k y"]`, "true", true, false},
		{"$.a.zz", "", false, false},
		{"$.a.b[9]", "", false, false},
		{"a.b", "", false, true},
		{"$.a.b[x]", "", false, true},
	}
	for _, c := range cases {
		v, found, err := jsonPath(doc, c.path)
		if (err != nil) != c.err || found != c.found || (found && fmt.Sprint(v) != c.want) {
			t.Errorf("%s: v=%v found=%v err=%v", c.path, v, found, err)
		}
	}
}
