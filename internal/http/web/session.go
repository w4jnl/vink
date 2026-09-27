package web

import (
	"errors"
	"net/http"
	"strings"

	"github.com/w4jnl/vink/internal/domain"
)

// home sends the person to their last project, their only project, or
// the project list.
func (h *Web) home(c *reqCtx) error {
	list, err := h.svc.ProjectsForUser(c.r.Context(), c.principal.User.ID, c.principal.InstanceAdmin)
	if err != nil {
		return err
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

type projectsData struct {
	base
	Projects []projectRow
}

func (h *Web) projects(c *reqCtx) error {
	list, err := h.svc.ProjectsForUser(c.r.Context(), c.principal.User.ID, c.principal.InstanceAdmin)
	if err != nil {
		return err
	}
	data := projectsData{base: h.baseFor(c, "Projects")}
	for _, p := range list {
		data.Projects = append(data.Projects, projectRow{Name: p.Name, Slug: p.Slug, OrgSlug: p.OrgSlug, Path: "/o/" + p.OrgSlug + "/p/" + p.Slug, Role: p.Role})
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
		return h.errorPage(c, http.StatusNotFound, "No local sign-in", "This instance signs people in through the reverse proxy.")
	}
	if c.principal != nil {
		http.Redirect(c.w, c.r, safeNext(c.r.URL.Query().Get("next")), http.StatusSeeOther)
		return nil
	}
	return h.render(c, http.StatusOK, "login", "layout", loginData{base: h.baseFor(c, "Sign in"), Next: safeNext(c.r.URL.Query().Get("next"))})
}

func (h *Web) login(c *reqCtx) error {
	if !h.authn.LocalEnabled() {
		return domain.ErrForbidden
	}
	if err := c.r.ParseForm(); err != nil {
		return err
	}
	user := strings.TrimSpace(c.r.PostFormValue("user"))
	next := safeNext(c.r.PostFormValue("next"))
	_, err := h.authn.Login(c.w, c.r, user, c.r.PostFormValue("password"))
	if err != nil {
		data := loginData{base: h.baseFor(c, "Sign in"), User: user, Next: next}
		status := http.StatusUnauthorized
		switch {
		case errors.Is(err, domain.ErrRateLimited):
			data.Error = "Too many attempts. Wait a minute and try again."
			status = http.StatusTooManyRequests
		case errors.Is(err, domain.ErrUnauthorized):
			data.Error = "Wrong user or password."
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
