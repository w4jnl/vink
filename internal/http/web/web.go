package web

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/w4jnl/vink/internal/auth"
	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/http/middleware"
	"github.com/w4jnl/vink/internal/http/web/ui"
	"github.com/w4jnl/vink/internal/service"
	"github.com/w4jnl/vink/internal/version"
)

// Web serves the UI.
type Web struct {
	svc    *service.Service
	authn  *auth.Authenticator
	log    *slog.Logger
	static *Static
	tmpl   *Templates
	now    func() time.Time
	// smtpFrom is shown as the placeholder of a mail channel's From.
	smtpFrom string
}

// New builds the UI handlers.
func New(svc *service.Service, authn *auth.Authenticator, log *slog.Logger) (*Web, error) {
	static, err := NewStatic()
	if err != nil {
		return nil, err
	}
	tmpl, err := NewTemplates(static)
	if err != nil {
		return nil, err
	}
	return &Web{svc: svc, authn: authn, log: log, static: static, tmpl: tmpl, now: func() time.Time { return time.Now().UTC() }}, nil
}

// SetClock replaces the clock, for tests.
func (h *Web) SetClock(now func() time.Time) { h.now = now }

// SetSMTPFrom sets the instance mail sender shown in the channel form.
func (h *Web) SetSMTPFrom(from string) { h.smtpFrom = from }

// Mount registers every UI route.
func (h *Web) Mount(mux *http.ServeMux) {
	mux.Handle("GET /static/", h.static.Handler())
	mux.Handle("GET /{$}", h.user(h.home))
	mux.Handle("GET /projects", h.user(h.projects))
	mux.Handle("GET /login", h.public(h.loginForm))
	mux.Handle("POST /login", h.public(h.login))
	mux.Handle("POST /logout", h.user(h.logout))

	p := "/o/{org}/p/{project}"
	mux.Handle("GET "+p, h.project(h.monitors))
	mux.Handle("GET "+p+"/m/new", h.project(h.newMonitor))
	mux.Handle("POST "+p+"/m/new", h.project(h.createMonitor))
	mux.Handle("POST "+p+"/m/preview", h.project(h.previewMonitor))
	mux.Handle("GET "+p+"/m/{slug}", h.project(h.monitor))
	mux.Handle("GET "+p+"/m/{slug}/edit", h.project(h.editMonitor))
	mux.Handle("POST "+p+"/m/{slug}/edit", h.project(h.updateMonitor))
	mux.Handle("POST "+p+"/m/{slug}/preview", h.project(h.previewMonitor))
	mux.Handle("POST "+p+"/m/{slug}/pause", h.project(h.pauseMonitor))
	mux.Handle("POST "+p+"/m/{slug}/resume", h.project(h.resumeMonitor))
	mux.Handle("POST "+p+"/m/{slug}/check", h.project(h.checkMonitor))
	mux.Handle("POST "+p+"/m/{slug}/delete", h.project(h.deleteMonitor))
	mux.Handle("GET "+p+"/incidents", h.project(h.incidents))
	mux.Handle("POST "+p+"/incidents/{id}/ack", h.project(h.ackIncident))
	mux.Handle("GET "+p+"/settings", h.project(h.settingsRedirect))
	mux.Handle("GET "+p+"/settings/{tab}", h.project(h.settings))
	mux.Handle("POST "+p+"/settings/channels", h.project(h.saveChannel))
	mux.Handle("POST "+p+"/settings/channels/{id}", h.project(h.saveChannel))
	mux.Handle("POST "+p+"/settings/channels/{id}/delete", h.project(h.deleteChannel))
	mux.Handle("POST "+p+"/settings/channels/{id}/test", h.project(h.testChannel))
	mux.Handle("POST "+p+"/settings/channels/{id}/toggle", h.project(h.toggleChannel))
	mux.Handle("POST "+p+"/settings/routes", h.project(h.saveRoute))
	mux.Handle("POST "+p+"/settings/routes/{id}", h.project(h.saveRoute))
	mux.Handle("POST "+p+"/settings/routes/{id}/delete", h.project(h.deleteRoute))
	mux.Handle("POST "+p+"/settings/maintenance", h.project(h.saveMaintenance))
	mux.Handle("POST "+p+"/settings/maintenance/{id}", h.project(h.saveMaintenance))
	mux.Handle("POST "+p+"/settings/maintenance/{id}/delete", h.project(h.deleteMaintenance))
	mux.Handle("POST "+p+"/settings/maintenance/{id}/end", h.project(h.endMaintenance))
	mux.Handle("POST "+p+"/settings/keys", h.project(h.createKey))
	mux.Handle("POST "+p+"/settings/keys/{id}/revoke", h.project(h.revokeKey))
	mux.Handle("POST "+p+"/settings/ping-key/rotate", h.project(h.rotatePingKey))
}

// reqCtx is what every handler gets.
type reqCtx struct {
	w         http.ResponseWriter
	r         *http.Request
	principal *auth.Principal
	scope     domain.Scope
	org       *domain.Org
	project   *domain.Project
	now       time.Time
}

func (c *reqCtx) htmx() bool { return c.r.Header.Get("HX-Request") == "true" }

func (c *reqCtx) projectPath() string {
	return "/o/" + c.org.Slug + "/p/" + c.project.Slug
}

func (c *reqCtx) csrf() string {
	if c.principal == nil {
		return ""
	}
	return c.principal.CSRF()
}

type handlerFn func(c *reqCtx) error

func securityHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; frame-ancestors 'none'; form-action 'self'; base-uri 'self'")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "same-origin")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Cache-Control", "no-store")
}

// public runs fn without requiring a principal.
func (h *Web) public(fn handlerFn) http.Handler {
	return h.authn.Identity(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		securityHeaders(w)
		c := &reqCtx{w: w, r: r, principal: auth.PrincipalFrom(r.Context()), now: h.now()}
		if err := fn(c); err != nil {
			h.fail(c, err)
		}
	}))
}

// user requires a signed-in principal.
func (h *Web) user(fn handlerFn) http.Handler {
	return h.public(func(c *reqCtx) error {
		if c.principal == nil {
			return h.anonymous(c)
		}
		if c.r.Method != http.MethodGet && c.r.Method != http.MethodHead && !h.authn.CheckCSRF(c.r, c.principal) {
			return errCSRF
		}
		return fn(c)
	})
}

// project additionally resolves the org and project from the path and
// binds the scope; a project the principal cannot see is a 404.
func (h *Web) project(fn handlerFn) http.Handler {
	return h.user(func(c *reqCtx) error {
		ctx := c.r.Context()
		org, err := h.svc.OrgBySlug(ctx, c.r.PathValue("org"))
		if err != nil {
			return domain.NotFound("project")
		}
		project, err := h.svc.ProjectBySlug(ctx, org.ID, c.r.PathValue("project"))
		if err != nil {
			return domain.NotFound("project")
		}
		sc, err := h.authn.UserScope(c.principal, project)
		if err != nil {
			return err
		}
		project.OrgSlug = org.Slug
		c.org, c.project, c.scope = org, project, sc
		c.r = c.r.WithContext(auth.WithScope(ctx, sc))
		if c.principal.Session != nil && c.principal.Session.LastProjectID != project.ID {
			_ = h.svc.SetSessionProject(ctx, c.principal.Session.ID, project.ID)
		}
		return fn(c)
	})
}

var errCSRF = errors.New("the form token is missing or stale; reload the page and try again")

// anonymous sends people to the login form in local mode and shows the
// proxy-denied page in proxy-only mode.
func (h *Web) anonymous(c *reqCtx) error {
	if h.authn.LocalEnabled() {
		if c.r.Method != http.MethodGet {
			return domain.ErrUnauthorized
		}
		next := c.r.URL.RequestURI()
		http.Redirect(c.w, c.r, "/login?next="+url.QueryEscape(next), http.StatusSeeOther)
		return nil
	}
	return h.proxyDenied(c)
}

// fail maps an error to a page.
func (h *Web) fail(c *reqCtx, err error) {
	switch {
	case errors.Is(err, errCSRF):
		_ = h.authPage(c, http.StatusForbidden, authPage{Heading: "The form expired", Lead: err.Error(), Actions: []ui.ButtonProps{{Label: "Reload", Variant: "primary", Href: c.r.URL.RequestURI()}}})
	case errors.Is(err, domain.ErrNotFound):
		_ = h.authPage(c, http.StatusNotFound, authPage{Heading: "Not found", Lead: "There is nothing at this address, or you cannot see it.", Actions: []ui.ButtonProps{{Label: "Back to vink", Variant: "primary", Href: "/"}}})
	case errors.Is(err, domain.ErrForbidden):
		_ = h.authPage(c, http.StatusForbidden, authPage{Heading: "Not allowed", Lead: "Your role does not allow this.", Actions: []ui.ButtonProps{{Label: "Back", Variant: "primary", Href: "/"}}})
	case errors.Is(err, domain.ErrUnauthorized):
		_ = h.authPage(c, http.StatusUnauthorized, authPage{Heading: "Sign in", Lead: "Sign in to continue.", Actions: []ui.ButtonProps{{Label: "Sign in", Variant: "primary", Href: "/login"}}})
	default:
		reqID := middleware.GetRequestID(c.r.Context())
		h.log.Error("ui error", "err", err, "path", c.r.URL.Path, "req_id", reqID)
		_ = h.authPage(c, http.StatusInternalServerError, authPage{
			Heading: "Something broke", Lead: "The request failed inside vink. The server log has the reason.",
			KV:      []kv{{"status", "500"}, {"request", reqID}, {"time", c.now.Format("2006-01-02 15:04:05 MST")}},
			Note:    "Give your admin the request id.",
			Actions: []ui.ButtonProps{{Label: "Try again", Variant: "primary", Href: c.r.URL.RequestURI()}, {Label: "Copy details", Attrs: ui.Attr("data-copy", "500 "+reqID+" "+c.now.Format(time.RFC3339))}},
		})
	}
}

// base is the layout data every page carries.
type base struct {
	Title         string
	CSRF          string
	UserName      string
	OrgSlug       string
	ProjectSlug   string
	ProjectPath   string
	Section       string
	OpenIncidents int
	Query         string
	FilterState   string
	FilterTag     string
	Down          bool
	Fill          bool
	Version       string
}

func (h *Web) baseFor(c *reqCtx, title, section string) base {
	b := base{Title: title, CSRF: c.csrf(), Section: section, Version: version.Version}
	if c.principal != nil {
		b.UserName = c.principal.User.DisplayName
		if b.UserName == "" {
			b.UserName = c.principal.User.Subject
		}
	}
	if c.project != nil {
		b.OrgSlug, b.ProjectSlug, b.ProjectPath = c.org.Slug, c.project.Slug, c.projectPath()
		if counts, err := h.svc.MonitorCounts(c.r.Context(), c.scope); err == nil {
			b.Down = counts[domain.StateDown] > 0
		}
		if open, err := h.svc.ListIncidents(c.r.Context(), c.scope, true, 0, time.Time{}); err == nil {
			b.OpenIncidents = len(open)
		}
	}
	return b
}

type kv struct{ Key, Value string }

// authPage is a card on the auth layout: sign-in, proxy denied, no
// access, not found, and errors.
type authPage struct {
	base
	Heading string
	Lead    string
	KV      []kv
	Note    string
	Actions []ui.ButtonProps
}

func (h *Web) authPage(c *reqCtx, status int, page authPage) error {
	page.base = h.baseFor(c, page.Heading, "")
	page.ProjectPath = "" // the auth layout has no top bar
	page.Fill = true
	out, err := h.tmpl.Render("auth", "layout", page)
	if err != nil {
		http.Error(c.w, page.Heading, status)
		return nil
	}
	c.w.Header().Set("Content-Type", "text/html; charset=utf-8")
	c.w.WriteHeader(status)
	_, _ = c.w.Write(out)
	return nil
}

// proxyDenied is the 403 for a proxy-mode request without identity.
func (h *Web) proxyDenied(c *reqCtx) error {
	reqID := middleware.GetRequestID(c.r.Context())
	return h.authPage(c, http.StatusForbidden, authPage{
		Heading: "No identity from the proxy",
		Lead:    "This vink signs people in through an authenticating proxy, and this request arrived without a user. Open vink through the proxy’s address. If you did, ask your admin to check the proxy settings.",
		KV:      []kv{{"status", "403"}, {"request", reqID}, {"time", c.now.Format("2006-01-02 15:04:05 MST")}},
		Note:    "Give your admin the request id; the server log has the reason.",
		Actions: []ui.ButtonProps{{Label: "Try again", Variant: "primary", Href: c.r.URL.RequestURI()}, {Label: "Copy details", Attrs: ui.Attr("data-copy", "403 "+reqID+" "+c.now.Format(time.RFC3339))}},
	})
}

// noAccess is shown to a signed-in person with no org membership.
func (h *Web) noAccess(c *reqCtx) error {
	p := c.principal
	page := authPage{Heading: "Signed in, but not in an org yet"}
	if p.Source == "proxy" {
		page.Lead = "The proxy signed you in as " + p.User.Subject + ", but none of your groups gives access to an org in vink."
		page.KV = []kv{{"user", p.User.Subject}, {"groups", strings.Join(p.Groups, ", ")}, {"needs", "vink:<org>:<role>, for example vink:homelab:viewer"}}
		page.Note = "Ask an admin to add you to one of those groups. vink reads your groups again on the next page load."
		page.Actions = []ui.ButtonProps{{Label: "Reload", Variant: "primary", Href: "/"}}
		if h.authn.LogoutURL() != "" {
			page.Actions = append(page.Actions, ui.ButtonProps{Label: "Sign out", Href: h.authn.LogoutURL()})
		}
	} else {
		page.Lead = "You are signed in as " + p.User.Subject + ", but no org lists you as a member."
		page.KV = []kv{{"user", p.User.Subject}}
		page.Note = "Ask an admin to add you with vink admin user create --org <slug>, or to an org through the API."
		page.Actions = []ui.ButtonProps{{Label: "Reload", Variant: "primary", Href: "/"}, {Label: "Sign out", Type: "submit", Attrs: ui.Attr("form", "logout-form")}}
	}
	if len(page.KV) > 0 && page.KV[len(page.KV)-1].Key == "groups" && len(p.Groups) == 0 {
		page.KV[len(page.KV)-1].Value = "none"
	}
	page.base = h.baseFor(c, page.Heading, "")
	page.ProjectPath, page.Fill = "", true
	out, err := h.tmpl.Render("auth", "layout", page)
	if err != nil {
		return err
	}
	c.w.Header().Set("Content-Type", "text/html; charset=utf-8")
	c.w.WriteHeader(http.StatusForbidden)
	_, _ = c.w.Write(out)
	_, _ = c.w.Write([]byte(`<form id="logout-form" class="vk-sr" method="post" action="/logout"><input type="hidden" name="_csrf" value="` + c.csrf() + `"></form>`))
	return nil
}

// render writes a template; partials get an ETag and answer 304 when the
// client already has the same bytes.
func (h *Web) render(c *reqCtx, status int, page, name string, data any) error {
	out, err := h.tmpl.Render(page, name, data)
	if err != nil {
		return err
	}
	c.w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if name != "layout" && status == http.StatusOK {
		sum := sha256.Sum256(out)
		etag := `"` + hex.EncodeToString(sum[:8]) + `"`
		c.w.Header().Set("ETag", etag)
		if strings.Contains(c.r.Header.Get("If-None-Match"), etag) {
			c.w.WriteHeader(http.StatusNotModified)
			return nil
		}
	}
	c.w.WriteHeader(status)
	_, _ = c.w.Write(out)
	return nil
}

// redirect sends a browser or an htmx client to path.
func (h *Web) redirect(c *reqCtx, path string) error {
	if c.htmx() {
		c.w.Header().Set("HX-Redirect", path)
		c.w.WriteHeader(http.StatusNoContent)
		return nil
	}
	http.Redirect(c.w, c.r, path, http.StatusSeeOther)
	return nil
}

// safeNext keeps redirects on this site.
func safeNext(s string) string {
	if s == "" || !strings.HasPrefix(s, "/") || strings.HasPrefix(s, "//") || strings.Contains(s, "\\") {
		return "/"
	}
	return s
}
