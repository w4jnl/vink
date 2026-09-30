package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/http/web/ui"
	"github.com/w4jnl/vink/internal/http/web/view"
	"github.com/w4jnl/vink/internal/service"
)

// noteData is a Notice under the thing an action was about.
type noteData struct {
	Tone, Title, Text string
	HTML              ui.HTML
}

type channelRow struct {
	ID, Name, Sub string
	Enabled       bool
	Cells         []ui.Cell
	Actions       ui.HTML
	Note          *noteData
}

type routeRow struct {
	ID, Lead, Sub string
	TitleHTML     ui.HTML
	Cells         []ui.Cell
	Actions       ui.HTML
}

type keyRow struct {
	Name, Sub string
	Cells     []ui.Cell
	Actions   ui.HTML
}

// panelData is the inline add or edit form of the channels and routes tabs.
type panelData struct {
	Title, Action, KindPath, CancelPath, DeletePath, CSRF, SubmitLabel, EditID, Error string
	Kind                                                                              string
	Kinds                                                                             []ui.Option
	SMTPFrom                                                                          string
	Values                                                                            map[string]string
	Errors                                                                            map[string]string
	Note                                                                              *noteData
	ChannelChecks                                                                     []ui.CheckboxProps
	OnChecks                                                                          []ui.CheckboxProps
}

type keyForm struct{ Values, Errors map[string]string }

type settingsData struct {
	base
	Tab, TabPath     string
	Tabs             []ui.Tab
	Lede, ComingSoon string
	Channels         []channelRow
	Routes           []routeRow
	Panel            *panelData
	Keys             []keyRow
	KeysPath         string
	RotatePath       string
	PingKey          string
	CanSeePingKey    bool
	CanAdmin         bool
	NewKey           string
	Flash, FlashTone string
	Form             keyForm
	AccessOptions    []ui.Option
}

// settingsOpts carries what a request adds to a tab: an open panel, the
// notes under rows, a freshly created key.
type settingsOpts struct {
	panel   *panelData
	notes   map[string]*noteData
	newKey  string
	keyForm keyForm
}

var settingsTabs = []ui.Tab{{ID: "channels", Label: "Channels"}, {ID: "routes", Label: "Routes"}, {ID: "maintenance", Label: "Maintenance"}, {ID: "pages", Label: "Status pages"}, {ID: "keys", Label: "Keys"}}

var channelKinds = ui.Opts("webhook", "webhook", "ntfy", "ntfy", "smtp", "smtp")

// channelFields are the form names a channel panel can post, all kinds together.
var channelFields = []string{"name", "url", "topic", "token", "priority", "method", "headers", "body_template", "to", "from"}

func (h *Web) settingsRedirect(c *reqCtx) error {
	http.Redirect(c.w, c.r, c.projectPath()+"/settings/channels", http.StatusSeeOther)
	return nil
}

func knownTab(name string) bool {
	for _, t := range settingsTabs {
		if t.ID == name {
			return true
		}
	}
	return false
}

func csrfInput(c *reqCtx) string {
	return `<input type="hidden" name="_csrf"` + ui.Attr("value", c.csrf()) + `>`
}

// postForm wraps a button in a form that posts to path; htmx swaps the
// tab in place, a plain browser follows the redirect.
func postForm(c *reqCtx, path string, hx bool, button ui.HTML) ui.HTML {
	extra := ""
	if hx {
		extra = ui.Attr("hx-post", path) + ui.Attr("hx-target", "#tab")
	}
	return ui.HTML(`<form method="post"` + ui.Attr("action", path) + extra + `>` + csrfInput(c) + string(button) + `</form>`)
}

func plural(n int, word string) string {
	switch n {
	case 0:
		return "no " + word + "s"
	case 1:
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}

// dayShort renders "today" or "12 Aug".
func dayShort(t, now time.Time, loc *time.Location) string {
	lt, ln := t.In(loc), now.In(loc)
	if lt.Year() == ln.Year() && lt.YearDay() == ln.YearDay() {
		return "today"
	}
	return lt.Format("2 Jan")
}

func (h *Web) settingsData(c *reqCtx, tabName string, o settingsOpts) (settingsData, error) {
	ctx := c.r.Context()
	root := c.projectPath() + "/settings/"
	d := settingsData{
		base: h.baseFor(c, "Settings", "settings"), Tab: tabName, TabPath: root + tabName, Panel: o.panel,
		KeysPath: root + "keys", RotatePath: root + "ping-key/rotate", CanSeePingKey: c.scope.CanSeePingKey(), CanAdmin: c.scope.CanAdminProject(),
		NewKey: o.newKey, Form: o.keyForm, Flash: c.r.URL.Query().Get("flash"), FlashTone: "ok", AccessOptions: ui.Opts("ro", "ro", "rw", "rw"),
	}
	if d.Form.Values == nil {
		d.Form = keyForm{Values: map[string]string{}, Errors: map[string]string{}}
	}
	channels, err := h.svc.ListChannels(ctx, c.scope)
	if err != nil {
		return d, err
	}
	routes, err := h.svc.ListRoutes(ctx, c.scope)
	if err != nil {
		return d, err
	}
	var keys []*domain.APIKey
	if d.CanSeePingKey {
		if keys, err = h.svc.ListAPIKeys(ctx, c.scope); err != nil {
			return d, err
		}
	}
	counts := map[string]int{"channels": len(channels), "routes": len(routes), "maintenance": 0, "pages": 0}
	for _, t := range settingsTabs {
		tab := ui.Tab{ID: t.ID, Label: t.Label, Href: root + t.ID}
		if n, ok := counts[t.ID]; ok {
			tab.Count = ui.Count(n)
		}
		d.Tabs = append(d.Tabs, tab)
	}
	loc, err := time.LoadLocation(c.project.Timezone)
	if err != nil {
		loc = time.UTC
	}
	switch tabName {
	case "channels":
		routeCount := map[string]int{}
		for _, r := range routes {
			for _, rc := range r.Channels {
				routeCount[rc.ID]++
			}
		}
		for _, ch := range channels {
			row := channelRow{ID: ch.ID, Name: ch.Name, Sub: string(ch.Kind) + " · " + channelSummary(ch), Enabled: ch.Enabled, Note: o.notes[ch.ID]}
			sw := ui.Switch(ch.Enabled, ch.Name+" enabled", ui.Attr("hx-post", root+"channels/"+ch.ID+"/toggle")+ui.Attr("hx-target", "#tab"))
			sent := "never sent"
			if last, err := h.svc.LastSentForChannel(ctx, c.scope, ch.ID); err == nil && last != nil {
				sent = "sent " + view.Ago(*last, c.now)
			}
			row.Cells = []ui.Cell{{HTML: sw, Size: "s"}, {Text: plural(routeCount[ch.ID], "route")}, {Text: sent, Mono: true}}
			row.Actions = postForm(c, root+"channels/"+ch.ID+"/test", true, ui.Button(ui.ButtonProps{Label: "Test", Type: "submit"})) +
				ui.Button(ui.ButtonProps{Label: "Edit", Href: d.TabPath + "?edit=" + url.QueryEscape(ch.ID)})
			d.Channels = append(d.Channels, row)
		}
	case "routes":
		for i, r := range routes {
			row := routeRow{ID: r.ID, Lead: strconv.Itoa(i + 1), Sub: "→ " + strings.Join(r.ChannelNames(), ", ")}
			if len(r.MatchTags) == 0 {
				row.TitleHTML = "Every monitor"
			} else {
				var tags strings.Builder
				for _, t := range r.MatchTags {
					tags.WriteString(string(ui.Tag(t)))
				}
				row.TitleHTML = ui.HTML(tags.String())
			}
			var states strings.Builder
			for _, s := range r.On {
				states.WriteString(string(ui.StateBadge(ui.StateBadgeProps{State: string(s)})))
			}
			repeat := "no repeat"
			if r.RepeatEvery > 0 {
				repeat = "repeat every " + view.Span(r.RepeatEvery)
			}
			row.Cells = []ui.Cell{{HTML: ui.HTML(states.String()), Size: "l"}, {Text: repeat, Mono: true}}
			row.Actions = ui.Button(ui.ButtonProps{Label: "Edit", Href: d.TabPath + "?edit=" + url.QueryEscape(r.ID)})
			d.Routes = append(d.Routes, row)
		}
	case "keys":
		if d.CanSeePingKey {
			d.PingKey = c.project.PingKey
		}
		names := map[string]string{}
		for _, k := range keys {
			if _, ok := names[k.CreatedBy]; !ok {
				names[k.CreatedBy] = actorName(k.CreatedBy)
				if u, err := h.svc.UserByID(ctx, k.CreatedBy); err == nil {
					names[k.CreatedBy] = u.Subject
				}
			}
			used := "never used"
			if k.LastUsedAt != nil {
				used = "used " + view.Ago(*k.LastUsedAt, c.now)
			}
			row := keyRow{Name: k.Name, Sub: "vk_" + k.Prefix + "…", Cells: []ui.Cell{
				{HTML: ui.Tag(string(k.Access)), Size: "s"}, {Text: names[k.CreatedBy] + " · " + dayShort(k.CreatedAt, c.now, loc)}, {Text: used, Mono: true},
			}}
			if k.Access == domain.AccessRO || d.CanAdmin {
				row.Actions = postForm(c, root+"keys/"+k.ID+"/revoke", false, ui.Button(ui.ButtonProps{Label: "Revoke", Variant: "danger", Confirm: "Really revoke?", Type: "submit"}))
			}
			d.Keys = append(d.Keys, row)
		}
	case "maintenance":
		d.Lede = "A maintenance window silences alerts for the monitors that carry its tags."
		d.ComingSoon = "Maintenance windows arrive later in phase 1."
	case "pages":
		d.Lede = "A status page shows a set of monitors to people without an account."
		d.ComingSoon = "Status pages arrive later in phase 1."
	}
	return d, nil
}

// channelSummary is the row's target: the URL, topic or recipients.
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

func (h *Web) renderTab(c *reqCtx, status int, tab string, o settingsOpts) error {
	d, err := h.settingsData(c, tab, o)
	if err != nil {
		return err
	}
	if c.htmx() {
		return h.render(c, status, "settings", "settings-tab", d)
	}
	return h.render(c, status, "settings", "layout", d)
}

func (h *Web) settings(c *reqCtx) error {
	tabName := c.r.PathValue("tab")
	if !knownTab(tabName) {
		return domain.NotFound("settings tab")
	}
	ctx := c.r.Context()
	q := c.r.URL.Query()
	var o settingsOpts
	var err error
	switch {
	case tabName == "channels" && q.Has("add"):
		if !c.scope.CanEdit() {
			return domain.ErrForbidden
		}
		o.panel = h.channelPanel(c, nil, q.Get("kind"))
		for _, k := range channelFields {
			if q.Has(k) {
				o.panel.Values[k] = strings.TrimSpace(q.Get(k))
			}
		}
	case tabName == "channels" && q.Get("edit") != "":
		ch, err := h.svc.Channel(ctx, c.scope, q.Get("edit"))
		if err != nil {
			return err
		}
		o.panel = h.channelPanel(c, ch, "")
	case tabName == "routes" && q.Has("add"):
		if !c.scope.CanEdit() {
			return domain.ErrForbidden
		}
		if o.panel, err = h.routePanel(c, nil); err != nil {
			return err
		}
	case tabName == "routes" && q.Get("edit") != "":
		rt, err := h.svc.Route(ctx, c.scope, q.Get("edit"))
		if err != nil {
			return err
		}
		if o.panel, err = h.routePanel(c, rt); err != nil {
			return err
		}
	}
	return h.renderTab(c, http.StatusOK, tabName, o)
}

func (h *Web) settingsFlash(c *reqCtx, tab, flash string) error {
	return h.redirect(c, c.projectPath()+"/settings/"+tab+"?flash="+url.QueryEscape(flash))
}

// applyPanelValidation maps service field errors onto the panel. A
// notifier's config error names its field first ("url must be ...").
func applyPanelValidation(p *panelData, err error) bool {
	ve, ok := domain.AsValidation(err)
	if !ok {
		return false
	}
	for _, fe := range ve.Errors {
		field, msg := fe.Field, fe.Msg
		if field == "config" {
			field = ""
			for _, k := range channelFields {
				if strings.HasPrefix(msg, k+" ") {
					field = k
					break
				}
			}
		}
		if field == "" {
			p.Error = capitalise(msg) + "."
			continue
		}
		if _, exists := p.Errors[field]; !exists {
			p.Errors[field] = capitalise(msg) + "."
		}
	}
	return true
}

// --- channels -------------------------------------------------------------

func (h *Web) channelPanel(c *reqCtx, ch *domain.Channel, kind string) *panelData {
	root := c.projectPath() + "/settings/channels"
	p := &panelData{
		Title: "New channel", Action: root, KindPath: root + "?add=1", CancelPath: root, CSRF: c.csrf(), SubmitLabel: "Add channel",
		Kind: kind, Kinds: channelKinds, SMTPFrom: h.smtpFrom, Values: map[string]string{"method": "POST"}, Errors: map[string]string{},
	}
	if ch != nil {
		p.Title, p.EditID, p.Action, p.SubmitLabel = "Edit "+ch.Name, ch.ID, root+"/"+ch.ID, "Save"
		p.DeletePath, p.KindPath, p.Kind = root+"/"+ch.ID+"/delete", root+"?edit="+url.QueryEscape(ch.ID), string(ch.Kind)
		p.Values["name"] = ch.Name
		fillChannelValues(p.Values, ch)
	}
	if !domain.ChannelKind(p.Kind).Valid() {
		p.Kind = "webhook"
	}
	return p
}

// fillChannelValues shows a stored config in the form; secrets show as ***.
func fillChannelValues(v map[string]string, ch *domain.Channel) {
	var m map[string]any
	_ = json.Unmarshal(ch.Config, &m)
	str := func(k string) string {
		s, _ := m[k].(string)
		return s
	}
	switch ch.Kind {
	case domain.ChannelNtfy:
		v["url"], v["topic"] = str("url"), str("topic")
		if str("token") != "" {
			v["token"] = "***"
		}
		if n, ok := m["priority"].(float64); ok && n > 0 {
			v["priority"] = strconv.Itoa(int(n))
		}
	case domain.ChannelWebhook:
		v["url"], v["body_template"] = str("url"), str("body_template")
		if s := str("method"); s != "" {
			v["method"] = s
		}
		if hs, ok := m["headers"].(map[string]any); ok && len(hs) > 0 {
			v["headers"] = "***"
		}
	case domain.ChannelSMTP:
		v["from"] = str("from")
		if to, ok := m["to"].([]any); ok {
			parts := make([]string, 0, len(to))
			for _, a := range to {
				if s, ok := a.(string); ok {
					parts = append(parts, s)
				}
			}
			v["to"] = strings.Join(parts, ", ")
		}
	}
}

// channelConfig turns the kind's form fields into the notifier's JSON.
func channelConfig(kind string, v map[string]string, errs map[string]string) json.RawMessage {
	cfg := map[string]any{}
	switch kind {
	case "ntfy":
		cfg["url"], cfg["topic"] = v["url"], v["topic"]
		if v["token"] != "" {
			cfg["token"] = v["token"]
		}
		if v["priority"] != "" {
			if n, err := strconv.Atoi(v["priority"]); err != nil {
				errs["priority"] = "Pick a priority."
			} else {
				cfg["priority"] = n
			}
		}
	case "webhook":
		cfg["url"] = v["url"]
		if v["method"] != "" && v["method"] != "POST" {
			cfg["method"] = v["method"]
		}
		switch hs := v["headers"]; {
		case hs == "***":
			cfg["headers"] = "***"
		case hs != "":
			m := map[string]string{}
			for _, line := range strings.Split(hs, "\n") {
				if line = strings.TrimSpace(line); line == "" {
					continue
				}
				name, val, ok := strings.Cut(line, ":")
				if !ok || strings.TrimSpace(name) == "" {
					errs["headers"] = "One Name: value per line."
					break
				}
				m[strings.TrimSpace(name)] = strings.TrimSpace(val)
			}
			if len(m) > 0 {
				cfg["headers"] = m
			}
		}
		if v["body_template"] != "" {
			cfg["body_template"] = v["body_template"]
		}
	case "smtp":
		to := []string{}
		for _, a := range strings.Split(v["to"], ",") {
			if a = strings.TrimSpace(a); a != "" {
				to = append(to, a)
			}
		}
		cfg["to"] = to
		if v["from"] != "" {
			cfg["from"] = v["from"]
		}
	}
	out, _ := json.Marshal(cfg)
	return out
}

func testNote(err error) *noteData {
	if err == nil {
		return &noteData{Tone: "ok", Title: "Test sent.", Text: "The channel accepted it."}
	}
	return &noteData{Tone: "error", Title: "Test failed.", Text: "The channel did not accept it:", HTML: ui.HTML("<code>" + esc(err.Error()) + "</code>")}
}

func esc(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}

// saveChannel creates (no id) or updates a channel; action=test sends a
// test through the unsaved form instead.
func (h *Web) saveChannel(c *reqCtx) error {
	if err := c.r.ParseForm(); err != nil {
		return err
	}
	ctx := c.r.Context()
	id := c.r.PathValue("id")
	var cur *domain.Channel
	if id != "" {
		var err error
		if cur, err = h.svc.Channel(ctx, c.scope, id); err != nil {
			return err
		}
	}
	p := h.channelPanel(c, cur, strings.TrimSpace(c.r.PostFormValue("kind")))
	for _, k := range channelFields {
		if c.r.PostForm.Has(k) {
			p.Values[k] = strings.TrimSpace(c.r.PostFormValue(k))
		}
	}
	cfg := channelConfig(p.Kind, p.Values, p.Errors)
	ch := &domain.Channel{Name: p.Values["name"], Kind: domain.ChannelKind(p.Kind), Config: cfg, Enabled: true}
	if cur != nil {
		ch.Enabled = cur.Enabled
	}
	opts := settingsOpts{panel: p}
	if c.r.PostFormValue("action") == "test" {
		testCfg := cfg
		if cur != nil {
			if merged, err := service.KeepSecrets(cur.Kind, cfg, cur.Config); err == nil {
				testCfg = merged
			}
		}
		err := h.svc.TestChannelConfig(ctx, c.scope, ch.Kind, testCfg)
		if applyPanelValidation(p, err) {
			return h.renderTab(c, http.StatusUnprocessableEntity, "channels", opts)
		}
		if errors.Is(err, domain.ErrForbidden) {
			return err
		}
		p.Note = testNote(err)
		return h.renderTab(c, http.StatusOK, "channels", opts)
	}
	if len(p.Errors) > 0 {
		return h.renderTab(c, http.StatusUnprocessableEntity, "channels", opts)
	}
	var err error
	if cur == nil {
		_, err = h.svc.CreateChannel(ctx, c.scope, ch)
	} else {
		_, err = h.svc.UpdateChannel(ctx, c.scope, id, ch)
	}
	if err != nil {
		if applyPanelValidation(p, err) {
			return h.renderTab(c, http.StatusUnprocessableEntity, "channels", opts)
		}
		if errors.Is(err, domain.ErrConflict) {
			p.Errors["name"] = "A channel with this name exists."
			return h.renderTab(c, http.StatusUnprocessableEntity, "channels", opts)
		}
		return err
	}
	return h.redirect(c, c.projectPath()+"/settings/channels")
}

func (h *Web) deleteChannel(c *reqCtx) error {
	if err := h.svc.DeleteChannel(c.r.Context(), c.scope, c.r.PathValue("id")); err != nil {
		return err
	}
	return h.redirect(c, c.projectPath()+"/settings/channels")
}

// testChannel sends a test through a saved channel and shows the result
// under its row.
func (h *Web) testChannel(c *reqCtx) error {
	id := c.r.PathValue("id")
	err := h.svc.TestChannel(c.r.Context(), c.scope, id)
	if errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrForbidden) {
		return err
	}
	return h.renderTab(c, http.StatusOK, "channels", settingsOpts{notes: map[string]*noteData{id: testNote(err)}})
}

func (h *Web) toggleChannel(c *reqCtx) error {
	ctx := c.r.Context()
	ch, err := h.svc.Channel(ctx, c.scope, c.r.PathValue("id"))
	if err != nil {
		return err
	}
	upd := *ch
	upd.Enabled = !ch.Enabled
	if _, err := h.svc.UpdateChannel(ctx, c.scope, ch.ID, &upd); err != nil {
		return err
	}
	if c.htmx() {
		return h.renderTab(c, http.StatusOK, "channels", settingsOpts{})
	}
	return h.redirect(c, c.projectPath()+"/settings/channels")
}

// --- routes ---------------------------------------------------------------

func (h *Web) routePanel(c *reqCtx, rt *domain.Route) (*panelData, error) {
	root := c.projectPath() + "/settings/routes"
	p := &panelData{Title: "New route", Action: root, CancelPath: root, CSRF: c.csrf(), SubmitLabel: "Save route", Values: map[string]string{}, Errors: map[string]string{}}
	channels, err := h.svc.ListChannels(c.r.Context(), c.scope)
	if err != nil {
		return nil, err
	}
	selected := map[string]bool{}
	on := map[string]bool{"down": true, "up": true}
	if rt != nil {
		p.Title, p.EditID, p.Action, p.DeletePath = "Edit route", rt.ID, root+"/"+rt.ID, root+"/"+rt.ID+"/delete"
		p.Values["match_tags"] = strings.Join(rt.MatchTags, ", ")
		if rt.RepeatEvery > 0 {
			p.Values["repeat_every"] = domain.Duration(rt.RepeatEvery).String()
		}
		on = map[string]bool{}
		for _, s := range rt.On {
			on[string(s)] = true
		}
		for _, ch := range rt.Channels {
			selected[ch.ID] = true
		}
	}
	for _, ch := range channels {
		p.ChannelChecks = append(p.ChannelChecks, ui.CheckboxProps{Label: ch.Name, Name: "channels", Value: ch.ID, ID: "ch-" + ch.ID, Checked: selected[ch.ID], Attrs: ui.Attr("form", "route-form")})
	}
	for _, s := range []string{"down", "up", "late"} {
		p.OnChecks = append(p.OnChecks, ui.CheckboxProps{Label: s, Name: "on", Value: s, ID: "on-" + s, Checked: on[s], Attrs: ui.Attr("form", "route-form")})
	}
	return p, nil
}

func (h *Web) saveRoute(c *reqCtx) error {
	if err := c.r.ParseForm(); err != nil {
		return err
	}
	ctx := c.r.Context()
	id := c.r.PathValue("id")
	var cur *domain.Route
	if id != "" {
		var err error
		if cur, err = h.svc.Route(ctx, c.scope, id); err != nil {
			return err
		}
	}
	p, err := h.routePanel(c, cur)
	if err != nil {
		return err
	}
	for _, k := range []string{"match_tags", "repeat_every"} {
		p.Values[k] = strings.TrimSpace(c.r.PostFormValue(k))
	}
	rt := &domain.Route{ChannelIDs: c.r.PostForm["channels"]}
	if v := p.Values["match_tags"]; v != "" {
		rt.MatchTags = domain.NormalizeTags(strings.Split(v, ","))
	}
	for _, s := range c.r.PostForm["on"] {
		rt.On = append(rt.On, domain.State(s))
	}
	for i := range p.ChannelChecks {
		p.ChannelChecks[i].Checked = contains(rt.ChannelIDs, p.ChannelChecks[i].Value)
	}
	for i := range p.OnChecks {
		p.OnChecks[i].Checked = contains(c.r.PostForm["on"], p.OnChecks[i].Value)
	}
	if v := p.Values["repeat_every"]; v != "" && v != "0" {
		d, err := domain.ParseDuration(v)
		if err != nil {
			p.Errors["repeat_every"] = "Use a duration such as 4h, or 0 for never."
		}
		rt.RepeatEvery = d.Std()
	}
	if cur != nil {
		rt.Priority = cur.Priority
	}
	opts := settingsOpts{panel: p}
	if len(p.Errors) > 0 {
		return h.renderTab(c, http.StatusUnprocessableEntity, "routes", opts)
	}
	if cur == nil {
		_, err = h.svc.CreateRoute(ctx, c.scope, rt)
	} else {
		_, err = h.svc.UpdateRoute(ctx, c.scope, id, rt)
	}
	if err != nil {
		if applyPanelValidation(p, err) {
			return h.renderTab(c, http.StatusUnprocessableEntity, "routes", opts)
		}
		return err
	}
	return h.redirect(c, c.projectPath()+"/settings/routes")
}

func (h *Web) deleteRoute(c *reqCtx) error {
	if err := h.svc.DeleteRoute(c.r.Context(), c.scope, c.r.PathValue("id")); err != nil {
		return err
	}
	return h.redirect(c, c.projectPath()+"/settings/routes")
}

// --- keys -----------------------------------------------------------------

func (h *Web) createKey(c *reqCtx) error {
	if err := c.r.ParseForm(); err != nil {
		return err
	}
	f := keyForm{Values: map[string]string{"name": strings.TrimSpace(c.r.PostFormValue("name")), "access": c.r.PostFormValue("access")}, Errors: map[string]string{}}
	_, plain, err := h.svc.CreateAPIKey(c.r.Context(), c.scope, f.Values["name"], domain.Access(f.Values["access"]))
	if err != nil {
		if ve, ok := domain.AsValidation(err); ok {
			for _, fe := range ve.Errors {
				f.Errors[fe.Field] = capitalise(fe.Msg) + "."
			}
			return h.renderTab(c, http.StatusUnprocessableEntity, "keys", settingsOpts{keyForm: f})
		}
		return err
	}
	// The plaintext is shown once, in this response, never in a URL.
	return h.renderTab(c, http.StatusOK, "keys", settingsOpts{newKey: plain})
}

func (h *Web) revokeKey(c *reqCtx) error {
	if err := h.svc.RevokeAPIKey(c.r.Context(), c.scope, c.r.PathValue("id")); err != nil {
		return err
	}
	return h.settingsFlash(c, "keys", "Key revoked.")
}

func (h *Web) rotatePingKey(c *reqCtx) error {
	if _, err := h.svc.RotatePingKey(c.r.Context(), c.scope); err != nil {
		return err
	}
	return h.settingsFlash(c, "keys", "Ping key rotated. The old key works for one more day.")
}
