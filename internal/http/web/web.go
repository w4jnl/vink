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
	"github.com/w4jnl/vink/internal/service"
)

// Web serves the UI.
type Web struct {
	svc    *service.Service
	authn  *auth.Authenticator
	log    *slog.Logger
	static *Static
	tmpl   *Templates
	now    func() time.Time
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
	mux.Handle("GET "+p+"/m/{slug}", h.project(h.monitor))
	mux.Handle("GET "+p+"/m/{slug}/edit", h.project(h.editMonitor))
	mux.Handle("POST "+p+"/m/{slug}/edit", h.project(h.updateMonitor))
	mux.Handle("POST "+p+"/m/{slug}/pause", h.project(h.pauseMonitor))
	mux.Handle("POST "+p+"/m/{slug}/resume", h.project(h.resumeMonitor))
	mux.Handle("POST "+p+"/m/{slug}/delete", h.project(h.deleteMonitor))
	mux.Handle("GET "+p+"/incidents", h.project(h.incidents))
	mux.Handle("POST "+p+"/incidents/{id}/ack", h.project(h.ackIncident))
	mux.Handle("GET "+p+"/settings", h.project(h.settingsRedirect))
	mux.Handle("GET "+p+"/settings/{tab}", h.project(h.settings))
	mux.Handle("POST "+p+"/settings/channels", h.project(h.createChannel))
	mux.Handle("POST "+p+"/settings/channels/{id}/delete", h.project(h.deleteChannel))
	mux.Handle("POST "+p+"/settings/channels/{id}/test", h.project(h.testChannel))
	mux.Handle("POST "+p+"/settings/routes", h.project(h.createRoute))
	mux.Handle("POST "+p+"/settings/routes/{id}/delete", h.project(h.deleteRoute))
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

// anonymous sends people to the login form in local mode and shows a 403
// in proxy-only mode.
func (h *Web) anonymous(c *reqCtx) error {
	if h.authn.LocalEnabled() {
		if c.r.Method != http.MethodGet {
			return domain.ErrUnauthorized
		}
		next := c.r.URL.RequestURI()
		http.Redirect(c.w, c.r, "/login?next="+url.QueryEscape(next), http.StatusSeeOther)
		return nil
	}
	return h.errorPage(c, http.StatusForbidden, "No identity", "The reverse proxy did not send an identity for this request.")
}

// fail maps an error to a page.
func (h *Web) fail(c *reqCtx, err error) {
	switch {
	case errors.Is(err, errCSRF):
		_ = h.errorPage(c, http.StatusForbidden, "Form expired", err.Error())
	case errors.Is(err, domain.ErrNotFound):
		_ = h.errorPage(c, http.StatusNotFound, "Not found", "There is nothing at this address, or you cannot see it.")
	case errors.Is(err, domain.ErrForbidden):
		_ = h.errorPage(c, http.StatusForbidden, "Not allowed", "Your role does not allow this.")
	case errors.Is(err, domain.ErrUnauthorized):
		_ = h.errorPage(c, http.StatusUnauthorized, "Sign in", "Sign in to continue.")
	default:
		h.log.Error("ui error", "err", err, "path", c.r.URL.Path, "req_id", middleware.GetRequestID(c.r.Context()))
		_ = h.errorPage(c, http.StatusInternalServerError, "Something broke", "Request "+middleware.GetRequestID(c.r.Context())+" failed. The log has the details.")
	}
}

// base is the layout data every page carries.
type base struct {
	Title       string
	CSRF        string
	UserName    string
	UserInitial string
	OrgSlug     string
	ProjectSlug string
	ProjectPath string
	Query       string
	FilterState string
	FilterTag   string
	Down        bool
}

func (h *Web) baseFor(c *reqCtx, title string) base {
	b := base{Title: title, CSRF: c.csrf()}
	if c.principal != nil {
		b.UserName = c.principal.User.DisplayName
		if b.UserName == "" {
			b.UserName = c.principal.User.Subject
		}
		b.UserInitial = strings.ToUpper(string([]rune(b.UserName)[:1]))
	}
	if c.project != nil {
		b.OrgSlug, b.ProjectSlug, b.ProjectPath = c.org.Slug, c.project.Slug, c.projectPath()
		if counts, err := h.svc.MonitorCounts(c.r.Context(), c.scope); err == nil {
			b.Down = counts[domain.StateDown] > 0
		}
	}
	return b
}

type errorData struct {
	base
	Heading  string
	Message  string
	BackPath string
}

func (h *Web) errorPage(c *reqCtx, status int, heading, message string) error {
	data := errorData{base: h.baseFor(c, heading), Heading: heading, Message: message}
	if c.project != nil {
		data.BackPath = c.projectPath()
	} else if c.principal != nil {
		data.BackPath = "/"
	}
	out, err := h.tmpl.Render("error", "layout", data)
	if err != nil {
		http.Error(c.w, heading, status)
		return nil
	}
	c.w.Header().Set("Content-Type", "text/html; charset=utf-8")
	c.w.WriteHeader(status)
	_, _ = c.w.Write(out)
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
