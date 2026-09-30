package checks

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/w4jnl/vink/internal/domain"
)

// TLS handshakes and watches the certificate's expiry: warn_days makes
// the monitor late, crit_days or a failed handshake makes it down.
type TLS struct{}

func (TLS) Kind() domain.Kind { return domain.KindTLS }

func (TLS) Check(ctx context.Context, spec *domain.PullSpec, env Env) Result {
	t := spec.TLS
	if t == nil {
		return fail("no tls block")
	}
	addr := net.JoinHostPort(t.Host, strconv.Itoa(t.Port))
	sni := t.ServerName
	if sni == "" {
		sni = t.Host
	}
	start := time.Now()
	raw, err := env.Out.Dial(ctx, "tcp", addr)
	if err != nil {
		return fail(reasonFor(err, spec.Timeout.Std()))
	}
	defer func() { _ = raw.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		_ = raw.SetDeadline(dl)
	}
	conn := tls.Client(raw, &tls.Config{ServerName: sni, RootCAs: env.Out.RootCAs, MinVersion: tls.VersionTLS12})
	if err := conn.HandshakeContext(ctx); err != nil {
		return fail(reasonFor(err, spec.Timeout.Std()))
	}
	res := Result{LatencyMs: ms(start), Detail: map[string]any{}}
	state := conn.ConnectionState()
	res.Detail["version"] = tls.VersionName(state.Version)
	res.Detail["cipher"] = tls.CipherSuiteName(state.CipherSuite)
	if len(state.PeerCertificates) == 0 {
		res.Reason = "no certificate presented"
		return res
	}
	leaf := state.PeerCertificates[0]
	now := env.Now()
	days := daysUntil(leaf.NotAfter, now)
	res.Detail["subject"] = leaf.Subject.CommonName
	res.Detail["issuer"] = leaf.Issuer.CommonName
	res.Detail["not_after"] = leaf.NotAfter.UTC().Format(time.RFC3339)
	res.Detail["days_left"] = days
	if len(leaf.DNSNames) > 0 {
		res.Detail["dns_names"] = leaf.DNSNames
	}
	switch {
	case now.After(leaf.NotAfter):
		res.Reason = fmt.Sprintf("certificate expired %s ago", dayWord(-days))
	case days <= t.CritDays:
		res.Reason = "certificate expires in " + dayWord(days)
	default:
		res.OK = true
		if days <= t.WarnDays {
			res.Warn = true
			res.Reason = "certificate expires in " + dayWord(days)
		}
	}
	return res
}

func dayWord(days int) string {
	if days <= 0 {
		return "under a day"
	}
	return strconv.Itoa(days) + " d"
}
