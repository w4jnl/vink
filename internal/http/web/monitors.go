package web

import (
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/http/web/ui"
	"github.com/w4jnl/vink/internal/http/web/view"
	"github.com/w4jnl/vink/internal/service"
)

type monitorsData struct {
	base
	Total     int
	DownCount int
	Chips     []ui.ChipProps
	Rows      []ui.MonitorRowProps
	Filtered  bool
	PingBase  string
	PingKey   string
	ListPath  string
	NewPath   string
	Drawer    *drawerData
}

func (h *Web) pingBase() string {
	return h.svc.Config().PingBaseURL + "/ping/"
}

// listData builds the monitors page without a drawer.
func (h *Web) listData(c *reqCtx) (monitorsData, error) {
	ctx := c.r.Context()
	q := c.r.URL.Query()
	filter := service.MonitorFilter{Tag: q.Get("tag"), State: domain.State(q.Get("state")), Query: q.Get("q")}
	if !filter.State.Valid() {
		filter.State = ""
	}
	all, err := h.svc.ListMonitors(ctx, c.scope, service.MonitorFilter{})
	if err != nil {
		return monitorsData{}, err
	}
	d := monitorsData{base: h.baseFor(c, "Monitors"), Total: len(all), PingBase: h.pingBase(), NewPath: c.projectPath() + "/m/new"}
	d.Query, d.FilterState, d.FilterTag = filter.Query, string(filter.State), filter.Tag
	if c.scope.CanSeePingKey() {
		d.PingKey = c.project.PingKey
	} else {
		d.PingKey = "<ping key>"
	}
	counts := map[domain.State]int{}
	tagCounts := map[string]int{}
	for _, m := range all {
		counts[m.State]++
		for _, t := range m.Tags {
			tagCounts[t]++
		}
	}
	d.DownCount = counts[domain.StateDown]
	d.Down = d.DownCount > 0
	for _, s := range domain.States {
		if counts[s] == 0 {
			continue
		}
		pressed := filter.State == s
		value := string(s)
		if pressed {
			value = ""
		}
		d.Chips = append(d.Chips, ui.ChipProps{Label: string(s), State: string(s), Count: ui.Count(counts[s]), Pressed: pressed, Type: "submit", Attrs: ui.Attr("name", "state") + ui.Attr("value", value)})
	}
	tags := make([]string, 0, len(tagCounts))
	for t := range tagCounts {
		tags = append(tags, t)
	}
	sort.Slice(tags, func(i, j int) bool {
		if tagCounts[tags[i]] != tagCounts[tags[j]] {
			return tagCounts[tags[i]] > tagCounts[tags[j]]
		}
		return tags[i] < tags[j]
	})
	for _, t := range tags {
		pressed := filter.Tag == t
		value := t
		if pressed {
			value = ""
		}
		d.Chips = append(d.Chips, ui.ChipProps{Label: t, Count: ui.Count(tagCounts[t]), Pressed: pressed, Type: "submit", Attrs: ui.Attr("name", "tag") + ui.Attr("value", value)})
	}
	if filter.State != "" {
		d.Chips = append(d.Chips, ui.ChipProps{Label: "clear", Type: "submit", Attrs: ui.Attr("name", "state") + ui.Attr("value", "") + ui.Attr("formaction", c.projectPath()) + ui.Attr("hidden", "")})
	}
	shown := make([]*domain.Monitor, 0, len(all))
	for _, m := range all {
		if filterMatches(filter, m) {
			shown = append(shown, m)
		}
	}
	sort.SliceStable(shown, func(i, j int) bool {
		a, b := shown[i], shown[j]
		if a.State.SortRank() != b.State.SortRank() {
			return a.State.SortRank() < b.State.SortRank()
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	d.Filtered = filter.Tag != "" || filter.State != "" || filter.Query != ""
	current := c.r.PathValue("slug")
	for _, m := range shown {
		d.Rows = append(d.Rows, h.row(c, m, m.Slug == current))
	}
	params := q
	params.Set("partial", "list")
	d.ListPath = c.projectPath() + "?" + params.Encode()
	return d, nil
}

func filterMatches(f service.MonitorFilter, m *domain.Monitor) bool {
	if f.Tag != "" && !m.HasAllTags([]string{f.Tag}) {
		return false
	}
	if f.State != "" && m.State != f.State {
		return false
	}
	if q := strings.ToLower(strings.TrimSpace(f.Query)); q != "" && !strings.Contains(strings.ToLower(m.Name), q) && !strings.Contains(m.Slug, q) {
		return false
	}
	return true
}

func (h *Web) location(c *reqCtx, m *domain.Monitor) *time.Location {
	if m.Heartbeat != nil {
		if loc, err := m.Heartbeat.Location(c.project.Timezone); err == nil {
			return loc
		}
	}
	if loc, err := time.LoadLocation(c.project.Timezone); err == nil {
		return loc
	}
	return time.UTC
}

// row builds the list row for a monitor.
func (h *Web) row(c *reqCtx, m *domain.Monitor, current bool) ui.MonitorRowProps {
	loc := h.location(c, m)
	path := c.projectPath() + "/m/" + m.Slug
	row := ui.MonitorRowProps{
		State: string(m.State), Name: m.Name, Slug: m.Slug, Kind: string(m.Kind), Tags: m.Tags, Href: path, Current: current,
		Attrs: ui.Attr("hx-get", path) + ui.Attr("hx-target", "#drawer") + ui.Attr("hx-push-url", "true"),
	}
	if len(row.Tags) > 3 {
		row.Tags = row.Tags[:3]
	}
	if m.LastObsAt == nil {
		row.Last = "no pings yet"
	} else {
		row.Last = view.Ago(*m.LastObsAt, c.now)
		row.LastAbs = view.Abs(*m.LastObsAt, loc)
		if last, err := h.svc.ListObservations(c.r.Context(), c.scope, m.Slug, service.ObservationPage{Limit: 1}); err == nil && len(last) == 1 {
			row.Last += lastDatum(last[0])
		}
	}
	expected := h.svc.ExpectedAt(m, c.project.Timezone)
	switch m.State {
	case domain.StatePaused:
		row.Next = "paused " + view.Ago(m.StateSince, c.now)
	case domain.StateDown:
		row.Next = "down " + view.For(m.StateSince, c.now)
	default:
		if expected != nil {
			row.Next = "due " + view.In(*expected, c.now)
		}
	}
	return row
}

// lastDatum is the one datum that matters about the last observation.
func lastDatum(o *domain.Observation) string {
	switch {
	case o.DurationMs != nil:
		return " · " + view.RunDuration(*o.DurationMs)
	case o.Signal == domain.SignalExit && o.ExitCode != nil && *o.ExitCode != 0:
		return " · exit " + strconv.FormatInt(*o.ExitCode, 10)
	case o.Signal == domain.SignalFail:
		return " · fail"
	}
	return ""
}

func (h *Web) monitors(c *reqCtx) error {
	d, err := h.listData(c)
	if err != nil {
		return err
	}
	switch c.r.URL.Query().Get("partial") {
	case "list":
		return h.render(c, http.StatusOK, "monitors", "monitors-list", d)
	case "main":
		return h.render(c, http.StatusOK, "monitors", "monitors-main", d)
	case "drawer-empty":
		return h.render(c, http.StatusOK, "monitors", "drawer", (*drawerData)(nil))
	}
	if c.htmx() {
		return h.render(c, http.StatusOK, "monitors", "monitors-main", d)
	}
	return h.render(c, http.StatusOK, "monitors", "layout", d)
}

// --- drawer ---------------------------------------------------------------

type drawerData struct {
	Path, ProjectPath, CSRF string
	Name, Slug              string
	Badge                   ui.StateBadgeProps
	Tags                    []string
	Paused                  bool
	PingBase, PingKey       string
	Summary                 string
	Cells, Legend           []string
	Observations            []obsRow
	Events                  []eventRow
}

type obsRow struct{ State, Clock, Abs, Text, Right string }

type eventRow struct{ State, Ago, Abs, Text string }

func (h *Web) drawerData(c *reqCtx, m *domain.Monitor) (*drawerData, error) {
	ctx := c.r.Context()
	loc := h.location(c, m)
	d := &drawerData{
		Path: c.projectPath() + "/m/" + m.Slug, ProjectPath: c.projectPath(), CSRF: c.csrf(), Name: m.Name, Slug: m.Slug,
		Badge: ui.StateBadgeProps{State: string(m.State), Pill: true}, Tags: m.Tags, Paused: m.Paused, PingBase: h.pingBase(),
	}
	if c.scope.CanSeePingKey() {
		d.PingKey = c.project.PingKey
	} else {
		d.PingKey = "<ping key>"
	}
	if m.State != domain.StateNew {
		d.Badge.Since = view.For(m.StateSince, c.now)
	}
	if s := m.Heartbeat; s != nil {
		parts := []string{s.Schedule.String()}
		if s.Schedule.Cron != "" {
			parts[0] += " (" + loc.String() + ")"
		}
		parts = append(parts, "grace "+s.Grace.String())
		if s.MaxRuntime > 0 {
			parts = append(parts, "max runtime "+s.MaxRuntime.String())
		}
		if s.FailureThreshold > 1 {
			parts = append(parts, "down after "+strconv.Itoa(s.FailureThreshold)+" failures")
		}
		if exp := h.svc.ExpectedAt(m, c.project.Timezone); exp != nil && !m.Paused {
			parts = append(parts, "due "+view.In(*exp, c.now))
		}
		d.Summary = strings.Join(parts, " · ")
	}
	events, err := h.svc.EventsSince(ctx, c.scope, m, c.now.Add(-24*time.Hour))
	if err != nil {
		return nil, err
	}
	d.Cells = view.HourCells(c.now, m.CreatedAt, m.State, events)
	d.Legend = []string{"24 h ago", view.UpShare(d.Cells), "now"}
	obs, err := h.svc.ListObservations(ctx, c.scope, m.Slug, service.ObservationPage{Limit: 20})
	if err != nil {
		return nil, err
	}
	for _, o := range obs {
		d.Observations = append(d.Observations, obsRowFor(o, loc))
	}
	recent, err := h.svc.ListEvents(ctx, c.scope, m.Slug, 20)
	if err != nil {
		return nil, err
	}
	for _, e := range recent {
		text := string(e.From) + " → " + string(e.To)
		if e.Reason != "" {
			text += " · " + e.Reason
		}
		d.Events = append(d.Events, eventRow{State: string(e.To), Ago: view.Ago(e.At, c.now), Abs: view.Abs(e.At, loc), Text: text})
	}
	return d, nil
}

func obsRowFor(o *domain.Observation, loc *time.Location) obsRow {
	row := obsRow{Clock: view.Clock(o.At, loc), Abs: view.Abs(o.At, loc)}
	switch {
	case o.Signal == domain.SignalStart:
		row.State, row.Text = "new", "start"
	case o.Signal == domain.SignalLog:
		row.State, row.Text = "new", "log"
	case o.OK:
		row.State, row.Text = "up", "ok"
	case o.Signal == domain.SignalExit && o.ExitCode != nil:
		row.State, row.Text = "down", "exit "+strconv.FormatInt(*o.ExitCode, 10)
	default:
		row.State, row.Text = "down", "fail"
	}
	if r, ok := o.Detail["reason"].(string); ok && r != "" {
		row.Text += " · " + strings.ReplaceAll(r, "_", " ")
	}
	if msg, ok := o.Detail["msg"].(string); ok && msg != "" {
		if len(msg) > 80 {
			msg = msg[:80] + "…"
		}
		row.Text += " · " + msg
	}
	if o.DurationMs != nil {
		row.Right = view.RunDuration(*o.DurationMs)
	} else if o.HasBody {
		row.Right = "body"
	}
	return row
}

func (h *Web) monitor(c *reqCtx) error {
	m, err := h.svc.MonitorBySlug(c.r.Context(), c.scope, c.r.PathValue("slug"))
	if err != nil {
		return err
	}
	dd, err := h.drawerData(c, m)
	if err != nil {
		return err
	}
	if c.htmx() {
		return h.render(c, http.StatusOK, "monitors", "drawer", dd)
	}
	d, err := h.listData(c)
	if err != nil {
		return err
	}
	d.Title = m.Name
	d.Drawer = dd
	return h.render(c, http.StatusOK, "monitors", "layout", d)
}

func (h *Web) pauseMonitor(c *reqCtx) error {
	_, err := h.svc.PauseMonitor(c.r.Context(), c.scope, c.r.PathValue("slug"))
	if err != nil {
		return err
	}
	return h.afterAction(c)
}

func (h *Web) resumeMonitor(c *reqCtx) error {
	_, err := h.svc.ResumeMonitor(c.r.Context(), c.scope, c.r.PathValue("slug"))
	if err != nil {
		return err
	}
	return h.afterAction(c)
}

// afterAction re-renders the drawer for htmx or redirects to the monitor.
func (h *Web) afterAction(c *reqCtx) error {
	path := c.projectPath() + "/m/" + c.r.PathValue("slug")
	if c.htmx() {
		m, err := h.svc.MonitorBySlug(c.r.Context(), c.scope, c.r.PathValue("slug"))
		if err != nil {
			return err
		}
		dd, err := h.drawerData(c, m)
		if err != nil {
			return err
		}
		return h.render(c, http.StatusOK, "monitors", "drawer", dd)
	}
	http.Redirect(c.w, c.r, path, http.StatusSeeOther)
	return nil
}

func (h *Web) deleteMonitor(c *reqCtx) error {
	if err := h.svc.DeleteMonitor(c.r.Context(), c.scope, c.r.PathValue("slug")); err != nil {
		return err
	}
	return h.redirect(c, c.projectPath())
}

// --- create and edit form ---------------------------------------------------

type formData struct {
	Edit            bool
	Action          string
	CancelPath      string
	CancelHX        string
	CSRF            string
	ProjectTimezone string
	SubmitLabel     string
	Values          map[string]string
	Errors          map[string]string
	Error           string
	AdvancedOpen    bool
}

type formPage struct {
	base
	List monitorsData
	Form formData
}

func (h *Web) blankForm(c *reqCtx) formData {
	return formData{
		Action: c.projectPath() + "/m/new", CancelPath: c.projectPath(), CancelHX: c.projectPath() + "?partial=drawer-empty", CSRF: c.csrf(),
		ProjectTimezone: c.project.Timezone, SubmitLabel: "Create monitor", Values: map[string]string{"grace": "5m"}, Errors: map[string]string{},
	}
}

func formFromMonitor(c *reqCtx, m *domain.Monitor) formData {
	f := formData{
		Edit: true, Action: c.projectPath() + "/m/" + m.Slug + "/edit", CancelPath: c.projectPath() + "/m/" + m.Slug, CancelHX: c.projectPath() + "/m/" + m.Slug,
		CSRF: c.csrf(), ProjectTimezone: c.project.Timezone, SubmitLabel: "Save", Values: map[string]string{}, Errors: map[string]string{},
	}
	f.Values["name"], f.Values["slug"], f.Values["tags"] = m.Name, m.Slug, strings.Join(m.Tags, ", ")
	if s := m.Heartbeat; s != nil {
		if s.Schedule.Period != 0 {
			f.Values["period"] = s.Schedule.Period.String()
		}
		f.Values["cron"], f.Values["timezone"], f.Values["grace"] = s.Schedule.Cron, s.Timezone, s.Grace.String()
		if s.MaxRuntime != 0 {
			f.Values["max_runtime"] = s.MaxRuntime.String()
		}
		f.Values["failure_threshold"] = strconv.Itoa(s.FailureThreshold)
		f.Values["recovery_threshold"] = strconv.Itoa(s.RecoveryThreshold)
		f.Values["methods"] = strings.Join(s.Methods, ", ")
		if s.BodyLimit != 0 {
			f.Values["body_limit"] = strconv.FormatInt(s.BodyLimit, 10)
		}
		f.AdvancedOpen = s.MaxRuntime != 0 || s.FailureThreshold != 1 || s.RecoveryThreshold != 1 || len(s.Methods) > 0 || s.BodyLimit != 0
	}
	return f
}

// parseMonitorForm turns posted values into a monitor, collecting parse
// errors per field.
func parseMonitorForm(r *http.Request, f *formData) *domain.Monitor {
	get := func(k string) string {
		v := strings.TrimSpace(r.PostFormValue(k))
		f.Values[k] = v
		return v
	}
	m := &domain.Monitor{Kind: domain.KindHeartbeat, Name: get("name"), Slug: get("slug")}
	if m.Slug == "" && m.Name != "" {
		m.Slug = domain.Slugify(m.Name)
	}
	spec := &domain.HeartbeatSpec{}
	if v := get("period"); v != "" {
		d, err := domain.ParseDuration(v)
		if err != nil {
			f.Errors["period"] = "Use a duration such as 1h or 1d."
		}
		spec.Schedule.Period = d
	}
	spec.Schedule.Cron = get("cron")
	spec.Timezone = get("timezone")
	if v := get("grace"); v != "" {
		d, err := domain.ParseDuration(v)
		if err != nil {
			f.Errors["grace"] = "Use a duration such as 5m or 1h."
		}
		spec.Grace = d
	}
	if v := get("tags"); v != "" {
		m.Tags = domain.NormalizeTags(strings.Split(v, ","))
	}
	if v := get("failure_threshold"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			f.Errors["failure_threshold"] = "Must be a whole number."
		}
		spec.FailureThreshold = n
	}
	if v := get("recovery_threshold"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			f.Errors["recovery_threshold"] = "Must be a whole number."
		}
		spec.RecoveryThreshold = n
	}
	if v := get("max_runtime"); v != "" {
		d, err := domain.ParseDuration(v)
		if err != nil {
			f.Errors["max_runtime"] = "Use a duration such as 2h."
		}
		spec.MaxRuntime = d
	}
	if v := get("methods"); v != "" {
		for _, mth := range strings.Split(v, ",") {
			if mth = strings.TrimSpace(mth); mth != "" {
				spec.Methods = append(spec.Methods, strings.ToUpper(mth))
			}
		}
	}
	if v := get("body_limit"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			f.Errors["body_limit"] = "Must be a whole number of bytes."
		}
		spec.BodyLimit = n
	}
	m.Heartbeat = spec
	if len(f.Errors) > 0 {
		f.AdvancedOpen = f.Errors["failure_threshold"] != "" || f.Errors["recovery_threshold"] != "" || f.Errors["max_runtime"] != "" || f.Errors["methods"] != "" || f.Errors["body_limit"] != ""
	}
	return m
}

// applyValidation maps service field errors onto form fields.
func applyValidation(f *formData, err error) bool {
	ve, ok := domain.AsValidation(err)
	if !ok {
		return false
	}
	for _, fe := range ve.Errors {
		field := fe.Field
		switch field {
		case "schedule":
			field = "period"
			if f.Values["cron"] != "" {
				field = "cron"
			}
		case "spec":
			field = "period"
		}
		if _, exists := f.Errors[field]; !exists {
			f.Errors[field] = capitalise(fe.Msg) + "."
		}
		if field == "failure_threshold" || field == "recovery_threshold" || field == "max_runtime" || field == "methods" || field == "body_limit" {
			f.AdvancedOpen = true
		}
	}
	return true
}

func capitalise(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func (h *Web) renderForm(c *reqCtx, status int, f formData) error {
	if c.htmx() {
		return h.render(c, status, "monitor_form", "monitor-form", f)
	}
	d, err := h.listData(c)
	if err != nil {
		return err
	}
	title := "Create monitor"
	if f.Edit {
		title = "Edit " + f.Values["name"]
	}
	page := formPage{base: h.baseFor(c, title), List: d, Form: f}
	return h.render(c, status, "monitor_form", "layout", page)
}

func (h *Web) newMonitor(c *reqCtx) error {
	if !c.scope.CanEdit() {
		return domain.ErrForbidden
	}
	return h.renderForm(c, http.StatusOK, h.blankForm(c))
}

func (h *Web) createMonitor(c *reqCtx) error {
	if err := c.r.ParseForm(); err != nil {
		return err
	}
	f := h.blankForm(c)
	m := parseMonitorForm(c.r, &f)
	if len(f.Errors) > 0 {
		return h.renderForm(c, http.StatusUnprocessableEntity, f)
	}
	created, err := h.svc.CreateMonitor(c.r.Context(), c.scope, m)
	if err != nil {
		if applyValidation(&f, err) {
			return h.renderForm(c, http.StatusUnprocessableEntity, f)
		}
		if errors.Is(err, domain.ErrConflict) {
			f.Errors["slug"] = "A monitor with this slug exists."
			return h.renderForm(c, http.StatusUnprocessableEntity, f)
		}
		return err
	}
	return h.redirect(c, c.projectPath()+"/m/"+created.Slug)
}

func (h *Web) editMonitor(c *reqCtx) error {
	if !c.scope.CanEdit() {
		return domain.ErrForbidden
	}
	m, err := h.svc.MonitorBySlug(c.r.Context(), c.scope, c.r.PathValue("slug"))
	if err != nil {
		return err
	}
	return h.renderForm(c, http.StatusOK, formFromMonitor(c, m))
}

func (h *Web) updateMonitor(c *reqCtx) error {
	if err := c.r.ParseForm(); err != nil {
		return err
	}
	cur, err := h.svc.MonitorBySlug(c.r.Context(), c.scope, c.r.PathValue("slug"))
	if err != nil {
		return err
	}
	f := formFromMonitor(c, cur)
	m := parseMonitorForm(c.r, &f)
	m.Slug = cur.Slug
	f.Values["slug"] = cur.Slug
	if len(f.Errors) > 0 {
		return h.renderForm(c, http.StatusUnprocessableEntity, f)
	}
	if _, err := h.svc.UpdateMonitor(c.r.Context(), c.scope, cur.Slug, m); err != nil {
		if applyValidation(&f, err) {
			return h.renderForm(c, http.StatusUnprocessableEntity, f)
		}
		return err
	}
	return h.redirect(c, c.projectPath()+"/m/"+cur.Slug)
}
