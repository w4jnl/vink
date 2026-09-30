package domain

import (
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
	return strings.TrimRight(b.String(), "\n")
}

func yamlFlowList(items []string) string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, yamlScalar(it))
	}
	return "[" + strings.Join(out, ", ") + "]"
}

// yamlScalar quotes a string when YAML would read it as something else.
func yamlScalar(s string) string {
	if s == "" {
		return `""`
	}
	plain := true
	for i, r := range s {
		switch r {
		case ':', '#', '{', '}', '[', ']', ',', '&', '*', '!', '|', '>', '\'', '"', '%', '@', '`', '\n', '\t':
			plain = false
		case ' ':
			if i == 0 || i == len(s)-1 {
				plain = false
			}
		case '-', '?':
			if i == 0 {
				plain = false
			}
		}
	}
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
