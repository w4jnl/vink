package notify

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
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

// newHTTPClient builds the one client every HTTP notifier uses.
func newHTTPClient(o Options) (*http.Client, error) {
	tr := &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		MaxIdleConns:          8,
		IdleConnTimeout:       60 * time.Second,
	}
	if o.Proxy != "" {
		u, err := url.Parse(o.Proxy)
		if err != nil {
			return nil, fmt.Errorf("outbound.proxy: %w", err)
		}
		tr.Proxy = http.ProxyURL(u)
	}
	if o.CAPem != "" {
		pem, err := os.ReadFile(o.CAPem)
		if err != nil {
			return nil, fmt.Errorf("outbound.ca_pem: %w", err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("outbound.ca_pem: no certificates found")
		}
		tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	if !o.AllowPrivateTargets {
		base := tr.DialContext
		tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, _, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			for _, ip := range ips {
				if isPrivate(ip.IP) {
					return nil, fmt.Errorf("refusing private target %s (%s); set outbound.allow_private_targets = true", host, ip.IP)
				}
			}
			return base(ctx, network, addr)
		}
	}
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &http.Client{
		Transport: tr, Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			return nil
		},
	}, nil
}

func isPrivate(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}

// userAgent is the header every HTTP notifier sends.
func userAgent(o string) string {
	if strings.TrimSpace(o) == "" {
		return "vink"
	}
	return o
}
