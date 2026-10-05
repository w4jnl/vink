package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
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
	Muted     bool
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
	// Maintenance windows: repeat once or weekly, the day toggles, the zones.
	Repeat     string
	DayOptions []ui.Option
	Days       []string
	Timezones  []ui.Option
	NextHint   string
	// Status pages: which incidents; an org page's projects and grouping.
	IncidentOptions []ui.Option
	Org             bool
	ProjectChecks   []ui.CheckboxProps
}

type keyForm struct{ Values, Errors map[string]string }

type settingsData struct {
	base
	Tab, TabPath     string
	Tabs             []ui.Tab
	Lede, ComingSoon string
	Channels         []channelRow
	Routes           []routeRow
	Windows          []windowRow
	Pages            []pageRow
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

var channelKinds = ui.Opts("webhook", "webhook", "ntfy", "ntfy", "smtp", "smtp", "gotify", "gotify", "matrix", "matrix", "slackhook", "slackhook", "alertmanager", "alertmanager")

// channelFields are the form names a channel panel can post, all kinds together.
var channelFields = []string{"name", "url", "topic", "token", "priority", "method", "headers", "body_template", "to", "from", "homeserver", "access_token", "room_id", "labels"}

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
	windows, err := h.svc.ListMaintenance(ctx, c.scope)
	if err != nil {
		return d, err
	}
	pages, err := h.svc.ListStatusPages(ctx, c.scope)
	if err != nil {
		return d, err
	}
	counts := map[string]int{"channels": len(channels), "routes": len(routes), "maintenance": len(windows), "pages": len(pages)}
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
		for _, w := range windows {
			d.Windows = append(d.Windows, h.windowRow(c, w, root+"maintenance"))
		}
	case "pages":
		for _, p := range pages {
			d.Pages = append(d.Pages, h.statusPageRow(c, p, root+"pages", nil))
		}
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
	case domain.ChannelGotify, domain.ChannelAlertmanager:
		if u, ok := m["url"].(string); ok {
			return u
		}
	case domain.ChannelMatrix:
		if r, ok := m["room_id"].(string); ok {
			return r
		}
	case domain.ChannelSlackhook:
		return "incoming webhook"
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
	case tabName == "maintenance" && q.Has("add"):
		if !c.scope.CanEdit() {
			return domain.ErrForbidden
		}
		o.panel = h.maintenancePanel(c, nil)
		if q.Has("repeat") || q.Has("name") {
			w := h.parseMaintenance(c, q, o.panel)
			o.panel.Errors = map[string]string{}
			h.windowHints(c, o.panel, w)
		}
	case tabName == "maintenance" && q.Get("edit") != "":
		w, err := h.svc.Maintenance(ctx, c.scope, q.Get("edit"))
		if err != nil {
			return err
		}
		o.panel = h.maintenancePanel(c, w)
	case tabName == "pages" && q.Has("add"):
		if !c.scope.CanEdit() {
			return domain.ErrForbidden
		}
		o.panel = h.statusPagePanel(c, nil)
		if q.Has("access") || q.Has("title") {
			h.parseStatusPage(q, o.panel)
			o.panel.Errors = map[string]string{}
		}
	case tabName == "pages" && q.Get("edit") != "":
		p, err := h.svc.StatusPage(ctx, c.scope, q.Get("edit"))
		if err != nil {
			return err
		}
		o.panel = h.statusPagePanel(c, p)
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
	case domain.ChannelGotify:
		v["url"] = str("url")
		if str("token") != "" {
			v["token"] = "***"
		}
		if n, ok := m["priority"].(float64); ok && n > 0 {
			v["priority"] = strconv.Itoa(int(n))
		}
	case domain.ChannelMatrix:
		v["homeserver"], v["room_id"] = str("homeserver"), str("room_id")
		if str("access_token") != "" {
			v["access_token"] = "***"
		}
	case domain.ChannelSlackhook:
		if str("url") != "" {
			v["url"] = "***"
		}
	case domain.ChannelAlertmanager:
		v["url"] = str("url")
		if ls, ok := m["labels"].(map[string]any); ok && len(ls) > 0 {
			keys := make([]string, 0, len(ls))
			for k := range ls {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			lines := make([]string, 0, len(keys))
			for _, k := range keys {
				lines = append(lines, k+": "+fmt.Sprint(ls[k]))
			}
			v["labels"] = strings.Join(lines, "\n")
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
	case "gotify":
		cfg["url"], cfg["token"] = v["url"], v["token"]
		if v["priority"] != "" {
			if n, err := strconv.Atoi(v["priority"]); err != nil {
				errs["priority"] = "A number from 0 to 10."
			} else {
				cfg["priority"] = n
			}
		}
	case "matrix":
		cfg["homeserver"], cfg["access_token"], cfg["room_id"] = v["homeserver"], v["access_token"], v["room_id"]
	case "slackhook":
		cfg["url"] = v["url"]
	case "alertmanager":
		cfg["url"] = v["url"]
		if ls := v["labels"]; ls != "" {
			m := map[string]string{}
			for _, line := range strings.Split(ls, "\n") {
				if line = strings.TrimSpace(line); line == "" {
					continue
				}
				name, val, ok := strings.Cut(line, ":")
				if !ok || strings.TrimSpace(name) == "" {
					errs["labels"] = "One name: value per line."
					break
				}
				m[strings.TrimSpace(name)] = strings.TrimSpace(val)
			}
			if len(m) > 0 {
				cfg["labels"] = m
			}
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
	if _, err := h.svc.SetChannelEnabled(ctx, c.scope, ch.ID, !ch.Enabled); err != nil {
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

// --- maintenance windows ------------------------------------------------

type windowRow struct {
	ID, Name, Sub string
	Cells         []ui.Cell
	Actions       ui.HTML
}

var dayCodes = []struct {
	code string
	day  time.Weekday
}{{"Mon", time.Monday}, {"Tue", time.Tuesday}, {"Wed", time.Wednesday}, {"Thu", time.Thursday}, {"Fri", time.Friday}, {"Sat", time.Saturday}, {"Sun", time.Sunday}}

func dayOptions() []ui.Option {
	out := make([]ui.Option, 0, 7)
	for _, d := range dayCodes {
		out = append(out, ui.Option{Value: d.code, Label: d.code})
	}
	return out
}

func dayNames(days []time.Weekday) string {
	names := make([]string, 0, len(days))
	for _, d := range days {
		for _, c := range dayCodes {
			if c.day == d {
				names = append(names, c.code)
			}
		}
	}
	return strings.Join(names, ", ")
}

// relDay renders "today 14:00", "tomorrow 19:00" or "Tue 29 Sep 19:00".
func relDay(t, now time.Time, loc *time.Location) string {
	lt, ln := t.In(loc), now.In(loc)
	clock := lt.Format("15:04")
	switch {
	case lt.Year() == ln.Year() && lt.YearDay() == ln.YearDay():
		return "today " + clock
	case lt.Year() == ln.AddDate(0, 0, 1).Year() && lt.YearDay() == ln.AddDate(0, 0, 1).YearDay():
		return "tomorrow " + clock
	}
	return lt.Format("Mon 2 Jan 15:04")
}

// describeWindow is the row's second line.
func describeWindow(w *domain.Maintenance, now time.Time) string {
	loc := w.Location()
	if w.Weekly {
		return "weekly · " + dayNames(w.Days) + " " + w.From + "–" + w.To + " " + w.Timezone
	}
	if w.StartsAt == nil || w.EndsAt == nil {
		return "once"
	}
	end := w.EndsAt.In(loc).Format("15:04")
	if w.EndsAt.In(loc).YearDay() != w.StartsAt.In(loc).YearDay() {
		end = relDay(*w.EndsAt, now, loc)
	}
	return "once · " + relDay(*w.StartsAt, now, loc) + "–" + end + " " + w.Timezone
}

func (h *Web) windowRow(c *reqCtx, w *domain.Maintenance, root string) windowRow {
	row := windowRow{ID: w.ID, Name: w.Name, Sub: describeWindow(w, c.now)}
	var tags strings.Builder
	for _, t := range w.MatchTags {
		tags.WriteString(string(ui.Tag(t)))
	}
	if tags.Len() == 0 {
		tags.WriteString("every monitor")
	}
	row.Cells = append(row.Cells, ui.Cell{HTML: ui.HTML(tags.String())})
	loc := w.Location()
	var actions ui.HTML
	if until, active := w.ActiveAt(c.now); active {
		row.Cells = append(row.Cells, ui.Cell{HTML: ui.HTML(`<span class="vk-pill">` + string(ui.Glyph("paused", "")) + `active · ` + view.Span(until.Sub(c.now)) + ` left</span>`), Size: "l"})
		actions = postForm(c, root+"/"+w.ID+"/end", true, ui.Button(ui.ButtonProps{Label: "End now", Type: "submit"}))
	} else if start, _, ok := w.Occurrence(c.now); ok {
		word := "starts "
		if w.Weekly {
			word = "next "
		}
		row.Cells = append(row.Cells, ui.Cell{Text: word + relDay(start, c.now, loc), Size: "l", Mono: true})
	} else {
		row.Cells = append(row.Cells, ui.Cell{Text: "over", Size: "l", Mono: true})
	}
	row.Actions = actions + ui.Button(ui.ButtonProps{Label: "Edit", Href: root + "?edit=" + url.QueryEscape(w.ID)})
	return row
}

func (h *Web) windowTimezones(c *reqCtx, current string) []ui.Option {
	out := []ui.Option{{Value: c.project.Timezone, Label: c.project.Timezone + " (project)"}}
	seen := map[string]bool{c.project.Timezone: true}
	if current != "" && !seen[current] {
		out = append(out, ui.Option{Value: current, Label: current})
		seen[current] = true
	}
	for _, z := range commonZones {
		if !seen[z] {
			out = append(out, ui.Option{Value: z, Label: z})
			seen[z] = true
		}
	}
	return out
}

func (h *Web) maintenancePanel(c *reqCtx, w *domain.Maintenance) *panelData {
	root := c.projectPath() + "/settings/maintenance"
	p := &panelData{
		Title: "Add window", Action: root, KindPath: root + "?add=1", CancelPath: root, CSRF: c.csrf(), SubmitLabel: "Save window",
		Values: map[string]string{"name": "", "match_tags": "", "repeat": "weekly", "from": "", "to": "", "timezone": c.project.Timezone}, Errors: map[string]string{},
		Repeat: "weekly", DayOptions: dayOptions(),
	}
	if w != nil {
		p.Title, p.EditID, p.Action, p.DeletePath, p.KindPath = "Edit window", w.ID, root+"/"+w.ID, root+"/"+w.ID+"/delete", root+"?edit="+url.QueryEscape(w.ID)
		p.Values["name"], p.Values["match_tags"], p.Values["timezone"] = w.Name, strings.Join(w.MatchTags, ", "), w.Timezone
		if w.Weekly {
			p.Values["from"], p.Values["to"] = w.From, w.To
			for _, d := range w.Days {
				for _, code := range dayCodes {
					if code.day == d {
						p.Days = append(p.Days, code.code)
					}
				}
			}
		} else {
			p.Repeat, p.Values["repeat"] = "once", "once"
			loc := w.Location()
			if w.StartsAt != nil {
				p.Values["from"] = w.StartsAt.In(loc).Format("2006-01-02 15:04")
			}
			if w.EndsAt != nil {
				p.Values["to"] = w.EndsAt.In(loc).Format("2006-01-02 15:04")
			}
		}
	}
	p.Timezones = h.windowTimezones(c, p.Values["timezone"])
	h.windowHints(c, p, w)
	return p
}

// parseMaintenance reads the panel's fields into a window.
func (h *Web) parseMaintenance(c *reqCtx, values map[string][]string, p *panelData) *domain.Maintenance {
	get := func(k string) string {
		v := ""
		if vs := values[k]; len(vs) > 0 {
			v = strings.TrimSpace(vs[0])
		}
		p.Values[k] = v
		return v
	}
	w := &domain.Maintenance{Name: get("name"), Timezone: get("timezone")}
	if v := get("match_tags"); v != "" {
		w.MatchTags = domain.NormalizeTags(strings.Split(v, ","))
	}
	if w.Timezone == "" {
		w.Timezone = c.project.Timezone
		p.Values["timezone"] = w.Timezone
	}
	p.Repeat = get("repeat")
	if p.Repeat != "once" {
		p.Repeat, p.Values["repeat"] = "weekly", "weekly"
	}
	from, to := get("from"), get("to")
	if p.Repeat == "weekly" {
		w.Weekly, w.From, w.To = true, from, to
		p.Days = values["days"]
		for _, code := range p.Days {
			for _, d := range dayCodes {
				if d.code == code {
					w.Days = append(w.Days, d.day)
				}
			}
		}
		return w
	}
	loc := w.Location()
	parse := func(k, v string) *time.Time {
		if v == "" {
			return nil
		}
		t, err := time.ParseInLocation("2006-01-02 15:04", v, loc)
		if err != nil {
			p.Errors[k] = "Use a date and time such as 2026-09-29 19:00."
			return nil
		}
		return &t
	}
	w.StartsAt, w.EndsAt = parse("from", from), parse("to", to)
	return w
}

// windowHints fills the placeholders and the sentence under the panel.
func (h *Web) windowHints(c *reqCtx, p *panelData, w *domain.Maintenance) {
	if p.Repeat == "weekly" {
		p.Values["from_placeholder"], p.Values["to_placeholder"] = "02:00", "04:00"
	} else {
		p.Values["from_placeholder"], p.Values["to_placeholder"] = c.now.In(h.location(c, nil)).Format("2006-01-02 15:04"), c.now.In(h.location(c, nil)).Add(2*time.Hour).Format("2006-01-02 15:04")
	}
	who := "Every monitor records"
	if w != nil && len(w.MatchTags) > 0 {
		who = "Monitors tagged " + strings.Join(w.MatchTags, ", ") + " record"
	}
	p.NextHint = "Times are in " + p.Values["timezone"] + ". " + who + " as usual but do not alert or go down."
	if w == nil {
		return
	}
	probe := *w
	probe.Normalize()
	loc := probe.Location()
	if until, active := probe.ActiveAt(c.now); active {
		p.NextHint = "Active until " + relDay(until, c.now, loc) + ". " + who + " as usual but do not alert or go down."
	} else if start, end, ok := probe.Occurrence(c.now); ok {
		p.NextHint = "Next: " + relDay(start, c.now, loc) + "–" + end.In(loc).Format("15:04") + ". " + who + " as usual but do not alert or go down."
	}
}

func (h *Web) saveMaintenance(c *reqCtx) error {
	if err := c.r.ParseForm(); err != nil {
		return err
	}
	ctx := c.r.Context()
	id := c.r.PathValue("id")
	var cur *domain.Maintenance
	if id != "" {
		var err error
		if cur, err = h.svc.Maintenance(ctx, c.scope, id); err != nil {
			return err
		}
	}
	p := h.maintenancePanel(c, cur)
	w := h.parseMaintenance(c, c.r.PostForm, p)
	h.windowHints(c, p, w)
	opts := settingsOpts{panel: p}
	if len(p.Errors) > 0 {
		return h.renderTab(c, http.StatusUnprocessableEntity, "maintenance", opts)
	}
	var err error
	if cur == nil {
		_, err = h.svc.CreateMaintenance(ctx, c.scope, w)
	} else {
		_, err = h.svc.UpdateMaintenance(ctx, c.scope, id, w)
	}
	if err != nil {
		if applyPanelValidation(p, err) {
			for from, to := range map[string]string{"starts_at": "from", "ends_at": "to"} {
				if msg, ok := p.Errors[from]; ok {
					p.Errors[to] = msg
					delete(p.Errors, from)
				}
			}
			return h.renderTab(c, http.StatusUnprocessableEntity, "maintenance", opts)
		}
		return err
	}
	return h.redirect(c, c.projectPath()+"/settings/maintenance")
}

func (h *Web) deleteMaintenance(c *reqCtx) error {
	if err := h.svc.DeleteMaintenance(c.r.Context(), c.scope, c.r.PathValue("id")); err != nil {
		return err
	}
	return h.redirect(c, c.projectPath()+"/settings/maintenance")
}

func (h *Web) endMaintenance(c *reqCtx) error {
	if _, err := h.svc.EndMaintenance(c.r.Context(), c.scope, c.r.PathValue("id")); err != nil {
		return err
	}
	if c.htmx() {
		return h.renderTab(c, http.StatusOK, "maintenance", settingsOpts{})
	}
	return h.redirect(c, c.projectPath()+"/settings/maintenance")
}

// --- status pages -----------------------------------------------------------

type pageRow struct {
	Slug, Title, Sub string
	Cells            []ui.Cell
	Actions          ui.HTML
}

// pageURL is the public address of a page.
func (h *Web) pageURL(slug string) string {
	return strings.TrimRight(h.svc.Config().BaseURL, "/") + "/s/" + slug
}

// pagePrefix is the address shown before the slug field: "vink.w4j.nl/s/".
func (h *Web) pagePrefix() string {
	base := strings.TrimRight(h.svc.Config().BaseURL, "/")
	if u, err := url.Parse(base); err == nil && u.Host != "" {
		return u.Host + "/s/"
	}
	return base + "/s/"
}

// incidentOptions are the Incidents select's choices.
var incidentOptions = []ui.Option{
	{Value: domain.IncidentsOpen, Label: "Open incidents"},
	{Value: "7d", Label: "Open and the last 7 days"},
	{Value: "30d", Label: "Open and the last 30 days"},
	{Value: "90d", Label: "Open and the last 90 days"},
	{Value: domain.IncidentsNone, Label: "None"},
}

// incidentsCell reads a page's incidents setting in a row.
func incidentsCell(v string) string {
	switch v {
	case domain.IncidentsNone:
		return "no incidents"
	case "7d", "30d", "90d":
		return "incidents, " + strings.TrimSuffix(v, "d") + " days"
	}
	return "open incidents"
}

// statusPageRow is a page in a settings list. projectNames names an org
// page's projects; a project page's row reads its tags instead.
func (h *Web) statusPageRow(c *reqCtx, p *domain.StatusPage, root string, projectNames map[string]string) pageRow {
	row := pageRow{Slug: p.Slug, Title: p.Title, Sub: h.pageURL(p.Slug)}
	access := "public"
	if p.HasPassword() {
		access = "password"
	}
	var shows strings.Builder
	if p.IsOrg() {
		var names []string
		for _, id := range p.Projects {
			if n, ok := projectNames[id]; ok {
				names = append(names, n)
			}
		}
		if len(names) == 0 {
			names = []string{"every project"}
		}
		shows.WriteString(esc(strings.Join(names, ", ")))
		if p.GroupBy == domain.GroupByTag {
			shows.WriteString(" · by tag")
		}
	}
	for _, t := range p.MatchTags {
		shows.WriteString(string(ui.Tag(t)))
	}
	if shows.Len() == 0 {
		shows.WriteString("every monitor")
	}
	domainText := "no custom domain"
	if p.CustomDomain != "" {
		domainText = p.CustomDomain
	}
	row.Cells = []ui.Cell{{Text: access, Size: "s"}, {HTML: ui.HTML(shows.String())}, {Text: incidentsCell(p.Incidents)}, {Text: domainText, Size: "l", Mono: true}}
	row.Actions = ui.Button(ui.ButtonProps{Label: "Open", Href: c.href("/s/" + p.Slug)}) + ui.Button(ui.ButtonProps{Label: "Edit", Href: root + "?edit=" + url.QueryEscape(p.Slug)})
	return row
}

func (h *Web) statusPagePanel(c *reqCtx, p *domain.StatusPage) *panelData {
	root := c.projectPath() + "/settings/pages"
	panel := &panelData{
		Title: "Add page", Action: root, KindPath: root + "?add=1", CancelPath: root, CSRF: c.csrf(), SubmitLabel: "Save page",
		Values: map[string]string{"title": "", "slug": "", "match_tags": "", "incidents": domain.IncidentsOpen, "access": "public", "password": "", "custom_domain": "", "password_placeholder": "only with Password", "prefix": h.pagePrefix()},
		Errors: map[string]string{}, Repeat: "public", IncidentOptions: incidentOptions,
	}
	if p != nil {
		panel.Title, panel.EditID, panel.Action, panel.DeletePath, panel.KindPath = "Edit page", p.Slug, root+"/"+p.Slug, root+"/"+p.Slug+"/delete", root+"?edit="+url.QueryEscape(p.Slug)
		panel.Values["title"], panel.Values["slug"], panel.Values["match_tags"], panel.Values["custom_domain"] = p.Title, p.Slug, strings.Join(p.MatchTags, ", "), p.CustomDomain
		panel.Values["incidents"] = p.Incidents
		if p.HasPassword() {
			panel.Values["access"], panel.Repeat = "password", "password"
			panel.Values["password_placeholder"] = "unchanged"
		}
	}
	return panel
}

// parseStatusPage reads the panel's fields; the password comes back separately.
func (h *Web) parseStatusPage(values map[string][]string, p *panelData) (*domain.StatusPage, string) {
	get := func(k string) string {
		v := ""
		if vs := values[k]; len(vs) > 0 {
			v = strings.TrimSpace(vs[0])
		}
		p.Values[k] = v
		return v
	}
	page := &domain.StatusPage{Title: get("title"), Slug: get("slug"), CustomDomain: get("custom_domain"), Incidents: get("incidents"), Public: true}
	if p.Org {
		page.GroupBy = get("group_by")
		page.Projects = values["projects"]
		chosen := map[string]bool{}
		for _, id := range page.Projects {
			chosen[id] = true
		}
		for i := range p.ProjectChecks {
			p.ProjectChecks[i].Checked = chosen[p.ProjectChecks[i].Value]
		}
	}
	if v := get("match_tags"); v != "" {
		page.MatchTags = domain.NormalizeTags(strings.Split(v, ","))
	}
	if page.Slug == "" && page.Title != "" {
		page.Slug = domain.Slugify(page.Title)
		p.Values["slug"] = page.Slug
	}
	password := get("password")
	p.Values["password"] = ""
	p.Repeat = get("access")
	if p.Repeat != "password" {
		p.Repeat, p.Values["access"] = "public", "public"
		password = ""
	} else {
		page.Public = false
	}
	return page, password
}

func (h *Web) saveStatusPage(c *reqCtx) error {
	if err := c.r.ParseForm(); err != nil {
		return err
	}
	ctx := c.r.Context()
	slug := c.r.PathValue("slug")
	var cur *domain.StatusPage
	if slug != "" {
		var err error
		if cur, err = h.svc.StatusPage(ctx, c.scope, slug); err != nil {
			return err
		}
	}
	p := h.statusPagePanel(c, cur)
	page, password := h.parseStatusPage(c.r.PostForm, p)
	if cur != nil && cur.HasPassword() {
		p.Values["password_placeholder"] = "unchanged"
	}
	opts := settingsOpts{panel: p}
	var err error
	if cur == nil {
		_, err = h.svc.CreateStatusPage(ctx, c.scope, page, password)
	} else {
		_, err = h.svc.UpdateStatusPage(ctx, c.scope, slug, page, password)
	}
	if err != nil {
		if applyPanelValidation(p, err) {
			return h.renderTab(c, http.StatusUnprocessableEntity, "pages", opts)
		}
		if errors.Is(err, domain.ErrConflict) {
			p.Errors["slug"] = "This address is taken."
			return h.renderTab(c, http.StatusUnprocessableEntity, "pages", opts)
		}
		return err
	}
	return h.redirect(c, c.projectPath()+"/settings/pages")
}

func (h *Web) deleteStatusPage(c *reqCtx) error {
	if err := h.svc.DeleteStatusPage(c.r.Context(), c.scope, c.r.PathValue("slug")); err != nil {
		return err
	}
	return h.redirect(c, c.projectPath()+"/settings/pages")
}
