package domain

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// MonitorYAML renders the monitor in the apply file's form: the flat
// representation from the design document, defaults left out, flow
// style for tags and schedule. It is the "As YAML" view and the export.
func MonitorYAML(m *Monitor) string {
	var b strings.Builder
	line := func(k, v string) { b.WriteString(k + ": " + v + "\n") }
	line("slug", yamlScalar(m.Slug))
	if m.Name != "" && m.Name != m.Slug {
		line("name", yamlScalar(m.Name))
	}
	line("kind", string(m.Kind))
	if len(m.Tags) > 0 {
		line("tags", yamlFlowList(m.Tags))
	}
	if s := m.Heartbeat; s != nil {
		switch {
		case s.Schedule.Cron != "":
			line("schedule", "{cron: "+yamlScalar(s.Schedule.Cron)+"}")
		case s.Schedule.Period != 0:
			line("schedule", "{period: "+s.Schedule.Period.String()+"}")
		}
		if s.Timezone != "" {
			line("timezone", yamlScalar(s.Timezone))
		}
		if s.Tolerance != 0 && s.Tolerance != DefaultTolerance {
			line("tolerance", s.Tolerance.String())
		}
		if s.Grace != 0 && s.Grace != DefaultGrace {
			line("grace", s.Grace.String())
		}
		if s.MaxRuntime != 0 {
			line("max_runtime", s.MaxRuntime.String())
		}
		if s.FailureThreshold > 1 {
			line("failure_threshold", strconv.Itoa(s.FailureThreshold))
		}
		if s.RecoveryThreshold > 1 {
			line("recovery_threshold", strconv.Itoa(s.RecoveryThreshold))
		}
		if len(s.Methods) > 0 {
			line("methods", yamlFlowList(s.Methods))
		}
		if s.BodyLimit > 0 {
			line("body_limit", strconv.FormatInt(s.BodyLimit, 10))
		}
	}
	if s := m.Pull; s != nil {
		pullYAML(&b, m.Kind, s)
	}
	return strings.TrimRight(b.String(), "\n")
}

// pullYAML writes the cadence fields that differ from the defaults and
// the kind's block as an indented map.
func pullYAML(b *strings.Builder, kind Kind, s *PullSpec) {
	line := func(k, v string) { b.WriteString(k + ": " + v + "\n") }
	sub := func(k, v string) { b.WriteString("  " + k + ": " + v + "\n") }
	if s.Interval != 0 && s.Interval != DefaultInterval {
		line("interval", s.Interval.String())
	}
	if s.Timeout != 0 && s.Timeout != DefaultCheckTimeout {
		line("timeout", s.Timeout.String())
	}
	if s.FailureThreshold != 0 && s.FailureThreshold != DefaultPullThreshold {
		line("failure_threshold", strconv.Itoa(s.FailureThreshold))
	}
	if !s.Confirm.IsZero() && (s.Confirm.Retries != DefaultConfirmRetries || s.Confirm.Delay != DefaultConfirmDelay) {
		line("confirm", "{retries: "+strconv.Itoa(s.Confirm.Retries)+", delay: "+s.Confirm.Delay.String()+"}")
	}
	if s.RecoveryThreshold > 1 {
		line("recovery_threshold", strconv.Itoa(s.RecoveryThreshold))
	}
	if s.Location != "" {
		line("location", yamlScalar(s.Location))
	}
	switch {
	case kind == KindHTTP && s.HTTP != nil:
		h := s.HTTP
		line("http", "")
		sub("url", yamlScalar(h.URL))
		if h.Method != "" && h.Method != "GET" {
			sub("method", h.Method)
		}
		if len(h.Headers) > 0 {
			keys := make([]string, 0, len(h.Headers))
			for k := range h.Headers {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			parts := make([]string, 0, len(keys))
			for _, k := range keys {
				parts = append(parts, yamlScalar(k)+": "+yamlScalar(h.Headers[k]))
			}
			sub("headers", "{"+strings.Join(parts, ", ")+"}")
		}
		if h.Body != "" {
			sub("body", yamlScalar(h.Body))
		}
		if len(h.ExpectStatus) > 1 || (len(h.ExpectStatus) == 1 && h.ExpectStatus[0] != (StatusRange{200, 299})) {
			parts := make([]string, 0, len(h.ExpectStatus))
			for _, r := range h.ExpectStatus {
				parts = append(parts, r.String())
			}
			sub("expect_status", "["+strings.Join(parts, ", ")+"]")
		}
		if eb := h.ExpectBody; !eb.IsZero() {
			var parts []string
			if eb.Contains != "" {
				parts = append(parts, "contains: "+yamlScalar(eb.Contains))
			}
			if eb.NotContains != "" {
				parts = append(parts, "not_contains: "+yamlScalar(eb.NotContains))
			}
			if jp := eb.JSONPath; jp != nil {
				parts = append(parts, "jsonpath: {path: "+yamlScalar(jp.Path)+", equals: "+yamlAny(jp.Equals)+"}")
			}
			sub("expect_body", "{"+strings.Join(parts, ", ")+"}")
		}
		if !h.Redirects() {
			sub("follow_redirects", "false")
		}
		if !h.Verify() {
			sub("verify_tls", "false")
		}
		if h.CAPem != "" {
			sub("ca_pem", yamlScalar(h.CAPem))
		}
	case kind == KindTCP && s.TCP != nil:
		t := s.TCP
		line("tcp", "")
		sub("host", yamlScalar(t.Host))
		sub("port", strconv.Itoa(t.Port))
		if t.Send != "" {
			sub("send", yamlScalar(t.Send))
		}
		if t.Expect != "" {
			sub("expect", yamlScalar(t.Expect))
		}
	case kind == KindDNS && s.DNS != nil:
		d := s.DNS
		line("dns", "")
		sub("name", yamlScalar(d.Name))
		if d.Type != "" && d.Type != "A" {
			sub("type", d.Type)
		}
		if d.Resolver != "" {
			sub("resolver", yamlScalar(d.Resolver))
		}
		if len(d.Expect) > 0 {
			sub("expect", yamlFlowList(d.Expect))
		}
	case kind == KindTLS && s.TLS != nil:
		t := s.TLS
		line("tls", "")
		sub("host", yamlScalar(t.Host))
		if t.Port != 0 && t.Port != 443 {
			sub("port", strconv.Itoa(t.Port))
		}
		if t.ServerName != "" {
			sub("servername", yamlScalar(t.ServerName))
		}
		if t.WarnDays != 0 && t.WarnDays != DefaultTLSWarnDays {
			sub("warn_days", strconv.Itoa(t.WarnDays))
		}
		if t.CritDays != 0 && t.CritDays != DefaultTLSCritDays {
			sub("crit_days", strconv.Itoa(t.CritDays))
		}
	case kind == KindICMP && s.ICMP != nil:
		i := s.ICMP
		line("icmp", "")
		sub("host", yamlScalar(i.Host))
		if i.Count != 0 && i.Count != DefaultICMPCount {
			sub("count", strconv.Itoa(i.Count))
		}
		if i.LossThreshold != 0 && i.LossThreshold != DefaultICMPLoss {
			sub("loss_threshold", strconv.FormatFloat(i.LossThreshold, 'g', -1, 64))
		}
	}
}

// yamlAny renders a scalar of any JSON type.
func yamlAny(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		return yamlScalar(x)
	case bool:
		return strconv.FormatBool(x)
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	case int:
		return strconv.Itoa(x)
	}
	return yamlScalar(fmt.Sprint(v))
}

func yamlFlowList(items []string) string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, yamlScalar(it))
	}
	return "[" + strings.Join(out, ", ") + "]"
}

// yamlScalar quotes a string when YAML would read it as something else,
// in block or flow context.
func yamlScalar(s string) string {
	if s == "" {
		return `""`
	}
	plain := !strings.ContainsAny(s[:1], "-?:,[]{}#&*!|>'\"%@` \t") && !strings.ContainsAny(s[len(s)-1:], ": \t") &&
		!strings.ContainsAny(s, "[]{},*\n\t\"'") && !strings.Contains(s, ": ") && !strings.Contains(s, " #")
	lower := strings.ToLower(s)
	switch lower {
	case "true", "false", "yes", "no", "on", "off", "null", "~":
		plain = false
	}
	if _, err := strconv.ParseFloat(s, 64); err == nil {
		plain = false
	}
	if plain {
		return s
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\t", `\t`).Replace(s) + `"`
}
