package web

import (
	"net/http"
	"time"

	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/http/web/view"
)

type incidentRow struct {
	State, MonitorName, MonitorSlug, MonitorPath string
	Text, OpenedAbs, AckedBy, AckedAbs, AckPath  string
	Open, CanAck                                 bool
}

type incidentsData struct {
	base
	DownCount int
	OpenCount int
	Rows      []incidentRow
	ListPath  string
}

func (h *Web) incidentsData(c *reqCtx) (incidentsData, error) {
	ctx := c.r.Context()
	list, err := h.svc.ListIncidents(ctx, c.scope, false, 200)
	if err != nil {
		return incidentsData{}, err
	}
	d := incidentsData{base: h.baseFor(c, "Incidents"), ListPath: c.projectPath() + "/incidents?partial=list"}
	if counts, err := h.svc.MonitorCounts(ctx, c.scope); err == nil {
		d.DownCount = counts[domain.StateDown]
	}
	loc, err := time.LoadLocation(c.project.Timezone)
	if err != nil {
		loc = time.UTC
	}
	for _, inc := range list {
		row := incidentRow{
			MonitorName: inc.MonitorName, MonitorSlug: inc.MonitorSlug, MonitorPath: c.projectPath() + "/m/" + inc.MonitorSlug,
			OpenedAbs: view.Abs(inc.OpenedAt, loc), AckedBy: inc.AckedBy, Open: inc.Open(), AckPath: c.projectPath() + "/incidents/" + inc.ID + "/ack",
		}
		if inc.Open() {
			d.OpenCount++
			row.State = "down"
			row.Text = "down " + view.For(inc.OpenedAt, c.now)
			row.CanAck = inc.AckedAt == nil && c.scope.CanOperate()
		} else {
			row.State = "up"
			row.Text = "down " + view.Span(inc.ResolvedAt.Sub(inc.OpenedAt)) + ", resolved " + view.Ago(*inc.ResolvedAt, c.now)
		}
		if inc.AckedAt != nil {
			row.AckedAbs = view.Abs(*inc.AckedAt, loc)
		}
		d.Rows = append(d.Rows, row)
	}
	return d, nil
}

func (h *Web) incidents(c *reqCtx) error {
	d, err := h.incidentsData(c)
	if err != nil {
		return err
	}
	if c.r.URL.Query().Get("partial") == "list" {
		return h.render(c, http.StatusOK, "incidents", "incidents-list", d)
	}
	if c.htmx() {
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
