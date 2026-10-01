package web

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/w4jnl/vink/internal/auth"
	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/service"
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
	Local bool
	OIDC  *oidcLogin
	// OIDCError is the provider's error code, shown in mono.
	OIDCError string
}

type oidcLogin struct {
	Name, Host, StartPath string
}

func (h *Web) loginData(c *reqCtx, next string) loginData {
	data := loginData{base: h.baseFor(c, "Sign in", ""), Next: next, Local: h.authn.LocalEnabled()}
	data.Fill = true
	if h.authn.OIDCEnabled() {
		o := &oidcLogin{Name: h.authn.OIDCDisplayName(), StartPath: auth.OIDCStartURL(next)}
		if u, err := url.Parse(h.authn.OIDCIssuer()); err == nil {
			o.Host = u.Host
		}
		data.OIDC = o
	}
	return data
}

func (h *Web) loginForm(c *reqCtx) error {
	if !h.authn.LocalEnabled() && !h.authn.OIDCEnabled() {
		return domain.NotFound("sign-in")
	}
	next := safeNext(c.r.URL.Query().Get("next"))
	if c.principal != nil {
		http.Redirect(c.w, c.r, next, http.StatusSeeOther)
		return nil
	}
	q := c.r.URL.Query()
	if h.authn.OIDCAutoRedirect() && q.Get("oidc_error") == "" && q.Get("code") == "" {
		http.Redirect(c.w, c.r, auth.OIDCStartURL(next), http.StatusSeeOther)
		return nil
	}
	data := h.loginData(c, next)
	switch q.Get("code") {
	case "locked":
		data.Error = "Too many wrong codes. Sign in again."
	case "expired":
		data.Error = "The sign-in timed out. Start again."
	}
	data.OIDCError = q.Get("oidc_error")
	return h.render(c, http.StatusOK, "login", "layout", data)
}

// oidcStart sends the browser to the provider.
func (h *Web) oidcStart(c *reqCtx) error {
	if !h.authn.OIDCEnabled() {
		return domain.NotFound("sign-in")
	}
	if c.principal != nil {
		http.Redirect(c.w, c.r, safeNext(c.r.URL.Query().Get("next")), http.StatusSeeOther)
		return nil
	}
	to, err := h.authn.OIDCStart(c.w, c.r, safeNext(c.r.URL.Query().Get("next")))
	if err != nil {
		h.log.Error("oidc start", "err", err)
		http.Redirect(c.w, c.r, "/login?oidc_error=provider_unreachable", http.StatusSeeOther)
		return nil
	}
	http.Redirect(c.w, c.r, to, http.StatusSeeOther)
	return nil
}

// oidcCallback finishes the sign-in; every failure lands on /login as one
// notice with the code.
func (h *Web) oidcCallback(c *reqCtx) error {
	if !h.authn.OIDCEnabled() {
		return domain.NotFound("sign-in")
	}
	_, next, err := h.authn.OIDCCallback(c.w, c.r)
	if err != nil {
		code := "failed"
		var oe auth.OIDCError
		switch {
		case errors.As(err, &oe):
			code = oe.Code
		case errors.Is(err, domain.ErrNotFound):
			code = "expired"
		default:
			h.log.Error("oidc callback", "err", err)
		}
		http.Redirect(c.w, c.r, "/login?oidc_error="+url.QueryEscape(code), http.StatusSeeOther)
		return nil
	}
	if h.authn.TOTPRequired() {
		// never for provider accounts; their second factor is the provider's
		next = safeNext(next)
	}
	http.Redirect(c.w, c.r, safeNext(next), http.StatusSeeOther)
	return nil
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
	_, err := h.authn.LoginNext(c.w, c.r, user, c.r.PostFormValue("password"), next)
	if errors.Is(err, auth.ErrNeedsCode) {
		http.Redirect(c.w, c.r, "/login/code", http.StatusSeeOther)
		return nil
	}
	if err != nil {
		data := h.loginData(c, next)
		data.User = user
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

// The code step: after a correct password for an account with two-factor.

type codeData struct {
	base
	Subject  string
	Recovery bool
	Error    string
}

func (h *Web) codeForm(c *reqCtx) error {
	ch, err := h.authn.Challenge(c.r)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			http.Redirect(c.w, c.r, "/login?code=expired", http.StatusSeeOther)
			return nil
		}
		return err
	}
	data := codeData{base: h.baseFor(c, "Two-factor sign-in", ""), Subject: ch.Subject, Recovery: c.r.URL.Query().Get("recovery") == "1"}
	data.Fill = true
	return h.render(c, http.StatusOK, "login_code", "layout", data)
}

func (h *Web) code(c *reqCtx) error {
	if err := c.r.ParseForm(); err != nil {
		return err
	}
	recovery := c.r.PostFormValue("recovery") != ""
	code := c.r.PostFormValue("otp")
	if recovery {
		code = c.r.PostFormValue("recovery")
	}
	_, next, err := h.authn.CompleteChallenge(c.w, c.r, code)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrCodeLocked):
			http.Redirect(c.w, c.r, "/login?code=locked", http.StatusSeeOther)
			return nil
		case errors.Is(err, domain.ErrNotFound):
			http.Redirect(c.w, c.r, "/login?code=expired", http.StatusSeeOther)
			return nil
		case errors.Is(err, domain.ErrUnauthorized):
			ch, cerr := h.authn.Challenge(c.r)
			if cerr != nil {
				http.Redirect(c.w, c.r, "/login?code=expired", http.StatusSeeOther)
				return nil
			}
			data := codeData{base: h.baseFor(c, "Two-factor sign-in", ""), Subject: ch.Subject, Recovery: recovery}
			data.Fill = true
			data.Error = "That code didn’t work. Use the code on screen now; it changes every 30 seconds."
			if recovery {
				data.Error = "That recovery code didn’t work. Each one works once."
			}
			return h.render(c, http.StatusUnauthorized, "login_code", "layout", data)
		}
		return err
	}
	http.Redirect(c.w, c.r, safeNext(next), http.StatusSeeOther)
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
	if c.principal.User.Source == "oidc" && h.authn.OIDCLogoutURL() != "" {
		http.Redirect(c.w, c.r, h.authn.OIDCLogoutURL(), http.StatusSeeOther)
		return nil
	}
	if h.authn.LocalEnabled() || h.authn.OIDCEnabled() {
		http.Redirect(c.w, c.r, "/login", http.StatusSeeOther)
		return nil
	}
	http.Redirect(c.w, c.r, "/", http.StatusSeeOther)
	return nil
}
