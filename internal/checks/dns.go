package checks

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/w4jnl/vink/internal/domain"
)

// DNS resolves a name, through the system resolver or a named one, and
// compares the answers.
type DNS struct{}

func (DNS) Kind() domain.Kind { return domain.KindDNS }

func (DNS) Check(ctx context.Context, spec *domain.PullSpec, env Env) Result {
	d := spec.DNS
	if d == nil {
		return fail("no dns block")
	}
	r := env.Out.Resolver
	res := Result{Detail: map[string]any{"type": d.Type}}
	if addr := d.ResolverAddr(); addr != "" {
		res.Detail["resolver"] = addr
		r = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return env.Out.Dial(ctx, network, addr)
		}}
	}
	start := time.Now()
	answers, err := lookup(ctx, r, d)
	res.LatencyMs = ms(start)
	if err != nil {
		res.Reason = reasonFor(err, spec.Timeout.Std())
		return res
	}
	sort.Strings(answers)
	res.Detail["answers"] = answers
	if len(answers) == 0 {
		res.Reason = "no " + d.Type + " records"
		return res
	}
	have := map[string]bool{}
	for _, a := range answers {
		have[normAnswer(a)] = true
	}
	for _, want := range d.Expect {
		if !have[normAnswer(want)] {
			res.Reason = fmt.Sprintf("answers %s, want %s", strings.Join(answers, ", "), want)
			return res
		}
	}
	res.OK = true
	return res
}

func normAnswer(s string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), ".")
}

func lookup(ctx context.Context, r *net.Resolver, d *domain.DNSCheck) ([]string, error) {
	var out []string
	switch d.Type {
	case "A", "AAAA":
		network := "ip4"
		if d.Type == "AAAA" {
			network = "ip6"
		}
		ips, err := r.LookupIP(ctx, network, d.Name)
		if err != nil {
			return nil, err
		}
		for _, ip := range ips {
			out = append(out, ip.String())
		}
	case "CNAME":
		cname, err := r.LookupCNAME(ctx, d.Name)
		if err != nil {
			return nil, err
		}
		if c := normAnswer(cname); c != "" && c != normAnswer(d.Name) {
			out = append(out, c)
		}
	case "MX":
		mxs, err := r.LookupMX(ctx, d.Name)
		if err != nil {
			return nil, err
		}
		for _, mx := range mxs {
			out = append(out, normAnswer(mx.Host))
		}
	case "NS":
		nss, err := r.LookupNS(ctx, d.Name)
		if err != nil {
			return nil, err
		}
		for _, ns := range nss {
			out = append(out, normAnswer(ns.Host))
		}
	case "TXT":
		txts, err := r.LookupTXT(ctx, d.Name)
		if err != nil {
			return nil, err
		}
		out = append(out, txts...)
	case "PTR":
		names, err := r.LookupAddr(ctx, d.Name)
		if err != nil {
			return nil, err
		}
		for _, n := range names {
			out = append(out, normAnswer(n))
		}
	case "SRV":
		_, srvs, err := r.LookupSRV(ctx, "", "", d.Name)
		if err != nil {
			return nil, err
		}
		for _, s := range srvs {
			out = append(out, normAnswer(s.Target)+":"+strconv.Itoa(int(s.Port)))
		}
	default:
		return nil, fmt.Errorf("unsupported record type %s", d.Type)
	}
	return out, nil
}
