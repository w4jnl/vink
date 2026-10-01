package web

import (
	"net/http"

	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/http/web/ui"
)

// Org settings: /o/{org}/admin/{members|projects|agents}, for org admins
// and owners. A member of another org gets a 404, a member of this org
// without the role a 403.

var orgTabs = []ui.Tab{{ID: "members", Label: "Members"}, {ID: "projects", Label: "Projects"}, {ID: "agents", Label: "Agents"}}

// orgAdmin resolves the org from the path and binds an org scope.
func (h *Web) orgAdmin(fn handlerFn) http.Handler {
	return h.user(func(c *reqCtx) error {
		org, err := h.svc.OrgBySlug(c.r.Context(), c.r.PathValue("org"))
		if err != nil {
			return domain.NotFound("org")
		}
		sc, err := h.authn.OrgScope(c.principal, org)
		if err != nil {
			return err
		}
		if !sc.CanAdminOrg() {
			return domain.ErrForbidden
		}
		c.org, c.scope = org, sc
		return fn(c)
	})
}

func (c *reqCtx) orgPath() string { return "/o/" + c.org.Slug + "/admin" }

type adminData struct {
	base
	Org        string
	Tab        string
	TabPath    string
	Tabs       []ui.Tab
	Lede       string
	ComingSoon string
}

func (h *Web) orgAdminHome(c *reqCtx) error {
	http.Redirect(c.w, c.r, c.orgPath()+"/projects", http.StatusSeeOther)
	return nil
}

// adminData builds the shell: title, tabs with counts, the top bar on
// "none" with the last project of this org.
func (h *Web) adminData(c *reqCtx, tab string) (adminData, error) {
	d := adminData{base: h.baseFor(c, c.org.Name+" settings", "none"), Org: c.org.Slug, Tab: tab, TabPath: c.orgPath() + "/" + tab}
	projects, err := h.svc.ProjectsForUser(c.r.Context(), c.principal.User.ID, c.principal.InstanceAdmin)
	if err != nil {
		return d, err
	}
	n := 0
	for _, p := range projects {
		if p.OrgSlug == c.org.Slug {
			n++
		}
	}
	for _, t := range orgTabs {
		tab := ui.Tab{ID: t.ID, Label: t.Label, Href: c.orgPath() + "/" + t.ID}
		if t.ID == "projects" {
			tab.Count = ui.Count(n)
		}
		d.Tabs = append(d.Tabs, tab)
	}
	switch tab {
	case "members":
		d.Lede = "Roles apply to every project in " + c.org.Slug + ". Admins manage members and projects; only owners transfer ownership or delete the org."
		d.ComingSoon = "Members and invites arrive in phase 3."
	case "projects":
		d.Lede = "A project holds monitors, channels, routes and keys. Roles in " + c.org.Slug + " apply to every project."
		d.ComingSoon = "The projects tab arrives later in phase 2."
	case "agents":
		d.Lede = "Agents run pull checks from networks vink cannot reach. An agent dials out to vink over WebSocket, keeps nothing on disk and never listens on a port."
		d.ComingSoon = "Agents arrive later in phase 2."
	}
	return d, nil
}

func (h *Web) orgAdminTab(c *reqCtx) error {
	tab := c.r.PathValue("tab")
	known := false
	for _, t := range orgTabs {
		known = known || t.ID == tab
	}
	if !known {
		return domain.NotFound("settings tab")
	}
	d, err := h.adminData(c, tab)
	if err != nil {
		return err
	}
	return h.render(c, http.StatusOK, "admin", "layout", d)
}
