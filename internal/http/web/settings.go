package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/http/web/view"
)

type tab struct {
	Label, Path string
	Active      bool
}

type simpleForm struct {
	Values               map[string]string
	Errors               map[string]string
	Error                string
	OnDown, OnUp, OnLate bool
}

func newSimpleForm() simpleForm {
	return simpleForm{Values: map[string]string{}, Errors: map[string]string{}, OnDown: true, OnUp: true}
}

type channelRow struct {
	ID, Name, Kind, Summary, TestPath, DeletePath string
	Enabled                                       bool
}

type routeRow struct {
	Channel, Kind, Summary, DeletePath string
	Tags                               []string
}

type keyRow struct {
	Name, Prefix, Access, Used, RevokePath string
	CanRevoke                              bool
}

type settingsData struct {
	base
	Tab           string
	Tabs          []tab
	Flash         string
	Form          simpleForm
	Channels      []channelRow
	Kinds         []string
	ChannelsPath  string
	Routes        []routeRow
	RoutesPath    string
	Keys          []keyRow
	KeysPath      string
	NewKey        string
	PingBase      string
	PingKey       string
	CanSeePingKey bool
	CanAdmin      bool
	RotatePath    string
}

var settingsTabs = []string{"channels", "routes", "keys"}

func (h *Web) settingsRedirect(c *reqCtx) error {
	http.Redirect(c.w, c.r, c.projectPath()+"/settings/channels", http.StatusSeeOther)
	return nil
}

func (h *Web) settingsData(c *reqCtx, tabName string, form simpleForm) (settingsData, error) {
	ctx := c.r.Context()
	base := c.projectPath() + "/settings/"
	d := settingsData{
		base: h.baseFor(c, "Settings"), Tab: tabName, Form: form, Flash: c.r.URL.Query().Get("flash"),
		ChannelsPath: base + "channels", RoutesPath: base + "routes", KeysPath: base + "keys", RotatePath: base + "ping-key/rotate",
		PingBase: h.pingBase(), CanSeePingKey: c.scope.CanSeePingKey(), CanAdmin: c.scope.CanAdminProject(),
		Kinds: []string{"webhook", "ntfy", "smtp"},
	}
	for _, t := range settingsTabs {
		d.Tabs = append(d.Tabs, tab{Label: t, Path: base + t, Active: t == tabName})
	}
	switch tabName {
	case "channels", "routes":
		channels, err := h.svc.ListChannels(ctx, c.scope)
		if err != nil {
			return d, err
		}
		for _, ch := range channels {
			d.Channels = append(d.Channels, channelRow{
				ID: ch.ID, Name: ch.Name, Kind: string(ch.Kind), Enabled: ch.Enabled, Summary: channelSummary(ch),
				TestPath: base + "channels/" + ch.ID + "/test", DeletePath: base + "channels/" + ch.ID + "/delete",
			})
		}
		if tabName == "routes" {
			routes, err := h.svc.ListRoutes(ctx, c.scope)
			if err != nil {
				return d, err
			}
			for _, r := range routes {
				on := make([]string, 0, len(r.On))
				for _, s := range r.On {
					on = append(on, string(s))
				}
				summary := "on " + strings.Join(on, ", ")
				if r.RepeatEvery > 0 {
					summary += " · repeat every " + domain.Duration(r.RepeatEvery).String()
				}
				if len(r.MatchTags) == 0 {
					summary += " · all monitors"
				}
				d.Routes = append(d.Routes, routeRow{Channel: r.ChannelName, Kind: string(r.ChannelKind), Summary: summary, Tags: r.MatchTags, DeletePath: base + "routes/" + r.ID + "/delete"})
			}
		}
	case "keys":
		if d.CanSeePingKey {
			d.PingKey = c.project.PingKey
			keys, err := h.svc.ListAPIKeys(ctx, c.scope)
			if err != nil {
				return d, err
			}
			for _, k := range keys {
				used := "never used"
				if k.LastUsedAt != nil {
					used = "used " + view.Ago(*k.LastUsedAt, c.now)
				}
				d.Keys = append(d.Keys, keyRow{Name: k.Name, Prefix: k.Prefix, Access: string(k.Access), Used: used + " · created " + view.Ago(k.CreatedAt, c.now),
					RevokePath: base + "keys/" + k.ID + "/revoke", CanRevoke: k.Access == domain.AccessRO || d.CanAdmin})
			}
		}
	}
	return d, nil
}

func channelSummary(ch *domain.Channel) string {
	var m map[string]any
	_ = json.Unmarshal(ch.Config, &m)
	switch ch.Kind {
	case domain.ChannelWebhook:
		if u, ok := m["url"].(string); ok {
			return u
		}
	case domain.ChannelNtfy:
		u, _ := m["url"].(string)
		t, _ := m["topic"].(string)
		return strings.TrimRight(u, "/") + "/" + t
	case domain.ChannelSMTP:
		if to, ok := m["to"].([]any); ok {
			parts := make([]string, 0, len(to))
			for _, a := range to {
				if s, ok := a.(string); ok {
					parts = append(parts, s)
				}
			}
			return strings.Join(parts, ", ")
		}
	}
	return ""
}

func (h *Web) settings(c *reqCtx) error {
	tabName := c.r.PathValue("tab")
	known := false
	for _, t := range settingsTabs {
		known = known || t == tabName
	}
	if !known {
		return domain.NotFound("settings tab")
	}
	d, err := h.settingsData(c, tabName, newSimpleForm())
	if err != nil {
		return err
	}
	d.NewKey = c.r.URL.Query().Get("key")
	return h.render(c, http.StatusOK, "settings", "layout", d)
}

func (h *Web) renderSettings(c *reqCtx, status int, tabName string, form simpleForm) error {
	d, err := h.settingsData(c, tabName, form)
	if err != nil {
		return err
	}
	return h.render(c, status, "settings", "layout", d)
}

func (h *Web) settingsRedirectFlash(c *reqCtx, tabName, flash string) error {
	http.Redirect(c.w, c.r, c.projectPath()+"/settings/"+tabName+"?flash="+url.QueryEscape(flash), http.StatusSeeOther)
	return nil
}

func formValues(r *http.Request, f *simpleForm, keys ...string) {
	for _, k := range keys {
		f.Values[k] = strings.TrimSpace(r.PostFormValue(k))
	}
}

func applySimpleValidation(f *simpleForm, err error) bool {
	ve, ok := domain.AsValidation(err)
	if !ok {
		return false
	}
	for _, fe := range ve.Errors {
		if _, exists := f.Errors[fe.Field]; !exists {
			f.Errors[fe.Field] = capitalise(fe.Msg) + "."
		}
	}
	return true
}

// --- channels -------------------------------------------------------------

func (h *Web) createChannel(c *reqCtx) error {
	if err := c.r.ParseForm(); err != nil {
		return err
	}
	f := newSimpleForm()
	formValues(c.r, &f, "name", "kind", "config")
	cfg := f.Values["config"]
	if cfg == "" {
		cfg = "{}"
	}
	ch := &domain.Channel{Name: f.Values["name"], Kind: domain.ChannelKind(f.Values["kind"]), Config: json.RawMessage(cfg), Enabled: true}
	if _, err := h.svc.CreateChannel(c.r.Context(), c.scope, ch); err != nil {
		if applySimpleValidation(&f, err) {
			return h.renderSettings(c, http.StatusUnprocessableEntity, "channels", f)
		}
		if errors.Is(err, domain.ErrConflict) {
			f.Errors["name"] = "A channel with this name exists."
			return h.renderSettings(c, http.StatusUnprocessableEntity, "channels", f)
		}
		return err
	}
	return h.settingsRedirectFlash(c, "channels", "Channel added.")
}

func (h *Web) deleteChannel(c *reqCtx) error {
	if err := h.svc.DeleteChannel(c.r.Context(), c.scope, c.r.PathValue("id")); err != nil {
		return err
	}
	return h.settingsRedirectFlash(c, "channels", "Channel deleted.")
}

func (h *Web) testChannel(c *reqCtx) error {
	err := h.svc.TestChannel(c.r.Context(), c.scope, c.r.PathValue("id"))
	switch {
	case err == nil:
		return h.settingsRedirectFlash(c, "channels", "Test notification sent.")
	case errors.Is(err, domain.ErrNotFound), errors.Is(err, domain.ErrForbidden):
		return err
	default:
		return h.settingsRedirectFlash(c, "channels", "Test failed: "+err.Error())
	}
}

// --- routes ---------------------------------------------------------------

func (h *Web) createRoute(c *reqCtx) error {
	if err := c.r.ParseForm(); err != nil {
		return err
	}
	f := newSimpleForm()
	formValues(c.r, &f, "channel_id", "match_tags", "repeat_every")
	rt := &domain.Route{ChannelID: f.Values["channel_id"]}
	if v := f.Values["match_tags"]; v != "" {
		rt.MatchTags = domain.NormalizeTags(strings.Split(v, ","))
	}
	f.OnDown, f.OnUp, f.OnLate = false, false, false
	for _, s := range c.r.PostForm["on"] {
		rt.On = append(rt.On, domain.State(s))
		switch s {
		case "down":
			f.OnDown = true
		case "up":
			f.OnUp = true
		case "late":
			f.OnLate = true
		}
	}
	if v := f.Values["repeat_every"]; v != "" {
		d, err := domain.ParseDuration(v)
		if err != nil {
			f.Errors["repeat_every"] = "Use a duration such as 4h."
			return h.renderSettings(c, http.StatusUnprocessableEntity, "routes", f)
		}
		rt.RepeatEvery = d.Std()
	}
	if _, err := h.svc.CreateRoute(c.r.Context(), c.scope, rt); err != nil {
		if applySimpleValidation(&f, err) {
			return h.renderSettings(c, http.StatusUnprocessableEntity, "routes", f)
		}
		return err
	}
	return h.settingsRedirectFlash(c, "routes", "Route added.")
}

func (h *Web) deleteRoute(c *reqCtx) error {
	if err := h.svc.DeleteRoute(c.r.Context(), c.scope, c.r.PathValue("id")); err != nil {
		return err
	}
	return h.settingsRedirectFlash(c, "routes", "Route deleted.")
}

// --- keys -----------------------------------------------------------------

func (h *Web) createKey(c *reqCtx) error {
	if err := c.r.ParseForm(); err != nil {
		return err
	}
	f := newSimpleForm()
	formValues(c.r, &f, "name", "access")
	_, plain, err := h.svc.CreateAPIKey(c.r.Context(), c.scope, f.Values["name"], domain.Access(f.Values["access"]))
	if err != nil {
		if applySimpleValidation(&f, err) {
			return h.renderSettings(c, http.StatusUnprocessableEntity, "keys", f)
		}
		return err
	}
	http.Redirect(c.w, c.r, c.projectPath()+"/settings/keys?key="+url.QueryEscape(plain), http.StatusSeeOther)
	return nil
}

func (h *Web) revokeKey(c *reqCtx) error {
	if err := h.svc.RevokeAPIKey(c.r.Context(), c.scope, c.r.PathValue("id")); err != nil {
		return err
	}
	return h.settingsRedirectFlash(c, "keys", "Key revoked.")
}

func (h *Web) rotatePingKey(c *reqCtx) error {
	if _, err := h.svc.RotatePingKey(c.r.Context(), c.scope); err != nil {
		return err
	}
	return h.settingsRedirectFlash(c, "keys", "Ping key rotated. The old key works for one more day.")
}
