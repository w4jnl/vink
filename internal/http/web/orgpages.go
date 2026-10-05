package web

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/http/web/ui"
)

// The status pages tab of the org settings: /o/{org}/admin/pages, the add
// panel on ?add=1, the edit panel on ?edit={slug}. The panel is the
// project tab's with Projects and Group by added.

type orgPagesData struct {
	adminData
	Panel *panelData
	Pages []pageRow
}

func (h *Web) orgPagePanel(c *reqCtx, p *domain.StatusPage, projects []*domain.Project) *panelData {
	root := c.orgPath() + "/pages"
	panel := &panelData{
		Title: "Add page", Action: root, KindPath: root + "?add=1", CancelPath: root, CSRF: c.csrf(), SubmitLabel: "Save page",
		Values: map[string]string{"title": "", "slug": "", "match_tags": "", "group_by": domain.GroupByProject, "incidents": domain.IncidentsOpen,
			"access": "public", "password": "", "custom_domain": "", "password_placeholder": "only with Password", "prefix": h.pagePrefix()},
		Errors: map[string]string{}, Repeat: "public", IncidentOptions: incidentOptions, Org: true,
	}
	chosen := map[string]bool{}
	if p != nil {
		panel.Title, panel.EditID, panel.Action, panel.DeletePath, panel.KindPath = "Edit page", p.Slug, root+"/"+p.Slug, root+"/"+p.Slug+"/delete", root+"?edit="+url.QueryEscape(p.Slug)
		panel.Values["title"], panel.Values["slug"], panel.Values["match_tags"], panel.Values["custom_domain"] = p.Title, p.Slug, strings.Join(p.MatchTags, ", "), p.CustomDomain
		panel.Values["group_by"], panel.Values["incidents"] = p.GroupBy, p.Incidents
		if p.HasPassword() {
			panel.Values["access"], panel.Repeat = "password", "password"
			panel.Values["password_placeholder"] = "unchanged"
		}
		for _, id := range p.Projects {
			chosen[id] = true
		}
	}
	for _, pr := range projects {
		panel.ProjectChecks = append(panel.ProjectChecks, ui.CheckboxProps{Name: "projects", Value: pr.ID, Label: pr.Name, Checked: chosen[pr.ID], Attrs: ui.Attr("form", "page-form")})
	}
	return panel
}

func (h *Web) orgPagesTab(c *reqCtx, status int, panel *panelData, projects []*domain.Project) error {
	ad, err := h.adminData(c, "pages")
	if err != nil {
		return err
	}
	pages, err := h.svc.ListOrgStatusPages(c.r.Context(), c.scope)
	if err != nil {
		return err
	}
	names := make(map[string]string, len(projects))
	for _, pr := range projects {
		names[pr.ID] = pr.Name
	}
	d := orgPagesData{adminData: ad, Panel: panel}
	for _, p := range pages {
		d.Pages = append(d.Pages, h.statusPageRow(c, p, ad.TabPath, names))
	}
	return h.render(c, status, "admin", "layout", d)
}

// orgPagesList serves the tab, with the panel the query asks for.
func (h *Web) orgPagesList(c *reqCtx) error {
	ctx := c.r.Context()
	projects, err := h.svc.ListProjects(ctx, c.scope)
	if err != nil {
		return err
	}
	q := c.r.URL.Query()
	var panel *panelData
	switch {
	case q.Has("add"):
		panel = h.orgPagePanel(c, nil, projects)
		if q.Has("access") || q.Has("title") { // the Access switch re-renders the panel with what was typed
			h.parseStatusPage(q, panel)
			panel.Errors = map[string]string{}
		}
	case q.Get("edit") != "":
		p, err := h.svc.OrgStatusPage(ctx, c.scope, q.Get("edit"))
		if err != nil {
			return err
		}
		panel = h.orgPagePanel(c, p, projects)
	}
	return h.orgPagesTab(c, http.StatusOK, panel, projects)
}

func (h *Web) saveOrgPage(c *reqCtx) error {
	if err := c.r.ParseForm(); err != nil {
		return err
	}
	ctx := c.r.Context()
	projects, err := h.svc.ListProjects(ctx, c.scope)
	if err != nil {
		return err
	}
	slug := c.r.PathValue("slug")
	var cur *domain.StatusPage
	if slug != "" {
		if cur, err = h.svc.OrgStatusPage(ctx, c.scope, slug); err != nil {
			return err
		}
	}
	p := h.orgPagePanel(c, cur, projects)
	page, password := h.parseStatusPage(c.r.PostForm, p)
	if cur != nil && cur.HasPassword() {
		p.Values["password_placeholder"] = "unchanged"
	}
	if cur == nil {
		_, err = h.svc.CreateOrgStatusPage(ctx, c.scope, page, password)
	} else {
		_, err = h.svc.UpdateOrgStatusPage(ctx, c.scope, slug, page, password)
	}
	if err != nil {
		if applyPanelValidation(p, err) {
			return h.orgPagesTab(c, http.StatusUnprocessableEntity, p, projects)
		}
		if errors.Is(err, domain.ErrConflict) {
			p.Errors["slug"] = "This address is taken."
			return h.orgPagesTab(c, http.StatusUnprocessableEntity, p, projects)
		}
		return err
	}
	return h.redirect(c, c.orgPath()+"/pages")
}

func (h *Web) deleteOrgPage(c *reqCtx) error {
	if err := h.svc.DeleteOrgStatusPage(c.r.Context(), c.scope, c.r.PathValue("slug")); err != nil {
		return err
	}
	return h.redirect(c, c.orgPath()+"/pages")
}
