package domain

import (
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Defaults for pull checks, from the design document.
const (
	DefaultInterval       = Duration(60 * time.Second)
	MinInterval           = Duration(10 * time.Second)
	MaxInterval           = Duration(24 * time.Hour)
	DefaultCheckTimeout   = Duration(10 * time.Second)
	DefaultPullThreshold  = 3
	DefaultConfirmRetries = 2
	DefaultConfirmDelay   = Duration(5 * time.Second)
	MaxConfirmRetries     = 5
	MaxConfirmDelay       = Duration(60 * time.Second)
	DefaultTLSWarnDays    = 14
	DefaultTLSCritDays    = 3
	DefaultICMPCount      = 3
	MaxICMPCount          = 10
	DefaultICMPLoss       = 0.67
	// MaxCheckBody caps how much of an HTTP body a check reads.
	MaxCheckBody = 1 << 20
)

// HTTPMethods are the methods an http check may use.
var HTTPMethods = []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"}

// DNSTypes are the record types a dns check can ask for.
var DNSTypes = []string{"A", "AAAA", "CNAME", "MX", "NS", "TXT", "PTR", "SRV"}

// Confirm retries a failed attempt before it counts as a failure.
type Confirm struct {
	Retries int      `json:"retries" yaml:"retries"`
	Delay   Duration `json:"delay" yaml:"delay"`
}

// IsZero reports whether the block was left out.
func (c Confirm) IsZero() bool { return c.Retries == 0 && c.Delay == 0 }

// PullSpec is the kind-specific part of an http, tcp, dns, tls or icmp
// monitor: the common cadence plus exactly one check block. JSON and
// YAML use the flat form from the design document.
type PullSpec struct {
	Interval          Duration `json:"interval" yaml:"interval"`
	Timeout           Duration `json:"timeout" yaml:"timeout"`
	FailureThreshold  int      `json:"failure_threshold" yaml:"failure_threshold"`
	Confirm           Confirm  `json:"confirm" yaml:"confirm"`
	RecoveryThreshold int      `json:"recovery_threshold" yaml:"recovery_threshold"`
	// Location is local or an agent id (phase 2).
	Location string `json:"location,omitempty" yaml:"location,omitempty"`

	HTTP *HTTPCheck `json:"http,omitempty" yaml:"http,omitempty"`
	TCP  *TCPCheck  `json:"tcp,omitempty" yaml:"tcp,omitempty"`
	DNS  *DNSCheck  `json:"dns,omitempty" yaml:"dns,omitempty"`
	TLS  *TLSCheck  `json:"tls,omitempty" yaml:"tls,omitempty"`
	ICMP *ICMPCheck `json:"icmp,omitempty" yaml:"icmp,omitempty"`
}

// StatusRange is one entry of expect_status: 200 or 200-299. JSON takes
// a number or a "lo-hi" string and writes the shorter of the two back.
type StatusRange struct {
	Lo, Hi int
}

// Contains reports whether code falls in the range.
func (r StatusRange) Contains(code int) bool { return code >= r.Lo && code <= r.Hi }

func (r StatusRange) String() string {
	if r.Lo == r.Hi {
		return strconv.Itoa(r.Lo)
	}
	return strconv.Itoa(r.Lo) + "-" + strconv.Itoa(r.Hi)
}

// ParseStatusRange reads "200" or "200-299".
func ParseStatusRange(s string) (StatusRange, error) {
	s = strings.TrimSpace(s)
	lo, hi, ok := strings.Cut(s, "-")
	if !ok {
		hi = lo
	}
	l, err := strconv.Atoi(strings.TrimSpace(lo))
	if err != nil {
		return StatusRange{}, fmt.Errorf("invalid status %q", s)
	}
	h, err := strconv.Atoi(strings.TrimSpace(hi))
	if err != nil {
		return StatusRange{}, fmt.Errorf("invalid status %q", s)
	}
	return StatusRange{Lo: l, Hi: h}, nil
}

func (r StatusRange) MarshalJSON() ([]byte, error) {
	if r.Lo == r.Hi {
		return json.Marshal(r.Lo)
	}
	return json.Marshal(r.String())
}

func (r *StatusRange) UnmarshalJSON(b []byte) error {
	var n int
	if err := json.Unmarshal(b, &n); err == nil {
		*r = StatusRange{Lo: n, Hi: n}
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("expect_status wants a number or \"lo-hi\", got %s", string(b))
	}
	parsed, err := ParseStatusRange(s)
	if err != nil {
		return err
	}
	*r = parsed
	return nil
}

// JSONPathExpect compares one value of a JSON body.
type JSONPathExpect struct {
	Path   string `json:"path" yaml:"path"`
	Equals any    `json:"equals" yaml:"equals"`
}

// ExpectBody is one of contains, not_contains or jsonpath; several may
// combine and all must hold.
type ExpectBody struct {
	Contains    string          `json:"contains,omitempty" yaml:"contains,omitempty"`
	NotContains string          `json:"not_contains,omitempty" yaml:"not_contains,omitempty"`
	JSONPath    *JSONPathExpect `json:"jsonpath,omitempty" yaml:"jsonpath,omitempty"`
}

// IsZero reports whether no expectation is set.
func (e *ExpectBody) IsZero() bool {
	return e == nil || (e.Contains == "" && e.NotContains == "" && e.JSONPath == nil)
}

// HTTPCheck requests a URL and judges status and body.
type HTTPCheck struct {
	URL             string            `json:"url" yaml:"url"`
	Method          string            `json:"method,omitempty" yaml:"method,omitempty"`
	Headers         map[string]string `json:"headers,omitempty" yaml:"headers,flow,omitempty"`
	Body            string            `json:"body,omitempty" yaml:"body,omitempty"`
	ExpectStatus    []StatusRange     `json:"expect_status,omitempty" yaml:"expect_status,flow,omitempty"`
	ExpectBody      *ExpectBody       `json:"expect_body,omitempty" yaml:"expect_body,flow,omitempty"`
	FollowRedirects *bool             `json:"follow_redirects,omitempty" yaml:"follow_redirects,omitempty"`
	VerifyTLS       *bool             `json:"verify_tls,omitempty" yaml:"verify_tls,omitempty"`
	// CAPem holds extra CA certificates for a private CA.
	CAPem string `json:"ca_pem,omitempty" yaml:"ca_pem,omitempty"`
}

// Redirects reports whether the check follows redirects (default true).
func (h *HTTPCheck) Redirects() bool { return h.FollowRedirects == nil || *h.FollowRedirects }

// Verify reports whether the check verifies the server certificate (default true).
func (h *HTTPCheck) Verify() bool { return h.VerifyTLS == nil || *h.VerifyTLS }

// TCPCheck connects and optionally matches a banner.
type TCPCheck struct {
	Host   string `json:"host" yaml:"host"`
	Port   int    `json:"port" yaml:"port"`
	Send   string `json:"send,omitempty" yaml:"send,omitempty"`
	Expect string `json:"expect,omitempty" yaml:"expect,omitempty"`
}

// DNSCheck resolves a name and optionally expects answers.
type DNSCheck struct {
	Name     string   `json:"name" yaml:"name"`
	Type     string   `json:"type,omitempty" yaml:"type,omitempty"`
	Resolver string   `json:"resolver,omitempty" yaml:"resolver,omitempty"`
	Expect   []string `json:"expect,omitempty" yaml:"expect,flow,omitempty"`
}

// TLSCheck handshakes and watches the certificate's expiry.
type TLSCheck struct {
	Host       string `json:"host" yaml:"host"`
	Port       int    `json:"port,omitempty" yaml:"port,omitempty"`
	ServerName string `json:"servername,omitempty" yaml:"servername,omitempty"`
	WarnDays   int    `json:"warn_days,omitempty" yaml:"warn_days,omitempty"`
	CritDays   int    `json:"crit_days,omitempty" yaml:"crit_days,omitempty"`
}

// ICMPCheck pings a host.
type ICMPCheck struct {
	Host          string  `json:"host" yaml:"host"`
	Count         int     `json:"count,omitempty" yaml:"count,omitempty"`
	LossThreshold float64 `json:"loss_threshold,omitempty" yaml:"loss_threshold,omitempty"`
}

// Normalize fills defaults and canonicalises fields. Call before Validate.
func (s *PullSpec) Normalize() {
	if s.Interval == 0 {
		s.Interval = DefaultInterval
	}
	if s.Timeout == 0 {
		s.Timeout = DefaultCheckTimeout
		if s.Timeout >= s.Interval {
			// A short interval needs a shorter default timeout.
			s.Timeout = s.Interval / 2
		}
	}
	if s.FailureThreshold == 0 {
		s.FailureThreshold = DefaultPullThreshold
	}
	if s.RecoveryThreshold == 0 {
		s.RecoveryThreshold = DefaultThreshold
	}
	if s.Confirm.IsZero() {
		s.Confirm = Confirm{Retries: DefaultConfirmRetries, Delay: DefaultConfirmDelay}
	}
	s.Location = strings.TrimSpace(s.Location)
	if s.Location == "local" {
		s.Location = ""
	}
	if h := s.HTTP; h != nil {
		h.URL = strings.TrimSpace(h.URL)
		h.Method = strings.ToUpper(strings.TrimSpace(h.Method))
		if h.Method == "" {
			h.Method = "GET"
		}
		if len(h.ExpectStatus) == 0 {
			h.ExpectStatus = []StatusRange{{Lo: 200, Hi: 299}}
		}
		if h.ExpectBody.IsZero() {
			h.ExpectBody = nil
		}
		t := true
		if h.FollowRedirects == nil {
			h.FollowRedirects = &t
		}
		if h.VerifyTLS == nil {
			h.VerifyTLS = &t
		}
		h.CAPem = strings.TrimSpace(h.CAPem)
	}
	if t := s.TCP; t != nil {
		t.Host = strings.TrimSpace(t.Host)
	}
	if d := s.DNS; d != nil {
		d.Name = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(d.Name)), ".")
		d.Type = strings.ToUpper(strings.TrimSpace(d.Type))
		if d.Type == "" {
			d.Type = "A"
		}
		d.Resolver = strings.TrimSpace(d.Resolver)
		for i, e := range d.Expect {
			d.Expect[i] = strings.TrimSpace(e)
		}
	}
	if t := s.TLS; t != nil {
		t.Host = strings.TrimSpace(t.Host)
		t.ServerName = strings.TrimSpace(t.ServerName)
		if t.Port == 0 {
			t.Port = 443
		}
		if t.WarnDays == 0 {
			t.WarnDays = DefaultTLSWarnDays
		}
		if t.CritDays == 0 {
			t.CritDays = DefaultTLSCritDays
		}
	}
	if i := s.ICMP; i != nil {
		i.Host = strings.TrimSpace(i.Host)
		if i.Count == 0 {
			i.Count = DefaultICMPCount
		}
		if i.LossThreshold == 0 {
			i.LossThreshold = DefaultICMPLoss
		}
	}
}

// Validate checks the cadence and the one block the kind needs. Field
// names match the JSON representation ("http.url").
func (s PullSpec) Validate(kind Kind) error {
	ve := &ValidationError{}
	if s.Interval < MinInterval {
		ve.Addf("interval", "must be at least %s", MinInterval)
	} else if s.Interval > MaxInterval {
		ve.Addf("interval", "must be at most %s", MaxInterval)
	}
	if s.Timeout <= 0 {
		ve.Add("timeout", "must be positive")
	} else if s.Timeout >= s.Interval && s.Interval >= MinInterval {
		ve.Add("timeout", "must be shorter than the interval")
	}
	if s.FailureThreshold < 1 || s.FailureThreshold > MaxThreshold {
		ve.Addf("failure_threshold", "must be between 1 and %d", MaxThreshold)
	}
	if s.RecoveryThreshold < 1 || s.RecoveryThreshold > MaxThreshold {
		ve.Addf("recovery_threshold", "must be between 1 and %d", MaxThreshold)
	}
	if s.Confirm.Retries < 0 || s.Confirm.Retries > MaxConfirmRetries {
		ve.Addf("confirm.retries", "must be between 0 and %d", MaxConfirmRetries)
	}
	if s.Confirm.Delay < 0 || s.Confirm.Delay > MaxConfirmDelay {
		ve.Addf("confirm.delay", "must be between 0 and %s", MaxConfirmDelay)
	}
	if s.Location != "" {
		ve.Add("location", "agents arrive in phase 2; leave it empty for a local check")
	}
	blocks := map[Kind]bool{KindHTTP: s.HTTP != nil, KindTCP: s.TCP != nil, KindDNS: s.DNS != nil, KindTLS: s.TLS != nil, KindICMP: s.ICMP != nil}
	for k, set := range blocks {
		if set && k != kind {
			ve.Addf(string(k), "a %s monitor does not take a %s block", string(kind), string(k))
		}
	}
	if set, ok := blocks[kind]; ok && !set {
		ve.Addf(string(kind), "set the %s block", string(kind))
	}
	switch {
	case kind == KindHTTP && s.HTTP != nil:
		s.HTTP.validate(ve)
	case kind == KindTCP && s.TCP != nil:
		validateHost(ve, "tcp.host", s.TCP.Host)
		validatePort(ve, "tcp.port", s.TCP.Port)
	case kind == KindDNS && s.DNS != nil:
		s.DNS.validate(ve)
	case kind == KindTLS && s.TLS != nil:
		validateHost(ve, "tls.host", s.TLS.Host)
		validatePort(ve, "tls.port", s.TLS.Port)
		if s.TLS.WarnDays < 0 || s.TLS.CritDays < 0 {
			ve.Add("tls.warn_days", "days must not be negative")
		} else if s.TLS.CritDays > s.TLS.WarnDays {
			ve.Add("tls.crit_days", "must not exceed warn_days")
		}
	case kind == KindICMP && s.ICMP != nil:
		validateHost(ve, "icmp.host", s.ICMP.Host)
		if s.ICMP.Count < 1 || s.ICMP.Count > MaxICMPCount {
			ve.Addf("icmp.count", "must be between 1 and %d", MaxICMPCount)
		}
		if s.ICMP.LossThreshold <= 0 || s.ICMP.LossThreshold > 1 {
			ve.Add("icmp.loss_threshold", "must be above 0 and at most 1")
		}
	}
	return ve.OrNil()
}

func (h *HTTPCheck) validate(ve *ValidationError) {
	u, err := url.Parse(h.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		ve.Add("http.url", "must be an absolute http(s) URL")
	}
	ok := false
	for _, m := range HTTPMethods {
		ok = ok || m == h.Method
	}
	if !ok {
		ve.Addf("http.method", "must be one of %s", strings.Join(HTTPMethods, ", "))
	}
	for k := range h.Headers {
		if strings.TrimSpace(k) == "" || strings.ContainsAny(k, " :\r\n") {
			ve.Addf("http.headers", "%q is not a header name", k)
		}
	}
	for _, r := range h.ExpectStatus {
		if r.Lo < 100 || r.Hi > 599 || r.Lo > r.Hi {
			ve.Addf("http.expect_status", "%s is not a status range between 100 and 599", r)
		}
	}
	if jp := h.ExpectBody; jp != nil && jp.JSONPath != nil {
		if !strings.HasPrefix(jp.JSONPath.Path, "$") {
			ve.Add("http.expect_body", "jsonpath.path must start with $")
		}
	}
	if h.CAPem != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(h.CAPem)) {
			ve.Add("http.ca_pem", "no certificates found in the PEM")
		}
	}
}

func (d *DNSCheck) validate(ve *ValidationError) {
	if d.Name == "" {
		ve.Add("dns.name", "must not be empty")
	}
	ok := false
	for _, t := range DNSTypes {
		ok = ok || t == d.Type
	}
	if !ok {
		ve.Addf("dns.type", "must be one of %s", strings.Join(DNSTypes, ", "))
	}
	if d.Resolver != "" {
		host, port, err := net.SplitHostPort(d.Resolver)
		if err != nil {
			host, port = d.Resolver, "53"
		}
		if host == "" {
			ve.Add("dns.resolver", "must be host or host:port")
		}
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			ve.Add("dns.resolver", "port must be between 1 and 65535")
		}
	}
	for _, e := range d.Expect {
		if e == "" {
			ve.Add("dns.expect", "must not contain empty values")
			break
		}
	}
}

func validateHost(ve *ValidationError, field, host string) {
	if host == "" {
		ve.Add(field, "must not be empty")
	} else if strings.ContainsAny(host, " /\\") {
		ve.Add(field, "must be a hostname or IP address")
	}
}

func validatePort(ve *ValidationError, field string, port int) {
	if port < 1 || port > 65535 {
		ve.Add(field, "must be between 1 and 65535")
	}
}

// ResolverAddr returns the resolver as host:port, or "" for the system one.
func (d *DNSCheck) ResolverAddr() string {
	if d.Resolver == "" {
		return ""
	}
	if _, _, err := net.SplitHostPort(d.Resolver); err == nil {
		return d.Resolver
	}
	return net.JoinHostPort(d.Resolver, "53")
}

// Target is what the monitor checks, for lists and logs.
func (s *PullSpec) Target() string {
	switch {
	case s.HTTP != nil:
		return s.HTTP.URL
	case s.TCP != nil:
		return net.JoinHostPort(s.TCP.Host, strconv.Itoa(s.TCP.Port))
	case s.DNS != nil:
		return s.DNS.Name + " " + s.DNS.Type
	case s.TLS != nil:
		return net.JoinHostPort(s.TLS.Host, strconv.Itoa(s.TLS.Port))
	case s.ICMP != nil:
		return s.ICMP.Host
	}
	return ""
}

// ParsePullSpec decodes and normalises a stored spec.
func ParsePullSpec(raw []byte) (*PullSpec, error) {
	var s PullSpec
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("decode pull spec: %w", err)
	}
	s.Normalize()
	return &s, nil
}

// IsPull reports whether the kind is checked by vink rather than pinged.
func (k Kind) IsPull() bool {
	switch k {
	case KindHTTP, KindTCP, KindDNS, KindTLS, KindICMP:
		return true
	}
	return false
}

// MarshalYAML writes 200 or "200-299".
func (r StatusRange) MarshalYAML() (any, error) {
	if r.Lo == r.Hi {
		return r.Lo, nil
	}
	return r.String(), nil
}
