package notify

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/w4jnl/vink/internal/outbound"
)

// Options configure the shared outbound client and the SMTP transport.
type Options struct {
	// Proxy is an http(s) proxy URL for every outbound HTTP request.
	Proxy string
	// CAPem is a path to extra CA certificates.
	CAPem string
	// AllowPrivateTargets false refuses loopback, link-local and RFC 1918
	// destinations after resolution.
	AllowPrivateTargets bool
	// Timeout bounds one send.
	Timeout time.Duration
	// SMTP is the instance mail transport.
	SMTP SMTPConfig
	// UserAgent identifies vink to receivers.
	UserAgent string
}

// newHTTPClient builds the one client every HTTP notifier uses, on the
// shared outbound environment.
func newHTTPClient(o Options) (*http.Client, error) {
	env, err := outbound.New(outbound.Options{Proxy: o.Proxy, CAPem: o.CAPem, AllowPrivateTargets: o.AllowPrivateTargets})
	if err != nil {
		return nil, err
	}
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &http.Client{
		Transport: env.Transport, Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			return nil
		},
	}, nil
}

// userAgent is the header every HTTP notifier sends.
func userAgent(o string) string {
	if strings.TrimSpace(o) == "" {
		return "vink"
	}
	return o
}
