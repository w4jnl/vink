package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/w4jnl/vink/internal/auth"
	"github.com/w4jnl/vink/internal/domain"
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
		out.Key = &meKey{Prefix: strings.TrimPrefix(sc.Actor, "key:"), Access: sc.KeyAccess}
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
// request used.
func locationFor(r *http.Request, path string) string {
	if org, project := r.PathValue("org"), r.PathValue("project"); org != "" && project != "" {
		return Prefix + "/orgs/" + org + "/projects/" + project + path
	}
	return Prefix + path
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
	cursor, err := decodeCursor(r.URL.Query().Get("cursor"), 2)
	if err != nil {
		return err
	}
	p := service.ObservationPage{Since: since, Until: until, Limit: limit}
	if cursor != nil {
		ms, err := strconv.ParseInt(cursor[0], 10, 64)
		if err != nil {
			return badRequest("invalid cursor")
		}
		p.CursorAt, p.CursorID = domain.FromMillis(ms), cursor[1]
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

func (a *API) listEvents(w http.ResponseWriter, r *http.Request) error {
	limit, err := limitParam(r)
	if err != nil {
		return err
	}
	events, err := a.svc.ListEvents(r.Context(), scope(r), r.PathValue("slug"), limit)
	if err != nil {
		return err
	}
	items := make([]EventOut, 0, len(events))
	for _, e := range events {
		items = append(items, eventOut(e))
	}
	writeJSON(w, http.StatusOK, page[EventOut]{Items: items})
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
