package web

import (
	"net/http"

	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/http/web/ui"
)

// Org settings: /o/{org}/admin/{members|projects|pages|agents}, for org admins
// and owners. A member of another org gets a 404, a member of this org
// without the role a 403.

var orgTabs = []ui.Tab{{ID: "members", Label: "Members"}, {ID: "projects", Label: "Projects"}, {ID: "pages", Label: "Status pages"}, {ID: "agents", Label: "Agents"}, {ID: "audit", Label: "Audit log"}}

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

// orgMember resolves the org and binds the member's scope; every member
// may pass, the handler decides what they see.
func (h *Web) orgMember(fn handlerFn) http.Handler {
	return h.user(func(c *reqCtx) error {
		org, err := h.svc.OrgBySlug(c.r.Context(), c.r.PathValue("org"))
		if err != nil {
			return domain.NotFound("org")
		}
		sc, err := h.authn.OrgScope(c.principal, org)
		if err != nil {
			return err
		}
		c.org, c.scope = org, sc
		return fn(c)
	})
}

func (c *reqCtx) orgPath() string { return c.href("/o/" + c.org.Slug + "/admin") }

type adminData struct {
	base
	Org        string
	Tab        string
	TabPath    string
	Tabs       []ui.Tab
	Lede       string
	ComingSoon string
	// Drawer is the agent drawer when one is open; the page then splits.
	Drawer *agentDrawer
	// Audit is the audit log tab.
	Audit *auditView
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
	// members and viewers get the audit tab alone; the rest is for admins
	if !c.scope.CanAdminOrg() {
		d.Tabs = []ui.Tab{{ID: "audit", Label: "Audit log", Href: c.orgPath() + "/audit"}}
		d.Lede = auditLede(c.org.Slug, false)
		return d, nil
	}
	agents, err := h.svc.ListAgents(c.r.Context(), c.scope)
	if err != nil {
		return d, err
	}
	members, err := h.svc.ListMembers(c.r.Context(), c.scope)
	if err != nil {
		return d, err
	}
	pages, err := h.svc.ListOrgStatusPages(c.r.Context(), c.scope)
	if err != nil {
		return d, err
	}
	for _, t := range orgTabs {
		tab := ui.Tab{ID: t.ID, Label: t.Label, Href: c.orgPath() + "/" + t.ID}
		switch t.ID {
		case "members":
			tab.Count = ui.Count(len(members))
		case "projects":
			tab.Count = ui.Count(n)
		case "pages":
			tab.Count = ui.Count(len(pages))
		case "agents":
			tab.Count = ui.Count(len(agents))
		}
		d.Tabs = append(d.Tabs, tab)
	}
	switch tab {
	case "members":
		d.Lede = "Roles apply to every project in " + c.org.Slug + ". Admins manage members and projects; only owners transfer ownership or delete the org."
	case "projects":
		d.Lede = "A project holds monitors, channels, routes and keys. Roles in " + c.org.Slug + " apply to every project."
	case "pages":
		d.Lede = "Public pages that show monitors from " + c.org.Slug + "'s projects, one group per project or per tag. No sign-in, no scripts, cached for 30 s."
	case "agents":
		d.Lede = "Agents run pull checks from networks vink cannot reach. An agent dials out to vink over WebSocket, keeps nothing on disk and never listens on a port."
	case "audit":
		d.Lede = auditLede(c.org.Slug, true)
	}
	return d, nil
}

func auditLede(org string, all bool) string {
	if !all {
		return "Everything that happened in your projects in " + org + ": changes made by people and API keys, and every state flip."
	}
	return "Everything that happened in " + org + ": changes made by people and API keys, sign-ins and access changes, and every state flip."
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
	switch tab {
	case "agents":
		return h.agentsList(c)
	case "projects":
		return h.projectsList(c)
	case "members":
		return h.membersList(c)
	case "pages":
		return h.orgPagesList(c)
	}
	d, err := h.adminData(c, tab)
	if err != nil {
		return err
	}
	return h.render(c, http.StatusOK, "admin", "layout", d)
}
