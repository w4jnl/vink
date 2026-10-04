package api

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/w4jnl/vink/internal/audit"
	"github.com/w4jnl/vink/internal/auth"
	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/http/middleware"
	"github.com/w4jnl/vink/internal/ratelimit"
	"github.com/w4jnl/vink/internal/service"
)

// Prefix is where the API lives.
const Prefix = "/api/v1"

// RequestsPerMinute is the per-caller rate limit.
const RequestsPerMinute = 600

// handlerFunc is a handler that returns an error for writeError.
type handlerFunc func(w http.ResponseWriter, r *http.Request) error

// API holds the dependencies of every handler.
type API struct {
	svc     *service.Service
	auth    *auth.Authenticator
	log     *slog.Logger
	limiter *ratelimit.Limiter
	// Routes records every registered method and path for the OpenAPI test.
	Routes []string
	// OrgRoutes are the org-level routes, mounted under /orgs/{org} for sessions only.
	OrgRoutes []string
}

// scopeMode says how a route finds its scope.
type scopeMode int

const (
	modeProject scopeMode = iota // bearer key, or a session on /orgs/{org}/projects/{project}
	modeMe                       // a session on /me
	modeOrg                      // a session on /orgs/{org}, org admins and owners
)

// New builds the API.
func New(svc *service.Service, a *auth.Authenticator, log *slog.Logger) *API {
	return &API{svc: svc, auth: a, log: log, limiter: ratelimit.New(RequestsPerMinute, RequestsPerMinute)}
}

// Mount registers every route on mux, once under /api/v1 for bearer keys
// and once under /api/v1/orgs/{org}/projects/{project} for sessions.
func (a *API) Mount(mux *http.ServeMux) {
	a.register(mux, "GET", "/me", a.me, true)
	a.register(mux, "GET", "/monitors", a.listMonitors, false)
	a.register(mux, "POST", "/monitors", a.createMonitor, false)
	a.register(mux, "GET", "/monitors/{slug}", a.getMonitor, false)
	a.register(mux, "PUT", "/monitors/{slug}", a.putMonitor, false)
	a.register(mux, "PATCH", "/monitors/{slug}", a.patchMonitor, false)
	a.register(mux, "DELETE", "/monitors/{slug}", a.deleteMonitor, false)
	a.register(mux, "POST", "/monitors/{slug}/pause", a.pauseMonitor, false)
	a.register(mux, "POST", "/monitors/{slug}/check", a.checkMonitor, false)
	a.register(mux, "POST", "/monitors/{slug}/resume", a.resumeMonitor, false)
	a.register(mux, "GET", "/monitors/{slug}/observations", a.listObservations, false)
	a.register(mux, "GET", "/monitors/{slug}/observations/{id}", a.getObservation, false)
	a.register(mux, "GET", "/monitors/{slug}/events", a.listEvents, false)
	a.register(mux, "GET", "/incidents", a.listIncidents, false)
	a.register(mux, "GET", "/incidents/{id}", a.getIncident, false)
	a.register(mux, "POST", "/incidents/{id}/ack", a.ackIncident, false)
	a.register(mux, "GET", "/channels", a.listChannels, false)
	a.register(mux, "POST", "/channels", a.createChannel, false)
	a.register(mux, "GET", "/channels/{id}", a.getChannel, false)
	a.register(mux, "PUT", "/channels/{id}", a.putChannel, false)
	a.register(mux, "DELETE", "/channels/{id}", a.deleteChannel, false)
	a.register(mux, "POST", "/channels/{id}/test", a.testChannel, false)
	a.register(mux, "GET", "/maintenance", a.listMaintenance, false)
	a.register(mux, "POST", "/maintenance", a.createMaintenance, false)
	a.register(mux, "GET", "/maintenance/{id}", a.getMaintenance, false)
	a.register(mux, "PUT", "/maintenance/{id}", a.putMaintenance, false)
	a.register(mux, "DELETE", "/maintenance/{id}", a.deleteMaintenance, false)
	a.register(mux, "POST", "/maintenance/{id}/end", a.endMaintenance, false)
	a.register(mux, "PUT", "/apply", a.applyProject, false)
	a.register(mux, "GET", "/export", a.exportProject, false)
	a.register(mux, "GET", "/status-pages", a.listStatusPages, false)
	a.register(mux, "POST", "/status-pages", a.createStatusPage, false)
	a.register(mux, "GET", "/status-pages/{slug}", a.getStatusPage, false)
	a.register(mux, "PUT", "/status-pages/{slug}", a.putStatusPage, false)
	a.register(mux, "DELETE", "/status-pages/{slug}", a.deleteStatusPage, false)
	a.register(mux, "GET", "/routes", a.listRoutes, false)
	a.register(mux, "POST", "/routes", a.createRoute, false)
	a.register(mux, "GET", "/routes/{id}", a.getRoute, false)
	a.register(mux, "PUT", "/routes/{id}", a.putRoute, false)
	a.register(mux, "DELETE", "/routes/{id}", a.deleteRoute, false)
	a.register(mux, "GET", "/keys", a.listKeys, false)
	a.register(mux, "POST", "/keys", a.createKey, false)
	a.register(mux, "DELETE", "/keys/{id}", a.deleteKey, false)
	a.register(mux, "POST", "/ping-key/rotate", a.rotatePingKey, false)
	a.register(mux, "GET", "/status", a.status, false)
	a.registerOrg(mux, "GET", "/agents", a.listAgents, false)
	a.registerOrg(mux, "POST", "/agents", a.createAgent, false)
	a.registerOrg(mux, "GET", "/agents/{name}", a.getAgent, false)
	a.registerOrg(mux, "PUT", "/agents/{name}/labels", a.putAgentLabels, false)
	a.registerOrg(mux, "DELETE", "/agents/{name}", a.deleteAgent, false)
	a.registerOrg(mux, "PUT", "/apply", a.applyOrg, true)
	a.registerOrg(mux, "GET", "/export", a.exportOrg, true)
	mux.HandleFunc("GET "+Prefix+"/openapi.yaml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		// the document's servers entry names the API's base under the deployment's path
		_, _ = w.Write(bytes.Replace(openAPI, []byte("  - url: "+Prefix+"\n"), []byte("  - url: "+middleware.Href(r, Prefix)+"\n"), 1)) //nolint:gosec // G705: the prefix is the configured mount path the router recorded, not client input
	})
	// Anything else under the prefix is a JSON 404, never HTML.
	mux.HandleFunc(Prefix+"/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, a.log, domain.NotFound("route"))
	})
}

// RegisterExtra lets later packages add a route with the same wrapping.
func (a *API) RegisterExtra(mux *http.ServeMux, method, path string, h handlerFunc) {
	a.register(mux, method, path, h, false)
}

func (a *API) register(mux *http.ServeMux, method, path string, h handlerFunc, meRoute bool) {
	a.Routes = append(a.Routes, method+" "+path)
	mode := modeProject
	if meRoute {
		mode = modeMe
	}
	mux.Handle(method+" "+Prefix+path, a.wrap(h, mode, false, false))
	mux.Handle(method+" "+Prefix+"/orgs/{org}/projects/{project}"+path, a.wrap(h, mode, true, false))
}

// registerOrg mounts an org-level route under /orgs/{org}, for sessions
// of org admins and owners. Project keys are refused; org keys are
// accepted only where orgKeys is true (export and apply).
func (a *API) registerOrg(mux *http.ServeMux, method, path string, h handlerFunc, orgKeys bool) {
	a.OrgRoutes = append(a.OrgRoutes, method+" "+path)
	mux.Handle(method+" "+Prefix+"/orgs/{org}"+path, a.wrap(h, modeOrg, true, orgKeys))
}

// wrap resolves the caller's scope, applies the rate limit, the read-only
// and CSRF rules, and turns handler errors into problems.
func (a *API) wrap(h handlerFunc, mode scopeMode, sessionPath, orgKeys bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "same-origin")
		ctx := audit.WithRequest(r.Context(), audit.Request{Via: audit.ViaAPI, RequestID: middleware.GetRequestID(r.Context()), RemoteAddr: middleware.ClientIP(r)})

		var (
			sc        domain.Scope
			principal *auth.Principal
			err       error
		)
		if token, ok := auth.BearerToken(r); ok {
			sc, err = a.auth.KeyScope(ctx, token)
			if err != nil {
				writeError(w, r, a.log, err)
				return
			}
			if err := a.keyAllowed(r, sc, mode, orgKeys); err != nil {
				writeError(w, r, a.log, err)
				return
			}
		} else {
			principal, err = a.auth.Identify(r)
			if err != nil {
				writeError(w, r, a.log, err)
				return
			}
			if principal == nil {
				writeError(w, r, a.log, domain.ErrUnauthorized)
				return
			}
			if !isRead(r.Method) && !a.auth.CheckCSRF(r, principal) {
				writeError(w, r, a.log, errors.Join(domain.ErrForbidden, errors.New("missing or invalid CSRF token")))
				return
			}
			switch {
			case mode == modeOrg:
				sc, err = a.orgScope(r, principal)
				if err != nil {
					writeError(w, r, a.log, err)
					return
				}
			case sessionPath:
				sc, err = a.sessionScope(r, principal)
				if err != nil {
					writeError(w, r, a.log, err)
					return
				}
			case mode == modeMe:
				sc = domain.Scope{UserID: principal.User.ID, InstanceAdmin: principal.InstanceAdmin, Actor: "user:" + principal.User.Subject}
			default:
				writeError(w, r, a.log, errors.Join(domain.ErrUnauthorized, errors.New("sessions must use /api/v1/orgs/{org}/projects/{project}/…")))
				return
			}
		}
		if ok, wait := a.limiter.Allow(sc.Actor); !ok {
			w.Header().Set("Retry-After", retryAfter(wait))
			writeError(w, r, a.log, domain.ErrRateLimited)
			return
		}
		if sc.IsKey() && sc.KeyAccess == domain.AccessRO && !isRead(r.Method) {
			writeError(w, r, a.log, errors.Join(domain.ErrForbidden, errors.New("read-only API key")))
			return
		}
		ctx = auth.WithScope(ctx, sc)
		if principal != nil {
			ctx = auth.WithPrincipal(ctx, principal)
		}
		if err := h(w, r.WithContext(ctx)); err != nil {
			writeError(w, r, a.log, err)
		}
	})
}

// keyAllowed says whether an API key may call this route: a project key
// acts as its project and never for the org; an org key may see /me and
// the org routes marked for it, on its own org only, and nothing else.
func (a *API) keyAllowed(r *http.Request, sc domain.Scope, mode scopeMode, orgKeys bool) error {
	if !sc.IsOrgKey() {
		if mode == modeOrg {
			return errors.Join(domain.ErrForbidden, errors.New("a project key acts as its project; org routes take a session or an org key"))
		}
		return nil
	}
	switch mode {
	case modeMe:
		return nil
	case modeOrg:
		org, err := a.svc.OrgBySlug(r.Context(), r.PathValue("org"))
		if err != nil || org.ID != sc.OrgID {
			return domain.NotFound("org")
		}
		if !orgKeys {
			return errors.Join(domain.ErrForbidden, errors.New("an org key may only export and apply the org"))
		}
		return nil
	default:
		return errors.Join(domain.ErrForbidden, errors.New("an org key may only export and apply the org; use a project key"))
	}
}

// orgScope resolves the org from the path for a principal; a principal
// without a role there gets a 404, one without the admin role a 403.
func (a *API) orgScope(r *http.Request, p *auth.Principal) (domain.Scope, error) {
	org, err := a.svc.OrgBySlug(r.Context(), r.PathValue("org"))
	if err != nil {
		return domain.Scope{}, domain.NotFound("org")
	}
	sc, err := a.auth.OrgScope(p, org)
	if err != nil {
		return domain.Scope{}, err
	}
	if !sc.InstanceAdmin && !sc.CanAdminOrg() {
		return domain.Scope{}, domain.ErrForbidden
	}
	return sc, nil
}

// sessionScope resolves org and project from the path for a principal.
func (a *API) sessionScope(r *http.Request, p *auth.Principal) (domain.Scope, error) {
	ctx := r.Context()
	org, err := a.svc.OrgBySlug(ctx, r.PathValue("org"))
	if err != nil {
		return domain.Scope{}, domain.NotFound("project")
	}
	project, err := a.svc.ProjectBySlug(ctx, org.ID, r.PathValue("project"))
	if err != nil {
		return domain.Scope{}, domain.NotFound("project")
	}
	return a.auth.UserScope(p, project)
}

func isRead(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}

func retryAfter(d time.Duration) string {
	return strconv.Itoa(int(d.Seconds()) + 1)
}

// scope returns the scope stored by wrap.
func scope(r *http.Request) domain.Scope {
	sc, _ := auth.ScopeFrom(r.Context())
	return sc
}
