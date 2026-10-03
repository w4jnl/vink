package web

import (
	"errors"
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

// The agents tab of the org settings: /o/{org}/admin/agents, the add
// panel on ?add=1, the drawer on …/agents/{name}, labels and revoke.

type agentRow struct {
	Name    string
	Sub     string
	Muted   bool
	Href    string
	Current bool
	Cells   []ui.Cell
	Actions ui.HTML
}

type agentPanel struct {
	Action, CancelPath, CSRF string
	Values, Errors           map[string]string
	Error                    string
}

type agentDrawer struct {
	Name       string
	State      ui.HTML
	Tags       []string
	LabelsPath string
	RevokePath string
	ClosePath  string
	CSRF       string
	Notice     *noteData
	KV         [][2]string
	Monitors   []ui.MonitorRowProps
	More       []moreLink
	Count      int
	Hint       string
	// Labels is the inline edit form when open.
	Labels *agentPanel
}

type moreLink struct {
	Text, Href string
}

type agentsData struct {
	adminData
	Quota   ui.HTML
	Panel   *agentPanel
	Created *noteData
	Flash   string
	Rows    []agentRow
}

// agentsTab renders the list, with the add panel, a created notice or a
// drawer as asked.
func (h *Web) agentsTab(c *reqCtx, status int, panel *agentPanel, created *noteData, drawer *agentDrawer) error {
	d, err := h.agentsData(c, panel, created, drawer)
	if err != nil {
		return err
	}
	return h.render(c, status, "admin", "layout", d)
}

func (h *Web) agentsData(c *reqCtx, panel *agentPanel, created *noteData, drawer *agentDrawer) (agentsData, error) {
	ad, err := h.adminData(c, "agents")
	if err != nil {
		return agentsData{}, err
	}
	ad.Drawer = drawer
	d := agentsData{adminData: ad, Panel: panel, Created: created, Flash: c.r.URL.Query().Get("flash")}
	ctx := c.r.Context()
	agents, err := h.svc.ListAgents(ctx, c.scope)
	if err != nil {
		return d, err
	}
	counts, err := h.svc.AgentMonitorCounts(ctx, c.scope)
	if err != nil {
		return d, err
	}
	if c.org.QuotaAgents != nil {
		d.Quota = ui.Usage(len(agents), int(*c.org.QuotaAgents), "agents")
	}
	loc := h.orgLocation(c)
	for _, a := range agents {
		row := agentRow{Name: a.Name, Sub: labelsSub(a.Labels), Href: d.TabPath + "/" + a.Name, Current: drawer != nil && drawer.Name == a.Name}
		state := a.State(h.svc.AgentConnected(a.ID))
		row.Muted = state == domain.AgentWaiting
		version := a.Version
		if version == "" {
			version = "—"
		}
		seen := "never"
		if a.LastSeenAt != nil {
			seen = "seen " + seenShort(*a.LastSeenAt, c.now, loc)
		}
		row.Cells = []ui.Cell{
			{HTML: h.agentBadge(a, state, c.now, false)},
			{Text: version, Size: "s", Mono: true},
			{Text: strconv.Itoa(counts[a.ID]) + " " + plural2(counts[a.ID], "monitor", "monitors")},
			{Text: seen, Mono: true},
		}
		row.Actions = postForm(c, d.TabPath+"/"+a.Name+"/revoke", false, ui.Button(ui.ButtonProps{Label: "Revoke", Variant: "danger", Confirm: "Really revoke?", Type: "submit"}))
		d.Rows = append(d.Rows, row)
	}
	return d, nil
}

// agentBadge is the state word: connected, offline for how long, waiting.
func (h *Web) agentBadge(a *domain.Agent, state domain.AgentState, now time.Time, pill bool) ui.HTML {
	p := ui.StateBadgeProps{Pill: pill}
	switch state {
	case domain.AgentConnected:
		p.State, p.Label = "up", "connected"
	case domain.AgentOffline:
		p.State, p.Label = "late", "offline"
		if a.LastSeenAt != nil {
			p.Since = view.Span(now.Sub(*a.LastSeenAt))
			if pill {
				p.Since = "for " + p.Since
			}
		}
	default:
		p.State, p.Label = "new", "waiting"
	}
	return ui.StateBadge(p)
}

func labelsSub(labels map[string]string) string {
	if len(labels) == 0 {
		return "no labels"
	}
	return strings.ReplaceAll(domain.LabelsString(labels), ",", " · ")
}

// seenShort writes "2 s ago" within the hour, the clock today, else the day.
func seenShort(t, now time.Time, loc *time.Location) string {
	switch d := now.Sub(t); {
	case d < time.Hour:
		return view.Ago(t, now)
	case t.In(loc).YearDay() == now.In(loc).YearDay() && t.In(loc).Year() == now.In(loc).Year():
		return t.In(loc).Format("15:04")
	default:
		return t.In(loc).Format("2 Jan")
	}
}

// orgLocation is the timezone of the org's first project, or UTC.
func (h *Web) orgLocation(c *reqCtx) *time.Location {
	projects, err := h.svc.ProjectsForUser(c.r.Context(), c.principal.User.ID, c.principal.InstanceAdmin)
	if err == nil {
		for _, p := range projects {
			if p.OrgSlug == c.org.Slug {
				if loc, err := time.LoadLocation(p.Timezone); err == nil {
					return loc
				}
			}
		}
	}
	return time.UTC
}

func (h *Web) newAgentPanel(c *reqCtx) *agentPanel {
	return &agentPanel{Action: c.orgPath() + "/agents", CancelPath: c.orgPath() + "/agents", CSRF: c.csrf(), Values: map[string]string{}, Errors: map[string]string{}}
}

// agentsList is the tab itself; ?add=1 opens the panel.
func (h *Web) agentsList(c *reqCtx) error {
	var panel *agentPanel
	if c.r.URL.Query().Get("add") == "1" {
		panel = h.newAgentPanel(c)
	}
	return h.agentsTab(c, http.StatusOK, panel, nil, nil)
}

// createAgent registers the agent and shows the token once, in this
// response, never in a URL.
func (h *Web) createAgent(c *reqCtx) error {
	p := h.newAgentPanel(c)
	p.Values["name"] = strings.TrimSpace(c.r.PostFormValue("name"))
	p.Values["labels"] = strings.TrimSpace(c.r.PostFormValue("labels"))
	labels, err := domain.ParseLabels(p.Values["labels"])
	if err != nil {
		p.Errors["labels"] = labelsError(err)
		return h.agentsTab(c, http.StatusUnprocessableEntity, p, nil, nil)
	}
	a, token, err := h.svc.CreateAgent(c.r.Context(), c.scope, p.Values["name"], labels)
	if err != nil {
		if ve, ok := domain.AsValidation(err); ok {
			for _, fe := range ve.Errors {
				field := fe.Field
				if field == "quota" {
					field = "name"
				}
				p.Errors[field] = capitalise(fe.Msg) + "."
			}
			return h.agentsTab(c, http.StatusUnprocessableEntity, p, nil, nil)
		}
		if errors.Is(err, domain.ErrConflict) {
			p.Errors["name"] = "An agent with this name exists."
			return h.agentsTab(c, http.StatusUnprocessableEntity, p, nil, nil)
		}
		return err
	}
	command := domain.AgentCommand(h.svc.Config().BaseURL, token, a.Labels)
	note := &noteData{Tone: "ok", Title: "Agent " + a.Name + " created.", Text: "Run this on its host. The token is shown once; vink keeps only a hash.", HTML: ui.Code(command, true, "Copy", false)}
	return h.agentsTab(c, http.StatusOK, nil, note, nil)
}

// agentDetail opens the drawer; ?labels=1 shows the labels form in it.
func (h *Web) agentDetail(c *reqCtx) error {
	d, err := h.agentDrawerFor(c, c.r.PathValue("name"), c.r.URL.Query().Get("labels") == "1", nil)
	if err != nil {
		return err
	}
	return h.agentsTab(c, http.StatusOK, nil, nil, d)
}

func (h *Web) agentDrawerFor(c *reqCtx, name string, editLabels bool, form *agentPanel) (*agentDrawer, error) {
	ctx := c.r.Context()
	a, err := h.svc.Agent(ctx, c.scope, name)
	if err != nil {
		return nil, err
	}
	loc := h.orgLocation(c)
	root := c.orgPath() + "/agents"
	state := a.State(h.svc.AgentConnected(a.ID))
	d := &agentDrawer{
		Name: a.Name, State: h.agentBadge(a, state, c.now, true), ClosePath: root, CSRF: c.csrf(),
		LabelsPath: root + "/" + a.Name + "?labels=1", RevokePath: root + "/" + a.Name + "/revoke",
		Hint: "A monitor runs here when its Run from names " + a.Name + ", or labels only this agent has.",
	}
	keys := make([]string, 0, len(a.Labels))
	for k := range a.Labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		d.Tags = append(d.Tags, k+"="+a.Labels[k])
	}
	if editLabels || form != nil {
		if form == nil {
			form = &agentPanel{Values: map[string]string{"labels": domain.LabelsString(a.Labels)}, Errors: map[string]string{}}
		}
		form.Action, form.CancelPath, form.CSRF = root+"/"+a.Name+"/labels", root+"/"+a.Name, c.csrf()
		d.Labels = form
	}
	monitors, err := h.svc.AgentMonitors(ctx, a.ID)
	if err != nil {
		return nil, err
	}
	d.Count = len(monitors)
	late := 0
	projects := map[string]*domain.Project{}
	byProject := map[string]int{}
	for i, m := range monitors {
		if m.State == domain.StateLate {
			late++
		}
		p, ok := projects[m.ProjectID]
		if !ok {
			p, err = h.svc.ProjectByID(ctx, m.ProjectID)
			if err != nil {
				return nil, err
			}
			projects[m.ProjectID] = p
		}
		if i >= 6 {
			byProject[m.ProjectID]++
			continue
		}
		row := ui.MonitorRowProps{State: string(m.State), Name: m.Name, Slug: m.Slug, Kind: string(m.Kind), Tags: m.Tags, Href: "/o/" + c.org.Slug + "/p/" + p.Slug + "/m/" + m.Slug, Points: []float64{}}
		if len(row.Tags) > 3 {
			row.Tags = row.Tags[:3]
		}
		switch {
		case m.State == domain.StateLate && state != domain.AgentConnected:
			row.Last = "agent offline"
			if a.LastSeenAt != nil {
				row.Last += " " + view.Span(c.now.Sub(*a.LastSeenAt))
			}
		case m.LastObsAt == nil:
			row.Last = "no checks yet"
		default:
			row.Last = view.Ago(*m.LastObsAt, c.now)
			if last, err := h.svc.ListObservations(ctx, domain.Scope{OrgID: c.org.ID, ProjectID: m.ProjectID, Role: domain.RoleViewer, UserID: c.scope.UserID, InstanceAdmin: c.scope.InstanceAdmin}, m.Slug, service.HistoryPage{Limit: 1}); err == nil && len(last) == 1 {
				row.Last += lastDatum(last[0])
			}
		}
		if m.LastObsAt != nil {
			row.LastAbs = "last check " + view.Abs(*m.LastObsAt, loc)
		}
		d.Monitors = append(d.Monitors, row)
	}
	ids := make([]string, 0, len(byProject))
	for id := range byProject {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return projects[ids[i]].Slug < projects[ids[j]].Slug })
	for _, id := range ids {
		d.More = append(d.More, moreLink{Text: strconv.Itoa(byProject[id]) + " more in " + projects[id].Slug, Href: "/o/" + c.org.Slug + "/p/" + projects[id].Slug})
	}
	if state != domain.AgentConnected && late > 0 {
		when := "when the agent went away"
		if a.LastSeenAt != nil {
			when = "with the agent at " + a.LastSeenAt.In(loc).Format("15:04")
		}
		d.Notice = &noteData{Tone: "warn", Title: plural(late, "monitor") + " " + isAre(late) + " late.", Text: "Their checks stopped " + when + ". They stay late with reason agent offline; an agent going away never makes a monitor down."}
	}
	d.KV = [][2]string{}
	if a.LastSeenAt != nil {
		d.KV = append(d.KV, [2]string{"last seen", a.LastSeenAt.In(loc).Format("Mon 2 Jan 15:04:05 MST")})
	} else {
		d.KV = append(d.KV, [2]string{"last seen", "never"})
	}
	if a.LastAddr != "" {
		d.KV = append(d.KV, [2]string{"from", a.LastAddr})
	}
	if a.Version != "" {
		d.KV = append(d.KV, [2]string{"version", a.Version})
	}
	d.KV = append(d.KV, [2]string{"token", "vat_" + a.TokenPrefix + "…"})
	if since, ok := h.svc.AgentConnectedSince(a.ID); ok {
		d.KV = append(d.KV, [2]string{"connected", view.For(since, c.now)[4:] + ", since " + since.In(loc).Format("15:04")})
	}
	return d, nil
}

// labelsError is the sentence for a labels field that did not parse.
func labelsError(err error) string {
	if ve, ok := domain.AsValidation(err); ok && len(ve.Errors) > 0 {
		return capitalise(ve.Errors[0].Msg) + ". Use key=value pairs separated by commas, like site=dc2,zone=dmz."
	}
	return "Use key=value pairs separated by commas, like site=dc2,zone=dmz."
}

func isAre(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}

// saveAgentLabels replaces the labels from the drawer form.
func (h *Web) saveAgentLabels(c *reqCtx) error {
	name := c.r.PathValue("name")
	form := &agentPanel{Values: map[string]string{"labels": strings.TrimSpace(c.r.PostFormValue("labels"))}, Errors: map[string]string{}}
	labels, err := domain.ParseLabels(form.Values["labels"])
	if err != nil {
		form.Errors["labels"] = labelsError(err)
	} else if _, err := h.svc.UpdateAgentLabels(c.r.Context(), c.scope, name, labels); err != nil {
		ve, ok := domain.AsValidation(err)
		if !ok {
			return err
		}
		form.Errors["labels"] = capitalise(ve.Errors[0].Msg) + "."
	}
	if len(form.Errors) > 0 {
		d, err := h.agentDrawerFor(c, name, true, form)
		if err != nil {
			return err
		}
		return h.agentsTab(c, http.StatusUnprocessableEntity, nil, nil, d)
	}
	return h.redirect(c, c.orgPath()+"/agents/"+url.PathEscape(name))
}

// revokeAgent deletes the agent; its socket closes and its monitors turn late.
func (h *Web) revokeAgent(c *reqCtx) error {
	name := c.r.PathValue("name")
	if err := h.svc.RevokeAgent(c.r.Context(), c.scope, name); err != nil {
		return err
	}
	return h.redirect(c, c.orgPath()+"/agents?flash="+url.QueryEscape("Agent "+name+" revoked. Its monitors turn late until they are moved or it comes back with a new token."))
}
