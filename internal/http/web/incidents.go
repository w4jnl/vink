package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/http/web/ui"
	"github.com/w4jnl/vink/internal/http/web/view"
	"github.com/w4jnl/vink/internal/timefmt"
)

type incidentsData struct {
	base
	MainPath      string
	IncidentsPath string
	DownCount     int
	OpenCount     int
	ResolvedCount int
	Chips         []ui.ChipProps
	Period        string
	PeriodLabel   string
	Open          []ui.IncidentRowProps
	Resolved      []ui.IncidentRowProps
}

var periods = map[string]struct {
	days  int
	label string
}{"7d": {7, "7 days"}, "30d": {30, "30 days"}, "90d": {90, "90 days"}}

func (h *Web) incidentsData(c *reqCtx) (incidentsData, error) {
	ctx := c.r.Context()
	q := c.r.URL.Query()
	period := q.Get("period")
	if _, ok := periods[period]; !ok {
		period = "30d"
	}
	tag := q.Get("tag")
	since := c.now.Add(-time.Duration(periods[period].days) * 24 * time.Hour)
	list, err := h.svc.ListIncidents(ctx, c.scope, false, 500, since)
	if err != nil {
		return incidentsData{}, err
	}
	d := incidentsData{base: h.baseFor(c, "Incidents", "incidents"), Period: period, PeriodLabel: periods[period].label, IncidentsPath: c.projectPath() + "/incidents"}
	d.FilterTag = tag
	params := url.Values{"partial": {"main"}, "period": {period}}
	if tag != "" {
		params.Set("tag", tag)
	}
	d.MainPath = d.IncidentsPath + "?" + params.Encode()
	if counts, err := h.svc.MonitorCounts(ctx, c.scope); err == nil {
		d.DownCount = counts[domain.StateDown]
	}
	loc, err := time.LoadLocation(c.project.Timezone)
	if err != nil {
		loc = time.UTC
	}
	tagCounts := map[string]int{}
	for _, inc := range list {
		for _, t := range inc.MonitorTags {
			tagCounts[t]++
		}
	}
	for _, t := range sortedTags(tagCounts) {
		pressed := tag == t
		value := t
		if pressed {
			value = ""
		}
		d.Chips = append(d.Chips, ui.ChipProps{Label: t, Count: ui.Count(tagCounts[t]), Pressed: pressed, Type: "submit", Attrs: ui.Attr("name", "tag") + ui.Attr("value", value)})
	}
	for _, inc := range list {
		if tag != "" && !contains(inc.MonitorTags, tag) {
			continue
		}
		row := ui.IncidentRowProps{
			Name: inc.MonitorName, Slug: inc.MonitorSlug, Href: c.projectPath() + "/m/" + inc.MonitorSlug,
			Reason: reasonText(inc, loc), OpenedAbs: view.Abs(inc.OpenedAt, loc),
		}
		if inc.Open() {
			d.OpenCount++
			row.Opened = timefmt.Clock(inc.OpenedAt, loc)[:5] + " · " + view.Ago(inc.OpenedAt, c.now)
			row.Duration = "open " + spanLong(c.now.Sub(inc.OpenedAt))
			if inc.AckedAt != nil {
				row.State = "acked"
				row.AckedBy = actorName(inc.AckedBy)
				row.AckedAt = timefmt.Clock(*inc.AckedAt, loc)[:5]
			} else {
				row.State = "open"
				if c.scope.CanOperate() {
					ack := c.projectPath() + "/incidents/" + inc.ID + "/ack"
					row.AckHTML = ui.HTML(`<form method="post" action="` + ack + `" hx-post="` + ack + `" hx-target="#incidents"><input type="hidden" name="_csrf" value="` + c.csrf() + `">` + string(ui.Button(ui.ButtonProps{Label: "Ack", Type: "submit"})) + `</form>`)
				}
			}
			d.Open = append(d.Open, row)
		} else {
			d.ResolvedCount++
			row.State = "resolved"
			row.Opened = dayClock(inc.OpenedAt, c.now, loc)
			row.Duration = "lasted " + spanLong(inc.ResolvedAt.Sub(inc.OpenedAt))
			row.Resolved = timefmt.Clock(*inc.ResolvedAt, loc)[:5]
			d.Resolved = append(d.Resolved, row)
		}
	}
	return d, nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// reasonText turns an event reason into the incident's one-line cause.
func reasonText(inc *domain.Incident, loc *time.Location) string {
	switch inc.Reason {
	case "grace over", "deadline passed":
		return "no ping by " + timefmt.Clock(inc.OpenedAt, loc)[:5]
	case "":
		return "down"
	}
	return inc.Reason
}

func actorName(actor string) string {
	if i := strings.IndexByte(actor, ':'); i >= 0 {
		return actor[i+1:]
	}
	return actor
}

// spanLong renders "4 min", "1 h 12 min", "2 d 3 h".
func spanLong(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	switch {
	case d < time.Minute:
		return "under a minute"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + " min"
	case d < 24*time.Hour:
		h, m := int(d.Hours()), int(d.Minutes())%60
		if m == 0 {
			return strconv.Itoa(h) + " h"
		}
		return strconv.Itoa(h) + " h " + strconv.Itoa(m) + " min"
	default:
		days, hrs := int(d.Hours())/24, int(d.Hours())%24
		if hrs == 0 {
			return strconv.Itoa(days) + " d"
		}
		return strconv.Itoa(days) + " d " + strconv.Itoa(hrs) + " h"
	}
}

// dayClock renders "today 03:30" or "Sat 26 Sep 22:14".
func dayClock(t, now time.Time, loc *time.Location) string {
	lt, ln := t.In(loc), now.In(loc)
	if lt.Year() == ln.Year() && lt.YearDay() == ln.YearDay() {
		return "today " + lt.Format("15:04")
	}
	return lt.Format("Mon 2 Jan 15:04")
}

func (h *Web) incidents(c *reqCtx) error {
	d, err := h.incidentsData(c)
	if err != nil {
		return err
	}
	if c.r.URL.Query().Get("partial") == "main" || c.htmx() {
		return h.render(c, http.StatusOK, "incidents", "incidents-main", d)
	}
	return h.render(c, http.StatusOK, "incidents", "layout", d)
}

func (h *Web) ackIncident(c *reqCtx) error {
	if _, err := h.svc.AckIncident(c.r.Context(), c.scope, c.r.PathValue("id")); err != nil {
		return err
	}
	if c.htmx() {
		d, err := h.incidentsData(c)
		if err != nil {
			return err
		}
		return h.render(c, http.StatusOK, "incidents", "incidents-main", d)
	}
	http.Redirect(c.w, c.r, c.projectPath()+"/incidents", http.StatusSeeOther)
	return nil
}
