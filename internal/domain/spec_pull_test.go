package domain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestPullSpecNormalizeDefaults(t *testing.T) {
	s := PullSpec{HTTP: &HTTPCheck{URL: " https://example.com/health "}, TLS: nil}
	s.Normalize()
	if s.Interval != DefaultInterval || s.Timeout != DefaultCheckTimeout || s.FailureThreshold != 3 || s.RecoveryThreshold != 1 {
		t.Errorf("cadence defaults: %+v", s)
	}
	if s.Confirm.Retries != 2 || s.Confirm.Delay != Duration(5*time.Second) {
		t.Errorf("confirm defaults: %+v", s.Confirm)
	}
	h := s.HTTP
	if h.URL != "https://example.com/health" || h.Method != "GET" || len(h.ExpectStatus) != 1 || h.ExpectStatus[0] != (StatusRange{200, 299}) || !h.Redirects() || !h.Verify() {
		t.Errorf("http defaults: %+v", h)
	}
	short := PullSpec{Interval: MinInterval, TCP: &TCPCheck{Host: "db", Port: 1}}
	short.Normalize()
	if short.Timeout != Duration(5*time.Second) || short.Validate(KindTCP) != nil {
		t.Errorf("short interval must shrink the default timeout: %s %v", short.Timeout, short.Validate(KindTCP))
	}
	tl := PullSpec{TLS: &TLSCheck{Host: "example.com"}}
	tl.Normalize()
	if tl.TLS.Port != 443 || tl.TLS.WarnDays != 14 || tl.TLS.CritDays != 3 {
		t.Errorf("tls defaults: %+v", tl.TLS)
	}
	ic := PullSpec{ICMP: &ICMPCheck{Host: "10.0.0.1"}}
	ic.Normalize()
	if ic.ICMP.Count != 3 || ic.ICMP.LossThreshold != 0.67 {
		t.Errorf("icmp defaults: %+v", ic.ICMP)
	}
	d := PullSpec{DNS: &DNSCheck{Name: "Example.COM."}}
	d.Normalize()
	if d.DNS.Type != "A" || d.DNS.Name != "example.com" {
		t.Errorf("dns defaults: %+v", d.DNS)
	}
}

func TestPullSpecValidate(t *testing.T) {
	f := false
	cases := []struct {
		name  string
		kind  Kind
		spec  PullSpec
		field string // "" means valid
	}{
		{"http ok", KindHTTP, PullSpec{HTTP: &HTTPCheck{URL: "https://example.com"}}, ""},
		{"http with body checks", KindHTTP, PullSpec{HTTP: &HTTPCheck{URL: "https://example.com", Method: "post", ExpectStatus: []StatusRange{{200, 200}, {300, 399}}, ExpectBody: &ExpectBody{JSONPath: &JSONPathExpect{Path: "$.status", Equals: "ok"}}, FollowRedirects: &f}}, ""},
		{"interval short", KindHTTP, PullSpec{Interval: Duration(5 * time.Second), HTTP: &HTTPCheck{URL: "https://example.com"}}, "interval"},
		{"timeout too long", KindHTTP, PullSpec{Interval: Duration(30 * time.Second), Timeout: Duration(30 * time.Second), HTTP: &HTTPCheck{URL: "https://example.com"}}, "timeout"},
		{"wrong block", KindHTTP, PullSpec{HTTP: &HTTPCheck{URL: "https://example.com"}, TCP: &TCPCheck{Host: "x", Port: 1}}, "tcp"},
		{"missing block", KindTCP, PullSpec{}, "tcp"},
		{"bad url", KindHTTP, PullSpec{HTTP: &HTTPCheck{URL: "ftp://example.com"}}, "http.url"},
		{"bad method", KindHTTP, PullSpec{HTTP: &HTTPCheck{URL: "https://example.com", Method: "BREW"}}, "http.method"},
		{"bad status", KindHTTP, PullSpec{HTTP: &HTTPCheck{URL: "https://example.com", ExpectStatus: []StatusRange{{600, 601}}}}, "http.expect_status"},
		{"bad jsonpath", KindHTTP, PullSpec{HTTP: &HTTPCheck{URL: "https://example.com", ExpectBody: &ExpectBody{JSONPath: &JSONPathExpect{Path: "status"}}}}, "http.expect_body"},
		{"bad ca", KindHTTP, PullSpec{HTTP: &HTTPCheck{URL: "https://example.com", CAPem: "not pem"}}, "http.ca_pem"},
		{"bad header", KindHTTP, PullSpec{HTTP: &HTTPCheck{URL: "https://example.com", Headers: map[string]string{"X Y": "1"}}}, "http.headers"},
		{"tcp ok", KindTCP, PullSpec{TCP: &TCPCheck{Host: "db.lan", Port: 5432}}, ""},
		{"tcp port", KindTCP, PullSpec{TCP: &TCPCheck{Host: "db.lan", Port: 70000}}, "tcp.port"},
		{"dns ok", KindDNS, PullSpec{DNS: &DNSCheck{Name: "example.com", Type: "MX", Resolver: "10.0.0.53", Expect: []string{"mail.example.com"}}}, ""},
		{"dns type", KindDNS, PullSpec{DNS: &DNSCheck{Name: "example.com", Type: "SPF"}}, "dns.type"},
		{"dns resolver port", KindDNS, PullSpec{DNS: &DNSCheck{Name: "example.com", Resolver: "10.0.0.53:99999"}}, "dns.resolver"},
		{"tls ok", KindTLS, PullSpec{TLS: &TLSCheck{Host: "example.com"}}, ""},
		{"tls crit over warn", KindTLS, PullSpec{TLS: &TLSCheck{Host: "example.com", WarnDays: 3, CritDays: 14}}, "tls.crit_days"},
		{"icmp ok", KindICMP, PullSpec{ICMP: &ICMPCheck{Host: "10.0.0.1"}}, ""},
		{"icmp loss", KindICMP, PullSpec{ICMP: &ICMPCheck{Host: "10.0.0.1", LossThreshold: 2}}, "icmp.loss_threshold"},
		{"icmp count", KindICMP, PullSpec{ICMP: &ICMPCheck{Host: "10.0.0.1", Count: 50}}, "icmp.count"},
		{"location agent", KindHTTP, PullSpec{Location: "dc1", HTTP: &HTTPCheck{URL: "https://example.com"}}, ""},
		{"location selector", KindHTTP, PullSpec{Location: "site=dc1,zone=dmz", HTTP: &HTTPCheck{URL: "https://example.com"}}, ""},
		{"location bad", KindHTTP, PullSpec{Location: "DC 1", HTTP: &HTTPCheck{URL: "https://example.com"}}, "location"},
		{"location bad selector", KindHTTP, PullSpec{Location: "site=", HTTP: &HTTPCheck{URL: "https://example.com"}}, "location"},
		{"confirm retries", KindHTTP, PullSpec{Confirm: Confirm{Retries: 9, Delay: Duration(time.Second)}, HTTP: &HTTPCheck{URL: "https://example.com"}}, "confirm.retries"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := c.spec
			s.Normalize()
			err := s.Validate(c.kind)
			if c.field == "" {
				if err != nil {
					t.Fatalf("unexpected: %v", err)
				}
				return
			}
			ve, ok := AsValidation(err)
			if !ok {
				t.Fatalf("expected validation error for %s, got %v", c.field, err)
			}
			found := false
			for _, fe := range ve.Errors {
				found = found || fe.Field == c.field
			}
			if !found {
				t.Errorf("no error on %s: %v", c.field, err)
			}
		})
	}
}

func TestStatusRangeJSON(t *testing.T) {
	var h HTTPCheck
	if err := json.Unmarshal([]byte(`{"url":"https://x","expect_status":[200,"300-399","404"]}`), &h); err != nil {
		t.Fatal(err)
	}
	if len(h.ExpectStatus) != 3 || h.ExpectStatus[1] != (StatusRange{300, 399}) || h.ExpectStatus[2] != (StatusRange{404, 404}) {
		t.Fatalf("parsed: %+v", h.ExpectStatus)
	}
	out, _ := json.Marshal(h.ExpectStatus)
	if string(out) != `[200,"300-399",404]` {
		t.Errorf("marshal: %s", out)
	}
	if err := json.Unmarshal([]byte(`[true]`), &h.ExpectStatus); err == nil || !strings.Contains(err.Error(), "expect_status") {
		t.Errorf("bad value: %v", err)
	}
}

func TestMonitorPullSpecRoundTrip(t *testing.T) {
	m := &Monitor{Slug: "api", Name: "API", Kind: KindHTTP}
	if err := m.Validate(); err == nil || !strings.Contains(err.Error(), "http") {
		t.Fatalf("missing block must fail: %v", err)
	}
	m.Pull = &PullSpec{HTTP: &HTTPCheck{URL: "https://api.example.com/healthz", ExpectBody: &ExpectBody{JSONPath: &JSONPathExpect{Path: "$.status", Equals: "ok"}}}}
	m.Normalize()
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	raw, err := m.SpecJSON()
	if err != nil {
		t.Fatal(err)
	}
	back := &Monitor{Kind: KindHTTP}
	if err := back.SetSpecJSON(raw); err != nil {
		t.Fatal(err)
	}
	if back.Pull == nil || back.Pull.HTTP.URL != m.Pull.HTTP.URL || back.Pull.HTTP.ExpectBody.JSONPath.Equals != "ok" || !back.Pull.HTTP.Verify() || back.Pull.Target() != m.Pull.HTTP.URL {
		t.Errorf("round trip: %s", raw)
	}
	y := MonitorYAML(m)
	for _, want := range []string{"kind: http", "http:", "  url: https://api.example.com/healthz", `  expect_body: {jsonpath: {path: $.status, equals: ok}}`} {
		if !strings.Contains(y, want) {
			t.Errorf("yaml lacks %q:\n%s", want, y)
		}
	}
	if strings.Contains(y, "interval") || strings.Contains(y, "method") {
		t.Errorf("defaults must stay out of the yaml:\n%s", y)
	}
}
