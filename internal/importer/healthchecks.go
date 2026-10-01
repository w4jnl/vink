package importer

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/w4jnl/vink/internal/apply"
	"github.com/w4jnl/vink/internal/domain"
)

// hcCheck is one check as the Healthchecks API lists it (v1 to v3): a
// simple check has timeout (the period) and grace in seconds, a cron
// check has schedule and tz, an oncalendar check a systemd expression.
type hcCheck struct {
	Name          string  `json:"name"`
	Slug          string  `json:"slug"`
	Tags          string  `json:"tags"`
	Desc          string  `json:"desc"`
	Kind          string  `json:"kind"`
	Grace         float64 `json:"grace"`
	Timeout       float64 `json:"timeout"`
	Schedule      string  `json:"schedule"`
	TZ            string  `json:"tz"`
	Methods       string  `json:"methods"`
	Status        string  `json:"status"`
	UniqueKey     string  `json:"unique_key"`
	FilterSubject bool    `json:"filter_subject"`
	FilterBody    bool    `json:"filter_body"`
	StartKw       string  `json:"start_kw"`
	SuccessKw     string  `json:"success_kw"`
	FailureKw     string  `json:"failure_kw"`
}

// Healthchecks converts the API listing (GET /api/v3/checks/, also v1 and
// v2, or a bare array of checks) into heartbeat monitors.
func Healthchecks(data []byte) (*Result, error) {
	var wrapped struct {
		Checks []hcCheck `json:"checks"`
	}
	var checks []hcCheck
	if err := json.Unmarshal(data, &wrapped); err == nil && wrapped.Checks != nil {
		checks = wrapped.Checks
	} else if err := json.Unmarshal(data, &checks); err != nil {
		return nil, errors.New("not a Healthchecks listing: want {\"checks\": [...]} from GET /api/v3/checks/")
	}
	if len(checks) == 0 {
		return nil, errors.New("the listing holds no checks")
	}
	r := &Result{File: &apply.File{Version: 1}}
	taken := slugs{}
	for _, c := range checks {
		slug := c.Slug
		if slug == "" || !domain.ValidSlug(slug) || taken[slug] {
			slug = taken.take(c.Name, c.UniqueKey)
		} else {
			taken[slug] = true
		}
		what := describe(c.Name, slug)
		m := apply.Monitor{Slug: slug, Name: c.Name, Kind: domain.KindHeartbeat, Tags: domain.NormalizeTags(strings.Fields(c.Tags))}
		if c.Grace > 0 {
			m.Grace = seconds(c.Grace, 0)
		}
		kind := c.Kind
		if kind == "" {
			if c.Schedule != "" {
				kind = "cron"
			} else {
				kind = "simple"
			}
		}
		switch kind {
		case "simple":
			if c.Timeout <= 0 {
				r.skip(what, "no period")
				continue
			}
			m.Schedule = &domain.Schedule{Period: seconds(c.Timeout, 0)}
		case "cron":
			m.Schedule = &domain.Schedule{Cron: c.Schedule}
			m.Timezone = c.TZ
		case "oncalendar":
			r.skip(what, fmt.Sprintf("OnCalendar schedules are not supported; rewrite %q as cron", c.Schedule))
			continue
		default:
			r.skip(what, "unknown check kind "+kind)
			continue
		}
		if strings.EqualFold(strings.TrimSpace(c.Methods), "POST") {
			m.Methods = []string{"POST"}
		}
		if c.FilterSubject || c.FilterBody || c.StartKw != "" || c.SuccessKw != "" || c.FailureKw != "" {
			r.note(what, "email keyword filters do not carry over; jobs ping the URL directly")
		}
		if c.Status == "paused" {
			r.note(what, "paused in Healthchecks; vink imports it running, pause it after the apply")
		}
		r.File.Monitors = append(r.File.Monitors, m)
	}
	if len(r.File.Monitors) == 0 {
		return r, errors.New("nothing to import; every check was skipped")
	}
	return r, nil
}
