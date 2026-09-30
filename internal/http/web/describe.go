package web

import (
	"fmt"
	"strings"
	"time"

	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/timefmt"
)

// hints are the sentences the form shows under its fields. One function
// computes them for the preview handler, the 422 re-render and the first
// render, so nothing depends on JavaScript.
type hints struct {
	Schedule string
	Grace    string
	Advanced string
}

// describe explains a heartbeat spec in the project's timezone.
func describe(spec *domain.HeartbeatSpec, projectTZ string, now time.Time, bodyDefault int64) hints {
	h := hints{Schedule: "How often a ping is expected.", Grace: "How long after the expected time late becomes down.", Advanced: advancedSummary(spec, bodyDefault)}
	if spec == nil {
		return h
	}
	loc, err := spec.Location(projectTZ)
	if err != nil {
		return h
	}
	sched := spec.Schedule
	if sched.IsZero() || sched.Period < 0 {
		return h
	}
	if sched.Period != 0 && sched.Period < domain.Duration(60*time.Second) {
		return h
	}
	if sched.Cron != "" {
		var runs []string
		after := now
		for range 3 {
			next, err := spec.ExpectedAfter(after, loc)
			if err != nil {
				h.Schedule = "Cron: minute hour day month weekday."
				return h
			}
			runs = append(runs, dayWord(next, now, loc))
			after = next
		}
		h.Schedule = "Next runs: " + strings.Join(runs, " · ")
	} else {
		h.Schedule = "A ping every " + sched.Period.String() + "; the clock restarts at each ping."
	}
	first, err := spec.ExpectedAfter(now, loc)
	if err == nil {
		grace := spec.Grace
		if grace == 0 {
			grace = domain.DefaultGrace
		}
		down := first.Add(grace.Std())
		h.Grace = fmt.Sprintf("Late at %s, down at %s.", timefmt.Clock(first, loc)[:5], timefmt.Clock(down, loc)[:5])
	}
	return h
}

// dayWord renders "tonight 03:00", "Wed 03:00" or "Wed 7 Oct 03:00".
func dayWord(t, now time.Time, loc *time.Location) string {
	lt, ln := t.In(loc), now.In(loc)
	clock := lt.Format("15:04")
	sameDay := lt.YearDay() == ln.YearDay() && lt.Year() == ln.Year()
	tomorrow := lt.YearDay() == ln.AddDate(0, 0, 1).YearDay() && lt.Year() == ln.AddDate(0, 0, 1).Year()
	switch {
	case sameDay:
		return "today " + clock
	case tomorrow && lt.Hour() < 6:
		return "tonight " + clock
	case tomorrow:
		return "tomorrow " + clock
	case lt.Sub(ln) < 6*24*time.Hour:
		return lt.Format("Mon") + " " + clock
	default:
		return lt.Format("Mon 2 Jan") + " " + clock
	}
}

// advancedSummary is the disclosure's one-line state:
// "max runtime none · down after 1 · methods any · body 64 KB".
func advancedSummary(spec *domain.HeartbeatSpec, bodyDefault int64) string {
	runtime, down, methods, body := "none", "1", "any", bytesWord(bodyDefault)
	if spec != nil {
		if spec.MaxRuntime > 0 {
			runtime = spec.MaxRuntime.String()
		}
		if spec.FailureThreshold > 0 {
			down = fmt.Sprint(spec.FailureThreshold)
		}
		if len(spec.Methods) > 0 {
			methods = strings.ToUpper(strings.Join(spec.Methods, ","))
			if len(spec.Methods) == 1 && spec.Methods[0] == "POST" {
				methods = "POST only"
			}
		}
		if spec.BodyLimit > 0 {
			body = bytesWord(spec.BodyLimit)
		}
	}
	return "max runtime " + runtime + " · down after " + down + " · methods " + methods + " · body " + body
}

// bytesWord renders 65536 as "64 KB".
func bytesWord(n int64) string {
	switch {
	case n <= 0:
		return "64 KB"
	case n%(1<<20) == 0:
		return fmt.Sprintf("%d MB", n>>20)
	case n%(1<<10) == 0:
		return fmt.Sprintf("%d KB", n>>10)
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// parseBytes reads "64 KB", "64KB", "1MB" or a plain count.
func parseBytes(s string) (int64, bool) {
	s = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(s), " ", ""))
	if s == "" {
		return 0, true
	}
	mult := int64(1)
	for _, suf := range []struct {
		s string
		m int64
	}{{"MB", 1 << 20}, {"KB", 1 << 10}, {"M", 1 << 20}, {"K", 1 << 10}, {"B", 1}} {
		if strings.HasSuffix(s, suf.s) {
			mult, s = suf.m, strings.TrimSuffix(s, suf.s)
			break
		}
	}
	var n int64
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int64(r-'0')
	}
	return n * mult, s != ""
}

// pullHints are the sentences of a pull form.
type pullHints struct {
	Interval string
	Timeout  string
	Failures string
	Advanced string
}

// describePull explains a pull spec: the failures sentence under the
// thresholds and the Advanced summary.
func describePull(kind domain.Kind, spec *domain.PullSpec) pullHints {
	h := pullHints{Interval: "10s or more.", Timeout: "Shorter than the interval."}
	if spec == nil {
		return h
	}
	retries := spec.Confirm.Retries
	if retries > 0 {
		h.Failures = fmt.Sprintf("A failed check is retried %d×, %s apart, before it counts. ", retries, spec.Confirm.Delay)
	} else {
		h.Failures = "A failed check counts at once. "
	}
	h.Failures += fmt.Sprintf("Down after %d counted %s, up after %d %s.", spec.FailureThreshold, plural2(spec.FailureThreshold, "failure", "failures"), spec.RecoveryThreshold, plural2(spec.RecoveryThreshold, "success", "successes"))
	var parts []string
	if kind == domain.KindHTTP && spec.HTTP != nil {
		parts = append(parts, spec.HTTP.Method)
	}
	if retries > 0 {
		parts = append(parts, fmt.Sprintf("retry %d× after %s", retries, spec.Confirm.Delay))
	} else {
		parts = append(parts, "no retry")
	}
	parts = append(parts, fmt.Sprintf("down after %d", spec.FailureThreshold))
	switch {
	case kind == domain.KindHTTP && spec.HTTP != nil:
		if bm := bodyMatch(spec.HTTP); bm != "" {
			parts = append(parts, bm)
		}
		if !spec.HTTP.Redirects() {
			parts = append(parts, "no redirects")
		}
		if !spec.HTTP.Verify() {
			parts = append(parts, "TLS unverified")
		}
	case kind == domain.KindTLS && spec.TLS != nil:
		parts = append(parts, fmt.Sprintf("warn %d d · crit %d d", spec.TLS.WarnDays, spec.TLS.CritDays))
	case kind == domain.KindICMP && spec.ICMP != nil:
		parts = append(parts, fmt.Sprintf("loss over %.0f%%", spec.ICMP.LossThreshold*100))
	case kind == domain.KindTCP && spec.TCP != nil && spec.TCP.Expect != "":
		parts = append(parts, fmt.Sprintf("banner %q", spec.TCP.Expect))
	}
	h.Advanced = strings.Join(parts, " · ")
	return h
}

// bodyMatch words an http body expectation: "$.status = ok".
func bodyMatch(h *domain.HTTPCheck) string {
	eb := h.ExpectBody
	if eb.IsZero() {
		return ""
	}
	switch {
	case eb.JSONPath != nil:
		return eb.JSONPath.Path + " = " + fmt.Sprint(eb.JSONPath.Equals)
	case eb.Contains != "":
		return fmt.Sprintf("contains %q", eb.Contains)
	case eb.NotContains != "":
		return fmt.Sprintf("without %q", eb.NotContains)
	}
	return ""
}

func plural2(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// shortReason is the word a list row has room for: "refused", "timeout", "503".
func shortReason(reason string) string {
	r := strings.ToLower(reason)
	switch {
	case reason == "":
		return "fail"
	case strings.Contains(r, "connection refused"):
		return "refused"
	case strings.HasPrefix(r, "timeout"):
		return "timeout"
	case strings.Contains(r, "no such host"):
		return "no such host"
	case strings.Contains(r, "refusing private target"):
		return "private target"
	case strings.HasPrefix(r, "x509") || strings.Contains(r, "certificate"):
		if strings.Contains(r, "expire") {
			return "cert expiry"
		}
		return "tls"
	}
	if len(reason) >= 3 && reason[0] >= '1' && reason[0] <= '5' && reason[1] >= '0' && reason[1] <= '9' && reason[2] >= '0' && reason[2] <= '9' {
		return reason[:3]
	}
	if len(reason) > 24 {
		return reason[:24] + "…"
	}
	return reason
}
