// Package checks runs pull monitors: one attempt against a target and a
// verdict with a latency, a one-line reason and structured detail. The
// pool in the engine drives it; the agent (phase 2) reuses it.
package checks

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/outbound"
)

// Result is one attempt's verdict.
type Result struct {
	OK bool
	// Warn marks a passing attempt that should show as late: a
	// certificate inside warn_days.
	Warn      bool
	LatencyMs int64
	// Reason is the one line the UI and alerts show when not OK (or Warn).
	Reason string
	// Detail is stored with the observation: status code, matched
	// keyword, certificate expiry, answers.
	Detail map[string]any
}

// Env is what every checker gets: the outbound environment and identity.
type Env struct {
	Out       *outbound.Env
	UserAgent string
	// Now is the clock for expiry maths; latency uses the monotonic clock.
	Now func() time.Time
}

// Checker runs one attempt for one kind.
type Checker interface {
	Kind() domain.Kind
	Check(ctx context.Context, spec *domain.PullSpec, env Env) Result
}

// Options configure the registry.
type Options struct {
	Outbound  outbound.Options
	UserAgent string
}

// Registry maps kinds to checkers and owns the shared environment.
type Registry struct {
	checkers map[domain.Kind]Checker
	env      Env
}

// NewRegistry registers the phase 1 checkers: http, tcp, dns, tls, icmp.
func NewRegistry(o Options) (*Registry, error) {
	out, err := outbound.New(o.Outbound)
	if err != nil {
		return nil, err
	}
	ua := strings.TrimSpace(o.UserAgent)
	if ua == "" {
		ua = "vink"
	}
	r := &Registry{checkers: map[domain.Kind]Checker{}, env: Env{Out: out, UserAgent: ua, Now: time.Now}}
	r.Register(HTTP{})
	r.Register(TCP{})
	r.Register(DNS{})
	r.Register(TLS{})
	r.Register(ICMP{})
	return r, nil
}

// Register adds or replaces a checker.
func (r *Registry) Register(c Checker) { r.checkers[c.Kind()] = c }

// SetClock replaces the clock used for expiry maths.
func (r *Registry) SetClock(now func() time.Time) { r.env.Now = now }

// Env returns the shared environment.
func (r *Registry) Env() Env { return r.env }

// Kinds lists the registered kinds in order.
func (r *Registry) Kinds() []domain.Kind {
	kinds := make([]domain.Kind, 0, len(r.checkers))
	for k := range r.checkers {
		kinds = append(kinds, k)
	}
	sort.Slice(kinds, func(i, j int) bool { return kinds[i] < kinds[j] })
	return kinds
}

// Validate decodes a stored or posted spec for kind and checks it, the
// way the service does before saving; the agent uses it on assignment.
func (r *Registry) Validate(kind domain.Kind, raw []byte) error {
	if _, ok := r.checkers[kind]; !ok {
		return fmt.Errorf("no checker for kind %s", kind)
	}
	spec, err := domain.ParsePullSpec(raw)
	if err != nil {
		return err
	}
	return spec.Validate(kind)
}

// Attempt runs one attempt under the spec's timeout. It always returns a
// Result; an unknown kind or a nil spec is a failed attempt with a reason.
func (r *Registry) Attempt(ctx context.Context, kind domain.Kind, spec *domain.PullSpec) Result {
	c, ok := r.checkers[kind]
	if !ok {
		return fail("no checker for kind " + string(kind))
	}
	if spec == nil {
		return fail("monitor has no spec")
	}
	timeout := spec.Timeout.Std()
	if timeout <= 0 {
		timeout = domain.DefaultCheckTimeout.Std()
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()
	res := c.Check(ctx, spec, r.env)
	if res.LatencyMs == 0 && !res.OK {
		res.LatencyMs = ms(start)
	}
	if !res.OK && errors.Is(ctx.Err(), context.DeadlineExceeded) && (res.Reason == "" || isTimeoutText(res.Reason)) {
		res.Reason = "timeout after " + domain.Duration(timeout).String()
	}
	if res.Detail == nil {
		res.Detail = map[string]any{}
	}
	return res
}

func fail(reason string) Result {
	return Result{Reason: reason, Detail: map[string]any{}}
}

func ms(start time.Time) int64 {
	return time.Since(start).Milliseconds()
}

func isTimeoutText(s string) bool {
	s = strings.ToLower(s)
	return strings.Contains(s, "timeout") || strings.Contains(s, "deadline exceeded") || strings.Contains(s, "i/o timeout")
}

// reasonFor turns a transport error into the one line a person reads:
// the URL wrapper and the operation prefix go, timeouts get one word.
func reasonFor(err error, timeout time.Duration) string {
	var refused outbound.RefusedError
	if errors.As(err, &refused) {
		return refused.Error()
	}
	if errors.Is(err, context.DeadlineExceeded) || os.IsTimeout(err) {
		return "timeout after " + domain.Duration(timeout).String()
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "timeout after " + domain.Duration(timeout).String()
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		return reasonFor(ue.Err, timeout)
	}
	var de *net.DNSError
	if errors.As(err, &de) {
		if de.IsNotFound {
			return "no such host " + de.Name
		}
		return "lookup " + de.Name + ": " + de.Err
	}
	return strings.TrimSpace(err.Error())
}

// quote renders a value the way the reason lines show it: strings
// quoted, numbers and booleans plain.
func quote(v any) string {
	if s, ok := v.(string); ok {
		return fmt.Sprintf("%q", s)
	}
	return fmt.Sprint(v)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
