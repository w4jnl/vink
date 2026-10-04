package web

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/http/web/ui"
)

// The projects tab of the org settings: /o/{org}/admin/projects, the add
// panel on ?add=1, the edit panel on ?edit={slug}.

type orgProjectRow struct {
	Slug, Sub string
	Href      string
	Cells     []ui.Cell
	Actions   ui.HTML
}

type projectPanel struct {
	Title, Action, CancelPath, CSRF, EditSlug string
	SlugHint                                  string
	Timezones                                 []ui.Option
	Values, Errors                            map[string]string
}

type orgProjectsData struct {
	adminData
	Quota ui.HTML
	Panel *projectPanel
	Flash string
	Rows  []orgProjectRow
}

func (h *Web) projectsTab(c *reqCtx, status int, panel *projectPanel) error {
	ad, err := h.adminData(c, "projects")
	if err != nil {
		return err
	}
	d := orgProjectsData{adminData: ad, Panel: panel, Flash: c.r.URL.Query().Get("flash")}
	ctx := c.r.Context()
	projects, err := h.svc.ListProjects(ctx, c.scope)
	if err != nil {
		return err
	}
	problems, err := h.svc.ProjectProblems(ctx)
	if err != nil {
		return err
	}
	total := 0
	for _, p := range projects {
		counts := problems[c.org.Slug+"/"+p.Slug]
		n := counts.Down + counts.Late + counts.Up + counts.Paused + counts.New
		total += n
		path := c.href("/o/" + c.org.Slug + "/p/" + p.Slug)
		d.Rows = append(d.Rows, orgProjectRow{
			Slug: p.Slug, Sub: path + " · " + p.Timezone, Href: path,
			Cells: []ui.Cell{
				{HTML: ui.StateCounts(ui.StateCountsProps{Down: counts.Down, Late: counts.Late, Up: counts.Up, Paused: counts.Paused, New: counts.New}), Size: "l"},
				{Text: strconv.Itoa(n) + " " + plural2(n, "monitor", "monitors")},
				{Text: "since " + p.CreatedAt.In(h.orgLocation(c)).Format("2 Jan"), Mono: true},
			},
			Actions: ui.Button(ui.ButtonProps{Label: "Open", Href: path}) + ui.Button(ui.ButtonProps{Label: "Edit", Href: d.TabPath + "?edit=" + url.QueryEscape(p.Slug)}),
		})
	}
	var quota ui.HTML
	if c.org.QuotaMonitors != nil {
		quota += ui.Usage(total, int(*c.org.QuotaMonitors), "monitors")
	}
	if c.org.QuotaAgents != nil {
		agents, err := h.svc.ListAgents(ctx, c.scope)
		if err != nil {
			return err
		}
		quota += ui.Usage(len(agents), int(*c.org.QuotaAgents), "agents")
	}
	d.Quota = quota
	return h.render(c, status, "admin", "layout", d)
}

// orgTimezoneOptions lists the common zones with the current one first.
func orgTimezoneOptions(current string) []ui.Option {
	out := []ui.Option{}
	seen := map[string]bool{}
	if current != "" {
		out = append(out, ui.Option{Value: current, Label: current})
		seen[current] = true
	}
	for _, z := range commonZones {
		if !seen[z] {
			out = append(out, ui.Option{Value: z, Label: z})
		}
	}
	return out
}

func (h *Web) newProjectPanel(c *reqCtx) *projectPanel {
	tz := "UTC"
	if projects, err := h.svc.ListProjects(c.r.Context(), c.scope); err == nil && len(projects) > 0 {
		tz = projects[0].Timezone
	}
	p := &projectPanel{Title: "Add project", Action: c.orgPath() + "/projects", CancelPath: c.orgPath() + "/projects", CSRF: c.csrf(), Values: map[string]string{"pr_tz": tz}, Errors: map[string]string{}}
	p.slugHint(c)
	return p
}

func (p *projectPanel) slugHint(c *reqCtx) {
	slug := p.Values["pr_slug"]
	if slug == "" {
		slug = "<slug>"
	}
	p.SlugHint = "In its URLs: /o/" + c.org.Slug + "/p/" + slug
	p.Timezones = orgTimezoneOptions(p.Values["pr_tz"])
}

// projectsList is the tab; ?add=1 opens the add panel, ?edit= the edit one.
func (h *Web) projectsList(c *reqCtx) error {
	var panel *projectPanel
	q := c.r.URL.Query()
	switch {
	case q.Get("add") == "1":
		panel = h.newProjectPanel(c)
	case q.Get("edit") != "":
		p, err := h.svc.ProjectBySlug(c.r.Context(), c.org.ID, q.Get("edit"))
		if err != nil {
			return err
		}
		panel = &projectPanel{Title: "Edit " + p.Slug, Action: c.orgPath() + "/projects/" + p.Slug, CancelPath: c.orgPath() + "/projects", CSRF: c.csrf(), EditSlug: p.Slug, Values: map[string]string{"pr_name": p.Name, "pr_slug": p.Slug, "pr_tz": p.Timezone}, Errors: map[string]string{}}
		panel.SlugHint = "Part of its URLs; it cannot change."
		panel.Timezones = orgTimezoneOptions(p.Timezone)
	}
	return h.projectsTab(c, http.StatusOK, panel)
}

// createProject makes the project with its own ping key; the slug derives
// from the name when left empty.
func (h *Web) createProject(c *reqCtx) error {
	p := h.newProjectPanel(c)
	for _, k := range []string{"pr_name", "pr_slug", "pr_tz"} {
		p.Values[k] = strings.TrimSpace(c.r.PostFormValue(k))
	}
	if p.Values["pr_slug"] == "" && p.Values["pr_name"] != "" {
		p.Values["pr_slug"] = domain.Slugify(p.Values["pr_name"])
	}
	p.slugHint(c)
	project, err := h.svc.CreateProject(c.r.Context(), c.scope, c.org.ID, p.Values["pr_slug"], p.Values["pr_name"], p.Values["pr_tz"])
	if err != nil {
		if applyProjectErrors(p, err) {
			return h.projectsTab(c, http.StatusUnprocessableEntity, p)
		}
		return err
	}
	return h.redirect(c, c.orgPath()+"/projects?flash="+url.QueryEscape("Project "+project.Slug+" created with its own ping key and a default route. Add channels in its settings."))
}

// updateProject renames or re-zones a project; the org admin role counts
// as the project's admin.
func (h *Web) updateProject(c *reqCtx) error {
	project, err := h.svc.ProjectBySlug(c.r.Context(), c.org.ID, c.r.PathValue("slug"))
	if err != nil {
		return err
	}
	sc := c.scope
	sc.ProjectID = project.ID
	p := &projectPanel{Title: "Edit " + project.Slug, Action: c.orgPath() + "/projects/" + project.Slug, CancelPath: c.orgPath() + "/projects", CSRF: c.csrf(), EditSlug: project.Slug,
		Values: map[string]string{"pr_name": strings.TrimSpace(c.r.PostFormValue("pr_name")), "pr_slug": project.Slug, "pr_tz": strings.TrimSpace(c.r.PostFormValue("pr_tz"))}, Errors: map[string]string{}}
	p.SlugHint = "Part of its URLs; it cannot change."
	p.Timezones = orgTimezoneOptions(p.Values["pr_tz"])
	if _, err := h.svc.UpdateProject(c.r.Context(), sc, p.Values["pr_name"], p.Values["pr_tz"]); err != nil {
		if applyProjectErrors(p, err) {
			return h.projectsTab(c, http.StatusUnprocessableEntity, p)
		}
		return err
	}
	return h.redirect(c, c.orgPath()+"/projects?flash="+url.QueryEscape("Project "+project.Slug+" saved."))
}

// applyProjectErrors maps service errors onto the panel's fields.
func applyProjectErrors(p *projectPanel, err error) bool {
	if ve, ok := domain.AsValidation(err); ok {
		for _, fe := range ve.Errors {
			field := map[string]string{"slug": "pr_slug", "name": "pr_name", "timezone": "pr_tz"}[fe.Field]
			if field == "" {
				field = "pr_name"
			}
			p.Errors[field] = capitalise(fe.Msg) + "."
		}
		return true
	}
	if errors.Is(err, domain.ErrConflict) {
		p.Errors["pr_slug"] = "A project with this slug exists in this org."
		return true
	}
	return false
}
