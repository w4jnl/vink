package web

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/http/web/ui"
)

// The channels and routes tabs of the org settings: /o/{org}/admin/channels
// and /o/{org}/admin/routes, the add panel on ?add=1, the edit panel on
// ?edit={id}. They share the project tabs' panels; an org route also picks
// the projects it covers, none meaning every project.

type orgAlertsData struct {
	adminData
	Panel    *panelData
	Channels []channelRow
	Routes   []routeRow
}

// orgAlertsTab renders the channels or the routes tab; htmx gets the tab
// alone.
func (h *Web) orgAlertsTab(c *reqCtx, status int, tab string, o settingsOpts) error {
	ad, err := h.adminData(c, tab)
	if err != nil {
		return err
	}
	ctx := c.r.Context()
	routes, err := h.svc.ListOrgRoutes(ctx, c.scope)
	if err != nil {
		return err
	}
	d := orgAlertsData{adminData: ad, Panel: o.panel}
	switch tab {
	case "channels":
		channels, err := h.svc.ListOrgChannels(ctx, c.scope)
		if err != nil {
			return err
		}
		d.Channels = channelRows(c, ad.TabPath, channels, routes, o.notes, func(id string) (*time.Time, error) {
			return h.svc.LastSentForOrgChannel(ctx, c.scope, id)
		})
	case "routes":
		projects, err := h.svc.ListProjects(ctx, c.scope)
		if err != nil {
			return err
		}
		names := make(map[string]string, len(projects))
		for _, pr := range projects {
			names[pr.ID] = pr.Name
		}
		d.Routes = routeRows(ad.TabPath, routes, names)
	}
	name := "layout"
	if c.htmx() {
		name = "admin-" + tab
	}
	return h.render(c, status, "admin", name, d)
}

// --- channels -------------------------------------------------------------

func (h *Web) orgChannelsList(c *reqCtx) error {
	root := c.orgPath() + "/channels"
	q := c.r.URL.Query()
	var o settingsOpts
	switch {
	case q.Has("add"):
		o.panel = h.channelPanelAt(c, root, nil, q.Get("kind"))
		for _, k := range channelFields {
			if q.Has(k) {
				o.panel.Values[k] = strings.TrimSpace(q.Get(k))
			}
		}
	case q.Get("edit") != "":
		ch, err := h.svc.OrgChannel(c.r.Context(), c.scope, q.Get("edit"))
		if err != nil {
			return err
		}
		o.panel = h.channelPanelAt(c, root, ch, "")
	}
	return h.orgAlertsTab(c, http.StatusOK, "channels", o)
}

// saveOrgChannel creates (no id) or updates a channel of the org;
// action=test sends a test through the unsaved form instead.
func (h *Web) saveOrgChannel(c *reqCtx) error {
	if err := c.r.ParseForm(); err != nil {
		return err
	}
	ctx := c.r.Context()
	id := c.r.PathValue("id")
	var cur *domain.Channel
	if id != "" {
		var err error
		if cur, err = h.svc.OrgChannel(ctx, c.scope, id); err != nil {
			return err
		}
	}
	p := h.channelPanelAt(c, c.orgPath()+"/channels", cur, strings.TrimSpace(c.r.PostFormValue("kind")))
	ch := readChannelForm(c, p, cur)
	opts := settingsOpts{panel: p}
	if c.r.PostFormValue("action") == "test" {
		err := h.svc.TestOrgChannelConfig(ctx, c.scope, ch.Kind, testConfig(ch, cur))
		if applyPanelValidation(p, err) {
			return h.orgAlertsTab(c, http.StatusUnprocessableEntity, "channels", opts)
		}
		if errors.Is(err, domain.ErrForbidden) {
			return err
		}
		p.Note = testNote(err)
		return h.orgAlertsTab(c, http.StatusOK, "channels", opts)
	}
	if len(p.Errors) > 0 {
		return h.orgAlertsTab(c, http.StatusUnprocessableEntity, "channels", opts)
	}
	var err error
	if cur == nil {
		_, err = h.svc.CreateOrgChannel(ctx, c.scope, ch)
	} else {
		_, err = h.svc.UpdateOrgChannel(ctx, c.scope, id, ch)
	}
	if err != nil {
		if applyPanelValidation(p, err) {
			return h.orgAlertsTab(c, http.StatusUnprocessableEntity, "channels", opts)
		}
		if errors.Is(err, domain.ErrConflict) {
			p.Errors["name"] = "A channel with this name exists."
			return h.orgAlertsTab(c, http.StatusUnprocessableEntity, "channels", opts)
		}
		return err
	}
	return h.redirect(c, c.orgPath()+"/channels")
}

func (h *Web) deleteOrgChannel(c *reqCtx) error {
	if err := h.svc.DeleteOrgChannel(c.r.Context(), c.scope, c.r.PathValue("id")); err != nil {
		return err
	}
	return h.redirect(c, c.orgPath()+"/channels")
}

// testOrgChannel sends a test through a saved channel of the org and shows
// the result under its row.
func (h *Web) testOrgChannel(c *reqCtx) error {
	id := c.r.PathValue("id")
	err := h.svc.TestOrgChannel(c.r.Context(), c.scope, id)
	if errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrForbidden) {
		return err
	}
	return h.orgAlertsTab(c, http.StatusOK, "channels", settingsOpts{notes: map[string]*noteData{id: testNote(err)}})
}

func (h *Web) toggleOrgChannel(c *reqCtx) error {
	ctx := c.r.Context()
	ch, err := h.svc.OrgChannel(ctx, c.scope, c.r.PathValue("id"))
	if err != nil {
		return err
	}
	if _, err := h.svc.SetOrgChannelEnabled(ctx, c.scope, ch.ID, !ch.Enabled); err != nil {
		return err
	}
	if c.htmx() {
		return h.orgAlertsTab(c, http.StatusOK, "channels", settingsOpts{})
	}
	return h.redirect(c, c.orgPath()+"/channels")
}

// --- routes ---------------------------------------------------------------

// orgRoutePanel is the route panel with the org's channels and a box for
// each of its projects.
func (h *Web) orgRoutePanel(c *reqCtx, rt *domain.Route, projects []*domain.Project) (*panelData, error) {
	channels, err := h.svc.ListOrgChannels(c.r.Context(), c.scope)
	if err != nil {
		return nil, err
	}
	p := routePanelAt(c, c.orgPath()+"/routes", channels, rt)
	p.Org = true
	chosen := map[string]bool{}
	if rt != nil {
		for _, id := range rt.Projects {
			chosen[id] = true
		}
	}
	for _, pr := range projects {
		p.ProjectChecks = append(p.ProjectChecks, ui.CheckboxProps{Label: pr.Name, Name: "projects", Value: pr.ID, Checked: chosen[pr.ID], Attrs: ui.Attr("form", "route-form")})
	}
	return p, nil
}

func (h *Web) orgRoutesList(c *reqCtx) error {
	ctx := c.r.Context()
	q := c.r.URL.Query()
	var o settingsOpts
	if q.Has("add") || q.Get("edit") != "" {
		projects, err := h.svc.ListProjects(ctx, c.scope)
		if err != nil {
			return err
		}
		var rt *domain.Route
		if id := q.Get("edit"); id != "" && !q.Has("add") {
			if rt, err = h.svc.OrgRoute(ctx, c.scope, id); err != nil {
				return err
			}
		}
		if o.panel, err = h.orgRoutePanel(c, rt, projects); err != nil {
			return err
		}
	}
	return h.orgAlertsTab(c, http.StatusOK, "routes", o)
}

func (h *Web) saveOrgRoute(c *reqCtx) error {
	if err := c.r.ParseForm(); err != nil {
		return err
	}
	ctx := c.r.Context()
	id := c.r.PathValue("id")
	var cur *domain.Route
	if id != "" {
		var err error
		if cur, err = h.svc.OrgRoute(ctx, c.scope, id); err != nil {
			return err
		}
	}
	projects, err := h.svc.ListProjects(ctx, c.scope)
	if err != nil {
		return err
	}
	p, err := h.orgRoutePanel(c, cur, projects)
	if err != nil {
		return err
	}
	rt := readRouteForm(c, p, cur)
	rt.Projects = c.r.PostForm["projects"]
	for i := range p.ProjectChecks {
		p.ProjectChecks[i].Checked = contains(rt.Projects, p.ProjectChecks[i].Value)
	}
	opts := settingsOpts{panel: p}
	if len(p.Errors) > 0 {
		return h.orgAlertsTab(c, http.StatusUnprocessableEntity, "routes", opts)
	}
	if cur == nil {
		_, err = h.svc.CreateOrgRoute(ctx, c.scope, rt)
	} else {
		_, err = h.svc.UpdateOrgRoute(ctx, c.scope, id, rt)
	}
	if err != nil {
		if applyPanelValidation(p, err) {
			return h.orgAlertsTab(c, http.StatusUnprocessableEntity, "routes", opts)
		}
		return err
	}
	return h.redirect(c, c.orgPath()+"/routes")
}

func (h *Web) deleteOrgRoute(c *reqCtx) error {
	if err := h.svc.DeleteOrgRoute(c.r.Context(), c.scope, c.r.PathValue("id")); err != nil {
		return err
	}
	return h.redirect(c, c.orgPath()+"/routes")
}
