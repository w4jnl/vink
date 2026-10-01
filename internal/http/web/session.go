package web

import (
	"errors"
	"net/http"
	"strings"

	"github.com/w4jnl/vink/internal/domain"
)

// home sends the person to their last project, their only project, or
// the project list; without any membership, the no-access page.
func (h *Web) home(c *reqCtx) error {
	list, err := h.svc.ProjectsForUser(c.r.Context(), c.principal.User.ID, c.principal.InstanceAdmin)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		if len(h.orgsFor(c)) == 0 {
			return h.noAccess(c)
		}
		// an org, but no project in it yet: the chooser says what to do
		http.Redirect(c.w, c.r, "/projects", http.StatusSeeOther)
		return nil
	}
	if c.principal.Session != nil && c.principal.Session.LastProjectID != "" {
		for _, p := range list {
			if p.ID == c.principal.Session.LastProjectID {
				http.Redirect(c.w, c.r, "/o/"+p.OrgSlug+"/p/"+p.Slug, http.StatusSeeOther)
				return nil
			}
		}
	}
	if len(list) == 1 {
		http.Redirect(c.w, c.r, "/o/"+list[0].OrgSlug+"/p/"+list[0].Slug, http.StatusSeeOther)
		return nil
	}
	http.Redirect(c.w, c.r, "/projects", http.StatusSeeOther)
	return nil
}

type projectRow struct {
	Name, Slug, OrgSlug, Path string
	Role                      domain.Role
}

// emptyOrg is an org the viewer is in that has no project yet; AddPath is
// set when the viewer may add one.
type emptyOrg struct {
	Slug, AddPath string
}

type projectsData struct {
	base
	Projects []projectRow
	Empty    []emptyOrg
}

func (h *Web) projects(c *reqCtx) error {
	list, err := h.svc.ProjectsForUser(c.r.Context(), c.principal.User.ID, c.principal.InstanceAdmin)
	if err != nil {
		return err
	}
	orgs := h.orgsFor(c)
	if len(list) == 0 && len(orgs) == 0 {
		return h.noAccess(c)
	}
	data := projectsData{base: h.baseFor(c, "Projects", "")}
	seen := map[string]bool{}
	for _, p := range list {
		seen[p.OrgSlug] = true
		data.Projects = append(data.Projects, projectRow{Name: p.Name, Slug: p.Slug, OrgSlug: p.OrgSlug, Path: "/o/" + p.OrgSlug + "/p/" + p.Slug, Role: p.Role})
	}
	for _, o := range orgs {
		if seen[o.Slug] {
			continue
		}
		e := emptyOrg{Slug: o.Slug}
		if c.principal.InstanceAdmin || o.Role.AtLeast(domain.RoleAdmin) {
			e.AddPath = "/o/" + o.Slug + "/admin/projects?add=1"
		}
		data.Empty = append(data.Empty, e)
	}
	return h.render(c, http.StatusOK, "projects", "layout", data)
}

type loginData struct {
	base
	User  string
	Next  string
	Error string
}

func (h *Web) loginForm(c *reqCtx) error {
	if !h.authn.LocalEnabled() {
		return domain.NotFound("local sign-in")
	}
	if c.principal != nil {
		http.Redirect(c.w, c.r, safeNext(c.r.URL.Query().Get("next")), http.StatusSeeOther)
		return nil
	}
	data := loginData{base: h.baseFor(c, "Sign in", ""), Next: safeNext(c.r.URL.Query().Get("next"))}
	data.Fill = true
	return h.render(c, http.StatusOK, "login", "layout", data)
}

func (h *Web) login(c *reqCtx) error {
	if !h.authn.LocalEnabled() {
		return domain.ErrForbidden
	}
	if err := c.r.ParseForm(); err != nil {
		return err
	}
	user := strings.TrimSpace(c.r.PostFormValue("username"))
	next := safeNext(c.r.PostFormValue("next"))
	_, err := h.authn.Login(c.w, c.r, user, c.r.PostFormValue("password"))
	if err != nil {
		data := loginData{base: h.baseFor(c, "Sign in", ""), User: user, Next: next}
		data.Fill = true
		status := http.StatusUnauthorized
		switch {
		case errors.Is(err, domain.ErrRateLimited):
			data.Error = "Too many attempts. Wait a minute and try again."
			status = http.StatusTooManyRequests
		case errors.Is(err, domain.ErrUnauthorized):
			data.Error = "Wrong username or password."
		default:
			return err
		}
		return h.render(c, status, "login", "layout", data)
	}
	http.Redirect(c.w, c.r, next, http.StatusSeeOther)
	return nil
}

func (h *Web) logout(c *reqCtx) error {
	if c.principal.Session != nil {
		if err := h.authn.Logout(c.w, c.r); err != nil {
			return err
		}
	}
	if c.principal.Source == "proxy" && h.authn.LogoutURL() != "" {
		http.Redirect(c.w, c.r, h.authn.LogoutURL(), http.StatusSeeOther)
		return nil
	}
	if h.authn.LocalEnabled() {
		http.Redirect(c.w, c.r, "/login", http.StatusSeeOther)
		return nil
	}
	http.Redirect(c.w, c.r, "/", http.StatusSeeOther)
	return nil
}
