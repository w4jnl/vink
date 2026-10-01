// Package outbound is the one place vink opens connections to the world:
// a dialer that can refuse private targets, a transport with the
// configured proxy and CA bundle, and the resolver. Notifiers and
// checkers share it so a deployment's egress rules hold everywhere.
package outbound

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
	"time"
)

// Options come from the [outbound] section of the config.
type Options struct {
	// Proxy is an http(s) proxy URL for every outbound HTTP request.
	Proxy string
	// CAPem is a path to extra CA certificates.
	CAPem string
	// AllowPrivateTargets false refuses loopback, link-local and RFC 1918
	// destinations after resolution.
	AllowPrivateTargets bool
	// DialTimeout bounds one connection attempt (default 10 s).
	DialTimeout time.Duration
	// Observe, when set, is told about every connection vink opens:
	// kind is dial or icmp, target the address. The egress audit uses it.
	Observe func(kind, target string)
}

// DialFunc opens one connection.
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// Env is the shared outbound environment.
type Env struct {
	// Dial connects with the private-target guard applied.
	Dial DialFunc
	// DialTrusted connects without the guard, for destinations the
	// operator configured (the SMTP relay); it is still observed.
	DialTrusted DialFunc
	// Transport carries the dialer, the proxy and the CA bundle. Callers
	// that need different TLS settings clone it.
	Transport *http.Transport
	// RootCAs is the system pool plus CAPem, or nil for the system pool.
	RootCAs *x509.CertPool
	// Resolver is the system resolver.
	Resolver *net.Resolver
	// AllowPrivateTargets mirrors the option, for checks that do not dial.
	AllowPrivateTargets bool
	// ProxyFor returns the proxy for a request, or nil.
	ProxyFor func(*http.Request) (*url.URL, error)
	// Observe records an outbound attempt; never nil.
	Observe func(kind, target string)
}

// New builds the environment.
func New(o Options) (*Env, error) {
	timeout := o.DialTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	e := &Env{Resolver: net.DefaultResolver, AllowPrivateTargets: o.AllowPrivateTargets, Observe: o.Observe}
	if e.Observe == nil {
		e.Observe = func(string, string) {}
	}
	raw := (&net.Dialer{Timeout: timeout}).DialContext
	base := func(ctx context.Context, network, addr string) (net.Conn, error) {
		e.Observe("dial", network+" "+addr)
		return raw(ctx, network, addr)
	}
	e.DialTrusted = base
	if o.AllowPrivateTargets {
		e.Dial = base
	} else {
		e.Dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := e.Resolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			for _, ip := range ips {
				if IsPrivate(ip.IP) {
					return nil, RefusedError{Host: host, IP: ip.IP}
				}
			}
			var last error
			for _, ip := range ips {
				c, err := base(ctx, network, net.JoinHostPort(ip.IP.String(), port))
				if err == nil {
					return c, nil
				}
				last = err
			}
			if last == nil {
				last = fmt.Errorf("no addresses for %s", host)
			}
			return nil, last
		}
	}
	tr := &http.Transport{
		DialContext:           e.Dial,
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
		MaxIdleConns:          8,
		IdleConnTimeout:       60 * time.Second,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
	}
	if o.Proxy != "" {
		u, err := url.Parse(o.Proxy)
		if err != nil {
			return nil, fmt.Errorf("outbound.proxy: %w", err)
		}
		e.ProxyFor = http.ProxyURL(u)
		tr.Proxy = e.ProxyFor
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
		e.RootCAs = pool
		tr.TLSClientConfig.RootCAs = pool
	}
	e.Transport = tr
	return e, nil
}

// RefusedError says a target resolved to a private address.
type RefusedError struct {
	Host string
	IP   net.IP
}

func (e RefusedError) Error() string {
	return fmt.Sprintf("refusing private target %s (%s); set outbound.allow_private_targets = true", e.Host, e.IP)
}

// IsPrivate reports whether an address is loopback, private, link-local
// or unspecified.
func IsPrivate(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}

// CheckTarget resolves host and refuses it when private targets are off.
// It returns the addresses so a caller that does not dial (icmp) can use
// them.
func (e *Env) CheckTarget(ctx context.Context, host string) ([]net.IPAddr, error) {
	ips, err := e.Resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	if !e.AllowPrivateTargets {
		for _, ip := range ips {
			if IsPrivate(ip.IP) {
				return nil, RefusedError{Host: host, IP: ip.IP}
			}
		}
	}
	return ips, nil
}
