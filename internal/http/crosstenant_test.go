package http

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/w4jnl/vink/internal/auth"
	"github.com/w4jnl/vink/internal/config"
	"github.com/w4jnl/vink/internal/db/dbtest"
	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/engine"
	"github.com/w4jnl/vink/internal/http/api"
	"github.com/w4jnl/vink/internal/service"
)

// tenant is one org with one project, its keys, a session and some data.
type tenant struct {
	org, project        string
	rw, ro              string
	cookie              *http.Cookie
	csrf                string
	monitor, obs, event string
	incident, channel   string
	route, key          string
}

func TestCrossTenantIsolation(t *testing.T) {
	d := dbtest.Open(t)
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := service.New(d, nil, quiet, service.DefaultConfig())
	cfg := config.Default()
	authn, err := auth.New(svc, cfg.Auth, cfg.Server.BaseURL, quiet)
	if err != nil {
		t.Fatal(err)
	}
	sched := engine.NewScheduler(svc, svc.Bus(), quiet, nil)
	h := Handler(Deps{Cfg: cfg, Svc: svc, Auth: authn, Log: quiet, Sched: sched}, true)
	ctx := context.Background()
	admin := domain.Scope{InstanceAdmin: true, Role: domain.RoleOwner}

	mk := func(name string) *tenant {
		org, _ := svc.CreateOrg(ctx, admin, name, name)
		project, _ := svc.CreateProject(ctx, admin, org.ID, "prod", "Prod", "UTC")
		sc := domain.Scope{OrgID: org.ID, ProjectID: project.ID, Role: domain.RoleAdmin, Actor: "seed"}
		tn := &tenant{org: name, project: "prod"}
		_, tn.rw, _ = svc.CreateAPIKey(ctx, sc, "rw", domain.AccessRW)
		_, tn.ro, _ = svc.CreateAPIKey(ctx, sc, "ro", domain.AccessRO)
		user, _ := svc.CreateLocalUser(ctx, admin, name+"-user", "", "", "correct horse", false)
		_ = svc.SetMembership(ctx, admin, user.ID, org.ID, domain.RoleAdmin)
		rec := httptest.NewRecorder()
		p, err := authn.Login(rec, httptest.NewRequest("POST", "/login", nil), user.Subject, "correct horse")
		if err != nil {
			t.Fatal(err)
		}
		tn.cookie, tn.csrf = rec.Result().Cookies()[0], p.CSRF()
		m, _ := svc.CreateMonitor(ctx, sc, &domain.Monitor{Slug: name + "-job", Kind: domain.KindHeartbeat, Heartbeat: &domain.HeartbeatSpec{Schedule: domain.Schedule{Period: domain.MustDuration("1h")}}})
		tn.monitor = m.Slug
		tgt, _ := svc.ResolvePing(ctx, project.PingKey, m.Slug, "", false)
		obs, _, _ := svc.RecordPing(ctx, tgt, service.PingObservation{Signal: domain.SignalFail, Body: []byte("x")})
		tn.obs = obs.ID
		events, _ := svc.ListEvents(ctx, sc, m.Slug, 1)
		tn.event = events[0].ID
		incs, _ := svc.ListIncidents(ctx, sc, true, 0)
		tn.incident = incs[0].ID
		ch, _ := svc.CreateChannel(ctx, sc, &domain.Channel{Name: "hook", Kind: domain.ChannelWebhook, Config: json.RawMessage(`{"url":"https://hooks.example.com/x"}`), Enabled: true})
		tn.channel = ch.ID
		routes, _ := svc.ListRoutes(ctx, sc)
		tn.route = routes[0].ID
		keys, _ := svc.ListAPIKeys(ctx, sc)
		tn.key = keys[0].ID
		return tn
	}
	a, b := mk("alpha"), mk("beta")

	// every route with a path parameter, filled with tenant A's ids
	fill := func(path string) string {
		r := strings.NewReplacer("{slug}", a.monitor, "{id}", "")
		out := r.Replace(path)
		switch {
		case strings.Contains(path, "/observations/{id}"):
			out = strings.Replace(path, "{slug}", a.monitor, 1)
			out = strings.Replace(out, "{id}", a.obs, 1)
		case strings.Contains(path, "/incidents/{id}"):
			out = strings.Replace(path, "{id}", a.incident, 1)
		case strings.Contains(path, "/channels/{id}"):
			out = strings.Replace(path, "{id}", a.channel, 1)
		case strings.Contains(path, "/routes/{id}"):
			out = strings.Replace(path, "{id}", a.route, 1)
		case strings.Contains(path, "/keys/{id}"):
			out = strings.Replace(path, "{id}", a.key, 1)
		}
		return out
	}
	spec := api.New(nil, nil, quiet)
	spec.Mount(http.NewServeMux())

	type caller struct {
		name  string
		apply func(r *http.Request)
		path  func(p string) string
		ro    bool
	}
	callers := []caller{
		{"rw key", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+b.rw) }, func(p string) string { return "/api/v1" + p }, false},
		{"ro key", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+b.ro) }, func(p string) string { return "/api/v1" + p }, true},
		{"session", func(r *http.Request) { r.AddCookie(b.cookie); r.Header.Set(auth.CSRFHeader, b.csrf) }, func(p string) string { return "/api/v1/orgs/beta/projects/prod" + p }, false},
		{"session via A's org path", func(r *http.Request) { r.AddCookie(b.cookie); r.Header.Set(auth.CSRFHeader, b.csrf) }, func(p string) string { return "/api/v1/orgs/alpha/projects/prod" + p }, false},
	}
	checked := 0
	for _, route := range spec.Routes {
		method, path, _ := strings.Cut(route, " ")
		hasParam := strings.Contains(path, "{")
		for _, c := range callers {
			t.Run(c.name+" "+route, func(t *testing.T) {
				var body io.Reader
				if method == "POST" || method == "PUT" || method == "PATCH" {
					body = bytes.NewReader([]byte(`{}`))
				}
				req := httptest.NewRequest(method, c.path(fill(path)), body)
				if body != nil {
					req.Header.Set("Content-Type", "application/json")
				}
				c.apply(req)
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				checked++
				viaAlpha := strings.Contains(c.name, "A's org")
				switch {
				case c.ro && method != "GET":
					if rec.Code != 403 {
						t.Fatalf("ro key write: %d %s", rec.Code, rec.Body.String())
					}
				case viaAlpha:
					// B has no role in alpha: everything is 404, even /me
					if rec.Code != 404 {
						t.Fatalf("foreign org path: %d %s", rec.Code, rec.Body.String())
					}
				case hasParam:
					if rec.Code != 404 {
						t.Fatalf("A's resource through B: %d %s", rec.Code, rec.Body.String())
					}
				case method == "GET":
					// lists and summaries must not show A's data
					if rec.Code != 200 && rec.Code != 403 {
						t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
					}
					if bytes.Contains(rec.Body.Bytes(), []byte(a.monitor+`"`)) && bytes.Contains(rec.Body.Bytes(), []byte(`"alpha"`)) {
						t.Fatalf("list leaks A: %s", rec.Body.String())
					}
					for _, id := range []string{a.obs, a.event, a.incident, a.channel, a.route, a.key} {
						if bytes.Contains(rec.Body.Bytes(), []byte(id)) {
							t.Fatalf("list leaks A id %s: %s", id, rec.Body.String())
						}
					}
				}
			})
		}
	}
	if checked < 100 {
		t.Fatalf("only %d cross-tenant checks ran", checked)
	}

	// pings: A's key cannot reach B's monitor slug in B's project
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/ping/"+pingKey(t, svc, "alpha")+"/alpha-job", nil))
	if rec.Code != 200 {
		t.Fatalf("own ping: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/ping/"+pingKey(t, svc, "beta")+"/alpha-job", nil))
	if rec.Code != 404 {
		t.Fatalf("B's key on A's slug must be 404: %d", rec.Code)
	}
	// UI: B's session cannot open A's project pages
	for _, p := range []string{"/o/alpha/p/prod", "/o/alpha/p/prod/m/alpha-job", "/o/alpha/p/prod/incidents", "/o/alpha/p/prod/settings/keys"} {
		req := httptest.NewRequest("GET", p, nil)
		req.AddCookie(b.cookie)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 404 {
			t.Errorf("UI %s via B: %d", p, rec.Code)
		}
	}
	// and B's own pages work
	req := httptest.NewRequest("GET", "/o/beta/p/prod/m/beta-job", nil)
	req.AddCookie(b.cookie)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Errorf("own UI page: %d", rec.Code)
	}
}

func pingKey(t *testing.T, svc *service.Service, orgSlug string) string {
	t.Helper()
	org, err := svc.OrgBySlug(context.Background(), orgSlug)
	if err != nil {
		t.Fatal(err)
	}
	p, err := svc.ProjectBySlug(context.Background(), org.ID, "prod")
	if err != nil {
		t.Fatal(err)
	}
	return p.PingKey
}
