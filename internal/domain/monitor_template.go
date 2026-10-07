package domain

import (
	"net/url"
	"strings"
)

// CreateParams are the query parameters a ping that creates its monitor
// (?create=1) may carry. Any other parameter is left alone.
var CreateParams = []string{"name", "period", "cron", "tz", "grace", "tolerance", "max_runtime", "tags"}

// MonitorTemplate is what a ping that creates its monitor asks for. It
// holds the raw values and is read only when a monitor is made, so a
// mistake in it never stands in the way of pinging a monitor that
// exists: the settings apply on create only.
type MonitorTemplate struct {
	Values url.Values
}

// TemplateFromQuery keeps the create parameters of a ping's query.
func TemplateFromQuery(q url.Values) *MonitorTemplate {
	t := &MonitorTemplate{Values: url.Values{}}
	for _, k := range CreateParams {
		if v, ok := q[k]; ok {
			t.Values[k] = v
		}
	}
	return t
}

// Given reports whether the ping set anything beyond the defaults.
func (t *MonitorTemplate) Given() bool { return t != nil && len(t.Values) > 0 }

// Monitor builds the heartbeat for slug: what the template sets, the
// period and grace defaults for the rest. A value that does not parse is
// a ValidationError naming its parameter; the monitor's own rules (cron,
// grace and tolerance limits, the timezone) are checked when it is
// created.
func (t *MonitorTemplate) Monitor(slug string, period, grace Duration) (*Monitor, error) {
	m := &Monitor{Slug: slug, Name: slug, Kind: KindHeartbeat, Heartbeat: &HeartbeatSpec{Schedule: Schedule{Period: period}, Grace: grace}}
	if t == nil {
		return m, nil
	}
	get := func(k string) string { return strings.TrimSpace(t.Values.Get(k)) }
	ve := &ValidationError{}
	dur := func(k string, into *Duration) {
		s := get(k)
		if s == "" {
			return
		}
		d, err := ParseDuration(s)
		if err != nil || d <= 0 {
			ve.Add(k, "must be a duration like 90s, 30m, 2h or 1d")
			return
		}
		*into = d
	}
	if name := get("name"); name != "" {
		m.Name = name
	}
	hb := m.Heartbeat
	every, cron := get("period"), get("cron")
	switch {
	case every != "" && cron != "":
		ve.Add("cron", "give period or cron, not both")
	case cron != "":
		hb.Schedule = Schedule{Cron: cron}
	default:
		dur("period", &hb.Schedule.Period)
	}
	hb.Timezone = get("tz")
	dur("grace", &hb.Grace)
	dur("tolerance", &hb.Tolerance)
	dur("max_runtime", &hb.MaxRuntime)
	if tags := get("tags"); tags != "" {
		m.Tags = NormalizeTags(strings.Split(tags, ","))
	}
	if err := ve.OrNil(); err != nil {
		return nil, err
	}
	return m, nil
}
