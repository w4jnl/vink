package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/w4jnl/vink/internal/apply"
	"github.com/w4jnl/vink/internal/auth"
	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/http/middleware"
	"github.com/w4jnl/vink/internal/service"
)

// --- me -------------------------------------------------------------------

type meOut struct {
	Actor       string              `json:"actor"`
	Kind        string              `json:"kind"`
	User        *meUser             `json:"user,omitempty"`
	Key         *meKey              `json:"key,omitempty"`
	Org         *meRef              `json:"org,omitempty"`
	Project     *meProject          `json:"project,omitempty"`
	Role        domain.Role         `json:"role,omitempty"`
	Memberships []domain.Membership `json:"memberships,omitempty"`
}

type meUser struct {
	Subject       string `json:"subject"`
	Email         string `json:"email,omitempty"`
	Name          string `json:"name,omitempty"`
	InstanceAdmin bool   `json:"instance_admin"`
}

type meKey struct {
	Prefix string        `json:"prefix"`
	Access domain.Access `json:"access"`
	// Kind is project, org or admin (an instance admin key).
	Kind string `json:"kind"`
	Name string `json:"name,omitempty"`
	// ExpiresAt is set for admin keys, which always expire.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

type meRef struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
}

type meProject struct {
	ID       string `json:"id"`
	Slug     string `json:"slug"`
	Name     string `json:"name"`
	Timezone string `json:"timezone"`
	PingKey  string `json:"ping_key,omitempty"`
	PingBase string `json:"ping_base,omitempty"`
}

func (a *API) me(w http.ResponseWriter, r *http.Request) error {
	sc := scope(r)
	ctx := r.Context()
	out := meOut{Actor: sc.Actor, Role: sc.Role}
	if p := auth.PrincipalFrom(ctx); p != nil {
		out.Kind = "user"
		out.User = &meUser{Subject: p.User.Subject, Email: p.User.Email, Name: p.User.DisplayName, InstanceAdmin: p.InstanceAdmin}
		out.Memberships = p.Memberships
	} else {
		out.Kind = "key"
		out.Key = &meKey{Prefix: strings.TrimPrefix(sc.Actor, "key:"), Access: sc.KeyAccess, Name: sc.KeyName}
		switch {
		case sc.IsAdminKey():
			out.Key.Kind = "admin"
			keys, err := a.svc.ListAdminKeys(ctx, sc)
			if err != nil {
				return err
			}
			for _, k := range keys {
				if k.ID == sc.KeyID {
					at := k.ExpiresAt
					out.Key.ExpiresAt = &at
				}
			}
		case sc.IsOrgKey():
			out.Key.Kind = "org"
		default:
			out.Key.Kind = "project"
		}
	}
	if sc.ProjectID != "" {
		project, err := a.svc.Project(ctx, sc)
		if err != nil {
			return err
		}
		org, err := a.svc.OrgByID(ctx, project.OrgID)
		if err != nil {
			return err
		}
		out.Org = &meRef{ID: org.ID, Slug: org.Slug}
		out.Project = &meProject{ID: project.ID, Slug: project.Slug, Name: project.Name, Timezone: project.Timezone}
		if sc.CanSeePingKey() {
			out.Project.PingKey = project.PingKey
			out.Project.PingBase = a.svc.Config().PingBaseURL + "/ping/"
		}
	} else if sc.OrgID != "" {
		// an org key: the org, no project
		org, err := a.svc.OrgByID(ctx, sc.OrgID)
		if err != nil {
			return err
		}
		out.Org = &meRef{ID: org.ID, Slug: org.Slug}
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// --- monitors -------------------------------------------------------------

func (a *API) listMonitors(w http.ResponseWriter, r *http.Request) error {
	sc := scope(r)
	ctx := r.Context()
	q := r.URL.Query()
	limit, err := limitParam(r)
	if err != nil {
		return err
	}
	cursor, err := decodeCursor(q.Get("cursor"), 2)
	if err != nil {
		return err
	}
	f := service.MonitorFilter{Tag: q.Get("tag"), Query: q.Get("q"), State: domain.State(q.Get("state")), Kind: domain.Kind(q.Get("kind"))}
	if f.State != "" && !f.State.Valid() {
		return badRequest("unknown state %q", f.State)
	}
	if f.Kind != "" && !f.Kind.Valid() {
		return badRequest("unknown kind %q", f.Kind)
	}
	project, err := a.svc.Project(ctx, sc)
	if err != nil {
		return err
	}
	all, err := a.svc.ListMonitors(ctx, sc, f)
	if err != nil {
		return err
	}
	items := make([]MonitorOut, 0, limit)
	var next *string
	for _, m := range all {
		if cursor != nil {
			key := strings.ToLower(m.Name)
			if key < cursor[0] || (key == cursor[0] && m.ID <= cursor[1]) {
				continue
			}
		}
		if len(items) == limit {
			c := encodeCursor(strings.ToLower(items[len(items)-1].Name), items[len(items)-1].ID)
			next = &c
			break
		}
		items = append(items, monitorOut(a.svc, project, m, sc.CanSeePingKey()))
	}
	writeJSON(w, http.StatusOK, page[MonitorOut]{Items: items, NextCursor: next})
	return nil
}

func (a *API) createMonitor(w http.ResponseWriter, r *http.Request) error {
	sc := scope(r)
	var in MonitorIn
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	if in.Slug == "" && in.Name != "" {
		in.Slug = domain.Slugify(in.Name)
	}
	project, err := a.svc.Project(r.Context(), sc)
	if err != nil {
		return err
	}
	m, err := a.svc.CreateMonitor(r.Context(), sc, in.toDomain())
	if err != nil {
		return err
	}
	w.Header().Set("Location", locationFor(r, "/monitors/"+m.Slug))
	writeJSON(w, http.StatusCreated, monitorOut(a.svc, project, m, sc.CanSeePingKey()))
	return nil
}

// locationFor builds a Location header under the same prefix form the
// request used, and under the deployment's path.
func locationFor(r *http.Request, path string) string {
	org, project := r.PathValue("org"), r.PathValue("project")
	switch {
	case org != "" && project != "":
		return middleware.Href(r, Prefix+"/orgs/"+org+"/projects/"+project+path)
	case org != "": // an org-level route
		return middleware.Href(r, Prefix+"/orgs/"+org+path)
	}
	return middleware.Href(r, Prefix+path)
}

func (a *API) getMonitor(w http.ResponseWriter, r *http.Request) error {
	sc := scope(r)
	ctx := r.Context()
	project, err := a.svc.Project(ctx, sc)
	if err != nil {
		return err
	}
	m, err := a.svc.MonitorBySlug(ctx, sc, r.PathValue("slug"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, monitorOut(a.svc, project, m, sc.CanSeePingKey()))
	return nil
}

func (a *API) putMonitor(w http.ResponseWriter, r *http.Request) error {
	var in MonitorIn
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	return a.updateMonitor(w, r, in)
}

func (a *API) patchMonitor(w http.ResponseWriter, r *http.Request) error {
	sc := scope(r)
	cur, err := a.svc.MonitorBySlug(r.Context(), sc, r.PathValue("slug"))
	if err != nil {
		return err
	}
	in := monitorInFrom(cur)
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		return badRequest("read body: %v", err)
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return badRequest("invalid JSON body: %v", err)
	}
	return a.updateMonitor(w, r, in)
}

func (a *API) updateMonitor(w http.ResponseWriter, r *http.Request, in MonitorIn) error {
	sc := scope(r)
	ctx := r.Context()
	project, err := a.svc.Project(ctx, sc)
	if err != nil {
		return err
	}
	m, err := a.svc.UpdateMonitor(ctx, sc, r.PathValue("slug"), in.toDomain())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, monitorOut(a.svc, project, m, sc.CanSeePingKey()))
	return nil
}

func (a *API) deleteMonitor(w http.ResponseWriter, r *http.Request) error {
	if err := a.svc.DeleteMonitor(r.Context(), scope(r), r.PathValue("slug")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (a *API) pauseMonitor(w http.ResponseWriter, r *http.Request) error {
	return a.setPaused(w, r, true)
}

func (a *API) resumeMonitor(w http.ResponseWriter, r *http.Request) error {
	return a.setPaused(w, r, false)
}

func (a *API) setPaused(w http.ResponseWriter, r *http.Request, paused bool) error {
	sc := scope(r)
	ctx := r.Context()
	project, err := a.svc.Project(ctx, sc)
	if err != nil {
		return err
	}
	var m *domain.Monitor
	if paused {
		m, err = a.svc.PauseMonitor(ctx, sc, r.PathValue("slug"))
	} else {
		m, err = a.svc.ResumeMonitor(ctx, sc, r.PathValue("slug"))
	}
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, monitorOut(a.svc, project, m, sc.CanSeePingKey()))
	return nil
}

// checkMonitor runs a pull monitor now and returns it fresh.
func (a *API) checkMonitor(w http.ResponseWriter, r *http.Request) error {
	sc := scope(r)
	ctx := r.Context()
	project, err := a.svc.Project(ctx, sc)
	if err != nil {
		return err
	}
	m, err := a.svc.CheckNow(ctx, sc, r.PathValue("slug"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, monitorOut(a.svc, project, m, sc.CanSeePingKey()))
	return nil
}

// --- observations and events ---------------------------------------------

func timeParam(r *http.Request, name string) (time.Time, error) {
	s := r.URL.Query().Get(name)
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.Unix(n, 0).UTC(), nil
	}
	return time.Time{}, badRequest("%s must be RFC 3339 or a Unix timestamp", name)
}

func (a *API) listObservations(w http.ResponseWriter, r *http.Request) error {
	sc := scope(r)
	limit, err := limitParam(r)
	if err != nil {
		return err
	}
	since, err := timeParam(r, "since")
	if err != nil {
		return err
	}
	until, err := timeParam(r, "until")
	if err != nil {
		return err
	}
	p, err := historyPage(r, since, until, limit)
	if err != nil {
		return err
	}
	if p.Kind = r.URL.Query().Get("kind"); p.Kind == service.KindChange || !service.ValidHistoryKind(p.Kind) {
		return badRequest("kind must be ok, fail or run")
	}
	obs, err := a.svc.ListObservations(r.Context(), sc, r.PathValue("slug"), p)
	if err != nil {
		return err
	}
	items := make([]ObservationOut, 0, len(obs))
	for _, o := range obs {
		items = append(items, observationOut(o))
	}
	var next *string
	if len(obs) == limit {
		last := obs[len(obs)-1]
		c := encodeCursor(strconv.FormatInt(domain.Millis(last.At), 10), last.ID)
		next = &c
	}
	writeJSON(w, http.StatusOK, page[ObservationOut]{Items: items, NextCursor: next})
	return nil
}

func (a *API) getObservation(w http.ResponseWriter, r *http.Request) error {
	sc := scope(r)
	ctx := r.Context()
	m, err := a.svc.MonitorBySlug(ctx, sc, r.PathValue("slug"))
	if err != nil {
		return err
	}
	o, err := a.svc.Observation(ctx, sc, r.PathValue("id"))
	if err != nil || o.MonitorID != m.ID {
		return domain.NotFound("observation")
	}
	if r.URL.Query().Get("body") == "1" {
		body, ct, err := a.svc.ObservationBody(ctx, sc, o.ID)
		if err != nil {
			return err
		}
		// Bodies are opaque bytes from pinging jobs: always plain text, never
		// sniffed, whatever content type the job declared.
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Disposition", "inline")
		w.Header().Set("X-Original-Content-Type", ct)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body) //nolint:gosec // G705: served as text/plain with nosniff, never rendered as HTML
		return nil
	}
	writeJSON(w, http.StatusOK, observationOut(o))
	return nil
}

// historyPage reads the window and cursor shared by observations and events.
func historyPage(r *http.Request, since, until time.Time, limit int) (service.HistoryPage, error) {
	p := service.HistoryPage{Since: since, Until: until, Limit: limit}
	cursor, err := decodeCursor(r.URL.Query().Get("cursor"), 2)
	if err != nil {
		return p, err
	}
	if cursor != nil {
		ms, err := strconv.ParseInt(cursor[0], 10, 64)
		if err != nil {
			return p, badRequest("invalid cursor")
		}
		p.CursorAt, p.CursorID = domain.FromMillis(ms), cursor[1]
	}
	return p, nil
}

func (a *API) listEvents(w http.ResponseWriter, r *http.Request) error {
	limit, err := limitParam(r)
	if err != nil {
		return err
	}
	since, err := timeParam(r, "since")
	if err != nil {
		return err
	}
	until, err := timeParam(r, "until")
	if err != nil {
		return err
	}
	p, err := historyPage(r, since, until, limit)
	if err != nil {
		return err
	}
	events, err := a.svc.ListEventsPage(r.Context(), scope(r), r.PathValue("slug"), p)
	if err != nil {
		return err
	}
	items := make([]EventOut, 0, len(events))
	for _, e := range events {
		items = append(items, eventOut(e))
	}
	var next *string
	if len(events) == limit {
		last := events[len(events)-1]
		c := encodeCursor(strconv.FormatInt(domain.Millis(last.At), 10), last.ID)
		next = &c
	}
	writeJSON(w, http.StatusOK, page[EventOut]{Items: items, NextCursor: next})
	return nil
}

// --- incidents ------------------------------------------------------------

func (a *API) listIncidents(w http.ResponseWriter, r *http.Request) error {
	limit, err := limitParam(r)
	if err != nil {
		return err
	}
	open := r.URL.Query().Get("open") == "1"
	incidents, err := a.svc.ListIncidents(r.Context(), scope(r), open, limit, time.Time{})
	if err != nil {
		return err
	}
	items := make([]IncidentOut, 0, len(incidents))
	for _, i := range incidents {
		items = append(items, incidentOut(i))
	}
	writeJSON(w, http.StatusOK, page[IncidentOut]{Items: items})
	return nil
}

func (a *API) getIncident(w http.ResponseWriter, r *http.Request) error {
	inc, err := a.svc.Incident(r.Context(), scope(r), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, incidentOut(inc))
	return nil
}

func (a *API) ackIncident(w http.ResponseWriter, r *http.Request) error {
	inc, err := a.svc.AckIncident(r.Context(), scope(r), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, incidentOut(inc))
	return nil
}

// --- channels -------------------------------------------------------------

func (a *API) listChannels(w http.ResponseWriter, r *http.Request) error {
	channels, err := a.svc.ListChannels(r.Context(), scope(r))
	if err != nil {
		return err
	}
	items := make([]ChannelOut, 0, len(channels))
	for _, c := range channels {
		items = append(items, channelOut(c))
	}
	writeJSON(w, http.StatusOK, page[ChannelOut]{Items: items})
	return nil
}

func (a *API) createChannel(w http.ResponseWriter, r *http.Request) error {
	var in ChannelIn
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	c, err := a.svc.CreateChannel(r.Context(), scope(r), in.toDomain())
	if err != nil {
		return err
	}
	w.Header().Set("Location", locationFor(r, "/channels/"+c.ID))
	writeJSON(w, http.StatusCreated, channelOut(c))
	return nil
}

func (a *API) getChannel(w http.ResponseWriter, r *http.Request) error {
	c, err := a.svc.Channel(r.Context(), scope(r), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, channelOut(c))
	return nil
}

func (a *API) putChannel(w http.ResponseWriter, r *http.Request) error {
	var in ChannelIn
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	c, err := a.svc.UpdateChannel(r.Context(), scope(r), r.PathValue("id"), in.toDomain())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, channelOut(c))
	return nil
}

func (a *API) deleteChannel(w http.ResponseWriter, r *http.Request) error {
	if err := a.svc.DeleteChannel(r.Context(), scope(r), r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// --- routes ---------------------------------------------------------------

func (a *API) listRoutes(w http.ResponseWriter, r *http.Request) error {
	routes, err := a.svc.ListRoutes(r.Context(), scope(r))
	if err != nil {
		return err
	}
	items := make([]RouteOut, 0, len(routes))
	for _, rt := range routes {
		items = append(items, routeOut(rt))
	}
	writeJSON(w, http.StatusOK, page[RouteOut]{Items: items})
	return nil
}

func (a *API) createRoute(w http.ResponseWriter, r *http.Request) error {
	var in RouteIn
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	rt, err := a.svc.CreateRoute(r.Context(), scope(r), in.toDomain())
	if err != nil {
		return err
	}
	w.Header().Set("Location", locationFor(r, "/routes/"+rt.ID))
	writeJSON(w, http.StatusCreated, routeOut(rt))
	return nil
}

func (a *API) getRoute(w http.ResponseWriter, r *http.Request) error {
	rt, err := a.svc.Route(r.Context(), scope(r), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, routeOut(rt))
	return nil
}

func (a *API) putRoute(w http.ResponseWriter, r *http.Request) error {
	var in RouteIn
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	rt, err := a.svc.UpdateRoute(r.Context(), scope(r), r.PathValue("id"), in.toDomain())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, routeOut(rt))
	return nil
}

func (a *API) deleteRoute(w http.ResponseWriter, r *http.Request) error {
	if err := a.svc.DeleteRoute(r.Context(), scope(r), r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// --- keys and ping key ----------------------------------------------------

func (a *API) listKeys(w http.ResponseWriter, r *http.Request) error {
	sc := scope(r)
	if sc.IsKey() && sc.KeyAccess != domain.AccessRW {
		return errors.Join(domain.ErrForbidden, errors.New("rw keys only"))
	}
	keys, err := a.svc.ListAPIKeys(r.Context(), sc)
	if err != nil {
		return err
	}
	items := make([]KeyOut, 0, len(keys))
	for _, k := range keys {
		items = append(items, keyOut(k))
	}
	writeJSON(w, http.StatusOK, page[KeyOut]{Items: items})
	return nil
}

type keyIn struct {
	Name   string        `json:"name"`
	Access domain.Access `json:"access"`
}

func (a *API) createKey(w http.ResponseWriter, r *http.Request) error {
	var in keyIn
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	if in.Access == "" {
		in.Access = domain.AccessRO
	}
	k, plain, err := a.svc.CreateAPIKey(r.Context(), scope(r), in.Name, in.Access)
	if err != nil {
		return err
	}
	out := keyOut(k)
	out.Key = plain
	writeJSON(w, http.StatusCreated, out)
	return nil
}

func (a *API) deleteKey(w http.ResponseWriter, r *http.Request) error {
	if err := a.svc.RevokeAPIKey(r.Context(), scope(r), r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (a *API) rotatePingKey(w http.ResponseWriter, r *http.Request) error {
	p, err := a.svc.RotatePingKey(r.Context(), scope(r))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"ping_key": p.PingKey, "previous_key_valid_until": p.PingKeyPrevUntil})
	return nil
}

// --- status ---------------------------------------------------------------

func (a *API) status(w http.ResponseWriter, r *http.Request) error {
	s, err := a.svc.Status(r.Context(), scope(r))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, statusOut(s))
	return nil
}

// --- maintenance windows ------------------------------------------------

func (a *API) listMaintenance(w http.ResponseWriter, r *http.Request) error {
	list, err := a.svc.ListMaintenance(r.Context(), scope(r))
	if err != nil {
		return err
	}
	now := a.svc.Now()
	out := make([]MaintenanceOut, 0, len(list))
	for _, m := range list {
		out = append(out, maintenanceOut(m, now))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
	return nil
}

func (a *API) createMaintenance(w http.ResponseWriter, r *http.Request) error {
	var in MaintenanceIn
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	win, err := in.toDomain()
	if err != nil {
		return err
	}
	created, err := a.svc.CreateMaintenance(r.Context(), scope(r), win)
	if err != nil {
		return err
	}
	w.Header().Set("Location", locationFor(r, "/maintenance/"+created.ID))
	writeJSON(w, http.StatusCreated, maintenanceOut(created, a.svc.Now()))
	return nil
}

func (a *API) getMaintenance(w http.ResponseWriter, r *http.Request) error {
	win, err := a.svc.Maintenance(r.Context(), scope(r), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, maintenanceOut(win, a.svc.Now()))
	return nil
}

func (a *API) putMaintenance(w http.ResponseWriter, r *http.Request) error {
	var in MaintenanceIn
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	win, err := in.toDomain()
	if err != nil {
		return err
	}
	updated, err := a.svc.UpdateMaintenance(r.Context(), scope(r), r.PathValue("id"), win)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, maintenanceOut(updated, a.svc.Now()))
	return nil
}

func (a *API) deleteMaintenance(w http.ResponseWriter, r *http.Request) error {
	if err := a.svc.DeleteMaintenance(r.Context(), scope(r), r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (a *API) endMaintenance(w http.ResponseWriter, r *http.Request) error {
	win, err := a.svc.EndMaintenance(r.Context(), scope(r), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, maintenanceOut(win, a.svc.Now()))
	return nil
}

// --- apply and export -------------------------------------------------------

// applyProject takes the apply file as YAML or JSON and answers the diff.
func (a *API) applyProject(w http.ResponseWriter, r *http.Request) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		return err
	}
	f, err := apply.Parse(body, true)
	if err != nil {
		return (&domain.ValidationError{Errors: []domain.FieldError{{Field: "file", Msg: err.Error()}}}).OrNil()
	}
	q := r.URL.Query()
	opts := service.ApplyOptions{DryRun: isOn(q.Get("dry_run")), Prune: isOn(q.Get("prune"))}
	diff, err := a.svc.Apply(r.Context(), scope(r), f, opts)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, diff)
	return nil
}

func isOn(v string) bool { return v == "1" || v == "true" || v == "yes" }

// exportProject writes the project as the apply YAML.
func (a *API) exportProject(w http.ResponseWriter, r *http.Request) error {
	f, err := a.svc.Export(r.Context(), scope(r), isOn(r.URL.Query().Get("secrets")))
	if err != nil {
		return err
	}
	out, err := apply.Encode(f)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, err = w.Write(out) //nolint:gosec // YAML vink generated, served as application/yaml, never as HTML
	return err
}

// applyOrg applies an org file: every project it names, in one transaction.
func (a *API) applyOrg(w http.ResponseWriter, r *http.Request) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
	if err != nil {
		return err
	}
	f, err := apply.ParseOrg(body, true)
	if err != nil {
		return (&domain.ValidationError{Errors: []domain.FieldError{{Field: "file", Msg: err.Error()}}}).OrNil()
	}
	q := r.URL.Query()
	diff, err := a.svc.ApplyOrg(r.Context(), scope(r), f, service.ApplyOptions{DryRun: isOn(q.Get("dry_run")), Prune: isOn(q.Get("prune"))})
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, diff)
	return nil
}

// exportOrg writes every project of the org as one apply YAML.
func (a *API) exportOrg(w http.ResponseWriter, r *http.Request) error {
	f, err := a.svc.ExportOrg(r.Context(), scope(r), isOn(r.URL.Query().Get("secrets")))
	if err != nil {
		return err
	}
	out, err := apply.EncodeOrg(f)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, err = w.Write(out) //nolint:gosec // YAML vink generated, served as application/yaml, never as HTML
	return err
}

// --- status pages ---------------------------------------------------------

func (a *API) listStatusPages(w http.ResponseWriter, r *http.Request) error {
	list, err := a.svc.ListStatusPages(r.Context(), scope(r))
	if err != nil {
		return err
	}
	out := make([]StatusPageOut, 0, len(list))
	for _, p := range list {
		out = append(out, statusPageOut(a.svc, p))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
	return nil
}

func (a *API) createStatusPage(w http.ResponseWriter, r *http.Request) error {
	var in StatusPageIn
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	p, err := a.svc.CreateStatusPage(r.Context(), scope(r), in.toDomain(), in.Password)
	if err != nil {
		return err
	}
	w.Header().Set("Location", locationFor(r, "/status-pages/"+p.Slug))
	writeJSON(w, http.StatusCreated, statusPageOut(a.svc, p))
	return nil
}

func (a *API) getStatusPage(w http.ResponseWriter, r *http.Request) error {
	p, err := a.svc.StatusPage(r.Context(), scope(r), r.PathValue("slug"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, statusPageOut(a.svc, p))
	return nil
}

func (a *API) putStatusPage(w http.ResponseWriter, r *http.Request) error {
	var in StatusPageIn
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	p, err := a.svc.UpdateStatusPage(r.Context(), scope(r), r.PathValue("slug"), in.toDomain(), in.Password)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, statusPageOut(a.svc, p))
	return nil
}

func (a *API) deleteStatusPage(w http.ResponseWriter, r *http.Request) error {
	if err := a.svc.DeleteStatusPage(r.Context(), scope(r), r.PathValue("slug")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// --- status pages (org level) -----------------------------------------------

// orgPageIn turns an org page's input into the domain, its project slugs
// into ids.
func (a *API) orgPageIn(r *http.Request, in StatusPageIn) (*domain.StatusPage, error) {
	p := in.toDomain()
	p.Projects = nil
	if len(in.Projects) == 0 {
		return p, nil
	}
	projects, err := a.svc.ListProjects(r.Context(), scope(r))
	if err != nil {
		return nil, err
	}
	ids := make(map[string]string, len(projects))
	for _, pr := range projects {
		ids[pr.Slug] = pr.ID
	}
	ve := &domain.ValidationError{}
	for _, slug := range in.Projects {
		if id, ok := ids[slug]; ok {
			p.Projects = append(p.Projects, id)
		} else {
			ve.Addf("projects", "no project %s in this org", slug)
		}
	}
	return p, ve.OrNil()
}

// orgPageOut shows an org page with its projects by slug.
func (a *API) orgPageOut(r *http.Request, p *domain.StatusPage) (OrgStatusPageOut, error) {
	projects, err := a.svc.ListProjects(r.Context(), scope(r))
	if err != nil {
		return OrgStatusPageOut{}, err
	}
	slugs := make(map[string]string, len(projects))
	for _, pr := range projects {
		slugs[pr.ID] = pr.Slug
	}
	out := OrgStatusPageOut{StatusPageOut: statusPageOut(a.svc, p), GroupBy: p.GroupBy, Projects: []string{}}
	for _, id := range p.Projects {
		if slug, ok := slugs[id]; ok {
			out.Projects = append(out.Projects, slug)
		}
	}
	return out, nil
}

func (a *API) listOrgStatusPages(w http.ResponseWriter, r *http.Request) error {
	list, err := a.svc.ListOrgStatusPages(r.Context(), scope(r))
	if err != nil {
		return err
	}
	out := make([]OrgStatusPageOut, 0, len(list))
	for _, p := range list {
		o, err := a.orgPageOut(r, p)
		if err != nil {
			return err
		}
		out = append(out, o)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
	return nil
}

func (a *API) createOrgStatusPage(w http.ResponseWriter, r *http.Request) error {
	var in StatusPageIn
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	want, err := a.orgPageIn(r, in)
	if err != nil {
		return err
	}
	p, err := a.svc.CreateOrgStatusPage(r.Context(), scope(r), want, in.Password)
	if err != nil {
		return err
	}
	out, err := a.orgPageOut(r, p)
	if err != nil {
		return err
	}
	w.Header().Set("Location", locationFor(r, "/status-pages/"+p.Slug))
	writeJSON(w, http.StatusCreated, out)
	return nil
}

func (a *API) getOrgStatusPage(w http.ResponseWriter, r *http.Request) error {
	p, err := a.svc.OrgStatusPage(r.Context(), scope(r), r.PathValue("slug"))
	if err != nil {
		return err
	}
	out, err := a.orgPageOut(r, p)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (a *API) putOrgStatusPage(w http.ResponseWriter, r *http.Request) error {
	var in StatusPageIn
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	want, err := a.orgPageIn(r, in)
	if err != nil {
		return err
	}
	p, err := a.svc.UpdateOrgStatusPage(r.Context(), scope(r), r.PathValue("slug"), want, in.Password)
	if err != nil {
		return err
	}
	out, err := a.orgPageOut(r, p)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (a *API) deleteOrgStatusPage(w http.ResponseWriter, r *http.Request) error {
	if err := a.svc.DeleteOrgStatusPage(r.Context(), scope(r), r.PathValue("slug")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// --- channels and routes (org level) ----------------------------------------

func (a *API) listOrgChannels(w http.ResponseWriter, r *http.Request) error {
	channels, err := a.svc.ListOrgChannels(r.Context(), scope(r))
	if err != nil {
		return err
	}
	items := make([]ChannelOut, 0, len(channels))
	for _, c := range channels {
		items = append(items, channelOut(c))
	}
	writeJSON(w, http.StatusOK, page[ChannelOut]{Items: items})
	return nil
}

func (a *API) createOrgChannel(w http.ResponseWriter, r *http.Request) error {
	var in ChannelIn
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	c, err := a.svc.CreateOrgChannel(r.Context(), scope(r), in.toDomain())
	if err != nil {
		return err
	}
	w.Header().Set("Location", locationFor(r, "/channels/"+c.ID))
	writeJSON(w, http.StatusCreated, channelOut(c))
	return nil
}

func (a *API) getOrgChannel(w http.ResponseWriter, r *http.Request) error {
	c, err := a.svc.OrgChannel(r.Context(), scope(r), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, channelOut(c))
	return nil
}

func (a *API) putOrgChannel(w http.ResponseWriter, r *http.Request) error {
	var in ChannelIn
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	c, err := a.svc.UpdateOrgChannel(r.Context(), scope(r), r.PathValue("id"), in.toDomain())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, channelOut(c))
	return nil
}

func (a *API) deleteOrgChannel(w http.ResponseWriter, r *http.Request) error {
	if err := a.svc.DeleteOrgChannel(r.Context(), scope(r), r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// orgProjects maps the org's project slugs to ids and back.
func (a *API) orgProjects(r *http.Request) (ids, slugs map[string]string, err error) {
	projects, err := a.svc.ListProjects(r.Context(), scope(r))
	if err != nil {
		return nil, nil, err
	}
	ids, slugs = make(map[string]string, len(projects)), make(map[string]string, len(projects))
	for _, p := range projects {
		ids[p.Slug], slugs[p.ID] = p.ID, p.Slug
	}
	return ids, slugs, nil
}

// orgRouteIn turns an org route's input into the domain, its project slugs
// into ids.
func (a *API) orgRouteIn(r *http.Request, in OrgRouteIn) (*domain.Route, error) {
	rt := in.toDomain()
	if len(in.Projects) == 0 {
		return rt, nil
	}
	ids, _, err := a.orgProjects(r)
	if err != nil {
		return nil, err
	}
	ve := &domain.ValidationError{}
	for _, slug := range in.Projects {
		if id, ok := ids[slug]; ok {
			rt.Projects = append(rt.Projects, id)
		} else {
			ve.Addf("projects", "no project %s in this org", slug)
		}
	}
	return rt, ve.OrNil()
}

// orgRouteOut shows an org route with its projects by slug.
func orgRouteOut(rt *domain.Route, slugs map[string]string) OrgRouteOut {
	out := OrgRouteOut{RouteOut: routeOut(rt), Projects: []string{}}
	for _, id := range rt.Projects {
		if slug, ok := slugs[id]; ok {
			out.Projects = append(out.Projects, slug)
		}
	}
	return out
}

func (a *API) writeOrgRoute(w http.ResponseWriter, r *http.Request, status int, rt *domain.Route) error {
	_, slugs, err := a.orgProjects(r)
	if err != nil {
		return err
	}
	writeJSON(w, status, orgRouteOut(rt, slugs))
	return nil
}

func (a *API) listOrgRoutes(w http.ResponseWriter, r *http.Request) error {
	routes, err := a.svc.ListOrgRoutes(r.Context(), scope(r))
	if err != nil {
		return err
	}
	_, slugs, err := a.orgProjects(r)
	if err != nil {
		return err
	}
	items := make([]OrgRouteOut, 0, len(routes))
	for _, rt := range routes {
		items = append(items, orgRouteOut(rt, slugs))
	}
	writeJSON(w, http.StatusOK, page[OrgRouteOut]{Items: items})
	return nil
}

func (a *API) createOrgRoute(w http.ResponseWriter, r *http.Request) error {
	var in OrgRouteIn
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	want, err := a.orgRouteIn(r, in)
	if err != nil {
		return err
	}
	rt, err := a.svc.CreateOrgRoute(r.Context(), scope(r), want)
	if err != nil {
		return err
	}
	w.Header().Set("Location", locationFor(r, "/routes/"+rt.ID))
	return a.writeOrgRoute(w, r, http.StatusCreated, rt)
}

func (a *API) getOrgRoute(w http.ResponseWriter, r *http.Request) error {
	rt, err := a.svc.OrgRoute(r.Context(), scope(r), r.PathValue("id"))
	if err != nil {
		return err
	}
	return a.writeOrgRoute(w, r, http.StatusOK, rt)
}

func (a *API) putOrgRoute(w http.ResponseWriter, r *http.Request) error {
	var in OrgRouteIn
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	want, err := a.orgRouteIn(r, in)
	if err != nil {
		return err
	}
	rt, err := a.svc.UpdateOrgRoute(r.Context(), scope(r), r.PathValue("id"), want)
	if err != nil {
		return err
	}
	return a.writeOrgRoute(w, r, http.StatusOK, rt)
}

func (a *API) deleteOrgRoute(w http.ResponseWriter, r *http.Request) error {
	if err := a.svc.DeleteOrgRoute(r.Context(), scope(r), r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// --- agents (org level) -----------------------------------------------------

func (a *API) listAgents(w http.ResponseWriter, r *http.Request) error {
	list, err := a.svc.ListAgents(r.Context(), scope(r))
	if err != nil {
		return err
	}
	out := make([]AgentOut, 0, len(list))
	for _, ag := range list {
		out = append(out, agentOut(a.svc, ag))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
	return nil
}

func (a *API) createAgent(w http.ResponseWriter, r *http.Request) error {
	var in AgentIn
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	ag, token, err := a.svc.CreateAgent(r.Context(), scope(r), in.Name, in.Labels)
	if err != nil {
		return err
	}
	w.Header().Set("Location", locationFor(r, "/agents/"+ag.Name))
	writeJSON(w, http.StatusCreated, AgentCreated{AgentOut: agentOut(a.svc, ag), Token: token, Command: domain.AgentCommand(a.svc.Config().BaseURL, token, ag.Labels)})
	return nil
}

func (a *API) getAgent(w http.ResponseWriter, r *http.Request) error {
	ag, err := a.svc.Agent(r.Context(), scope(r), r.PathValue("name"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, agentOut(a.svc, ag))
	return nil
}

func (a *API) putAgentLabels(w http.ResponseWriter, r *http.Request) error {
	var in AgentIn
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	ag, err := a.svc.UpdateAgentLabels(r.Context(), scope(r), r.PathValue("name"), in.Labels)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, agentOut(a.svc, ag))
	return nil
}

func (a *API) deleteAgent(w http.ResponseWriter, r *http.Request) error {
	if err := a.svc.RevokeAgent(r.Context(), scope(r), r.PathValue("name")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
