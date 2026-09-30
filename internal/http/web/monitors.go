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

func (h *Web) pingKeyFor(c *reqCtx) string {
	if c.scope.CanSeePingKey() {
		return c.project.PingKey
	}
	return "<ping key>"
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
	d := monitorsData{base: h.baseFor(c, "Monitors", "monitors"), Total: len(all), PingBase: h.pingBase(), PingKey: h.pingKeyFor(c), NewPath: c.projectPath() + "/m/new"}
	d.Query, d.FilterState, d.FilterTag = filter.Query, string(filter.State), filter.Tag
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
	for _, t := range sortedTags(tagCounts) {
		pressed := filter.Tag == t
		value := t
		if pressed {
			value = ""
		}
		d.Chips = append(d.Chips, ui.ChipProps{Label: t, Count: ui.Count(tagCounts[t]), Pressed: pressed, Type: "submit", Attrs: ui.Attr("name", "tag") + ui.Attr("value", value)})
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

func sortedTags(counts map[string]int) []string {
	tags := make([]string, 0, len(counts))
	for t := range counts {
		tags = append(tags, t)
	}
	sort.Slice(tags, func(i, j int) bool {
		if counts[tags[i]] != counts[tags[j]] {
			return counts[tags[i]] > counts[tags[j]]
		}
		return tags[i] < tags[j]
	})
	return tags
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
	if m != nil && m.Heartbeat != nil {
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
		if m.Pull != nil {
			row.Last = "no checks yet"
		}
	} else {
		row.Last = view.Ago(*m.LastObsAt, c.now)
		row.LastAbs = view.Abs(*m.LastObsAt, loc)
		if last, err := h.svc.ListObservations(c.r.Context(), c.scope, m.Slug, service.ObservationPage{Limit: 1}); err == nil && len(last) == 1 {
			row.Last += lastDatum(last[0])
		}
	}
	expected := h.svc.ExpectedAt(m, c.project.Timezone)
	word := "due"
	if m.Pull != nil {
		expected, word = m.NextDueAt, "next"
	}
	switch m.State {
	case domain.StatePaused:
		row.Next = "paused " + view.Ago(m.StateSince, c.now)
	case domain.StateDown:
		row.Next = "down " + view.For(m.StateSince, c.now)
	default:
		if expected != nil {
			row.Next = word + " " + view.In(*expected, c.now)
		}
	}
	return row
}

// lastDatum is the one datum that matters about the last observation.
func lastDatum(o *domain.Observation) string {
	switch {
	case o.LatencyMs != nil:
		return " · " + view.RunDuration(*o.LatencyMs)
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
	Pull                    bool
	Target                  string
	Cells, Legend           []string
	Observations            []obsRow
	Events                  []eventRow
	YAML                    string
}

type obsRow struct{ State, Clock, Abs, Text, Right string }

type eventRow struct{ State, Ago, Abs, Text string }

func (h *Web) drawerData(c *reqCtx, m *domain.Monitor) (*drawerData, error) {
	ctx := c.r.Context()
	loc := h.location(c, m)
	d := &drawerData{
		Path: c.projectPath() + "/m/" + m.Slug, ProjectPath: c.projectPath(), CSRF: c.csrf(), Name: m.Name, Slug: m.Slug,
		Badge: ui.StateBadgeProps{State: string(m.State), Pill: true}, Tags: m.Tags, Paused: m.Paused, PingBase: h.pingBase(), PingKey: h.pingKeyFor(c),
		YAML: domain.MonitorYAML(m),
	}
	if m.State != domain.StateNew {
		d.Badge.Since = view.For(m.StateSince, c.now)
	}
	if s := m.Pull; s != nil {
		d.Pull = true
		d.Target = s.Target()
		if s.HTTP != nil {
			d.Target = s.HTTP.Method + " " + s.HTTP.URL
		}
		parts := []string{"every " + view.Span(s.Interval.Std()), "timeout " + view.Span(s.Timeout.Std()), "down after " + strconv.Itoa(s.FailureThreshold) + " failures"}
		if s.Confirm.Retries > 0 {
			parts = append(parts, "confirm "+strconv.Itoa(s.Confirm.Retries)+"×")
		}
		if m.NextDueAt != nil && !m.Paused {
			parts = append(parts, "next "+view.In(*m.NextDueAt, c.now))
		}
		d.Summary = strings.Join(parts, " · ")
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
	if o.LatencyMs != nil {
		return checkRow(o, row)
	}
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

// checkRow renders one pull attempt: the reason or the status, and the
// latency; an attempt inside a confirm sequence shows as confirming.
func checkRow(o *domain.Observation, row obsRow) obsRow {
	row.Right = view.RunDuration(*o.LatencyMs)
	reason, _ := o.Detail["reason"].(string)
	switch {
	case o.OK && o.Detail["warn"] == true:
		row.State, row.Text = "late", reason
	case o.OK:
		row.State, row.Text = "up", "ok"
		if st, ok := o.Detail["status"].(float64); ok {
			row.Text = strconv.Itoa(int(st)) + " ok"
		}
	default:
		row.State, row.Text = "down", reason
		if row.Text == "" {
			row.Text = "fail"
		}
		if n, ok := o.Detail["attempt"].(float64); ok {
			if total, ok := o.Detail["attempts"].(float64); ok && int(n) < int(total) {
				row.State = "late"
				row.Text += " · confirming (" + strconv.Itoa(int(n)) + " of " + strconv.Itoa(int(total)) + ")"
			}
		}
	}
	return row
}

func (h *Web) checkMonitor(c *reqCtx) error {
	if _, err := h.svc.CheckNow(c.r.Context(), c.scope, c.r.PathValue("slug")); err != nil {
		return err
	}
	return h.afterAction(c)
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
	if _, err := h.svc.PauseMonitor(c.r.Context(), c.scope, c.r.PathValue("slug")); err != nil {
		return err
	}
	return h.afterAction(c)
}

func (h *Web) resumeMonitor(c *reqCtx) error {
	if _, err := h.svc.ResumeMonitor(c.r.Context(), c.scope, c.r.PathValue("slug")); err != nil {
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
	Edit                bool
	Kind                string
	Action              string
	PreviewPath         string
	KindPath            string
	CancelPath          string
	CancelHX            string
	DeletePath          string
	CSRF                string
	OrgSlug             string
	ProjectSlug         string
	PingBase            string
	PingKey             string
	SubmitLabel         string
	Values              map[string]string
	Errors              map[string]string
	Error               string
	Timezones           []ui.Option
	SlugHint            string
	SchedulePlaceholder string
	ScheduleHint        string
	GraceHint           string
	AdvancedSummary     string
	AdvancedOpen        bool
	YAMLOpen            bool
	YAML                string
}

type formPage struct {
	base
	List monitorsData
	Form formData
}

var commonZones = []string{"UTC", "Europe/Amsterdam", "Europe/London", "Europe/Berlin", "Europe/Paris", "America/New_York", "America/Chicago", "America/Los_Angeles", "Asia/Tokyo", "Asia/Singapore", "Australia/Sydney"}

func (h *Web) timezoneOptions(c *reqCtx, current string) []ui.Option {
	out := []ui.Option{{Value: "", Label: c.project.Timezone + " (project)"}}
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

func (h *Web) newForm(c *reqCtx, kind string) formData {
	if kind == "" {
		kind = string(domain.KindHeartbeat)
	}
	f := formData{
		Kind: kind, Action: c.projectPath() + "/m/new", PreviewPath: c.projectPath() + "/m/preview", KindPath: c.projectPath() + "/m/new",
		CancelPath: c.projectPath(), CancelHX: c.projectPath() + "?partial=drawer-empty", CSRF: c.csrf(),
		OrgSlug: c.org.Slug, ProjectSlug: c.project.Slug, PingBase: h.pingBase(), PingKey: h.pingKeyFor(c), SubmitLabel: "Create monitor",
		Values: map[string]string{"name": "", "slug": "", "schedule": "", "schedule_type": "period", "timezone": "", "grace": "5m", "tags": "", "max_runtime": "", "methods": "any", "failure_threshold": "1", "recovery_threshold": "1", "body_limit": ""},
		Errors: map[string]string{}, SlugHint: "Part of the ping URL. Empty derives it from the name.", YAMLOpen: true,
	}
	f.Timezones = h.timezoneOptions(c, "")
	return f
}

func (h *Web) formFromMonitor(c *reqCtx, m *domain.Monitor) formData {
	f := h.newForm(c, string(m.Kind))
	f.Edit = true
	f.Action = c.projectPath() + "/m/" + m.Slug + "/edit"
	f.PreviewPath = c.projectPath() + "/m/" + m.Slug + "/preview"
	f.KindPath = f.Action
	f.CancelPath = c.projectPath() + "/m/" + m.Slug
	f.CancelHX = f.CancelPath
	f.DeletePath = c.projectPath() + "/m/" + m.Slug + "/delete"
	f.SubmitLabel = "Save"
	f.SlugHint = "Part of the ping URL; it cannot change."
	f.YAMLOpen = false
	f.Values["name"], f.Values["slug"], f.Values["tags"] = m.Name, m.Slug, strings.Join(m.Tags, ", ")
	if s := m.Heartbeat; s != nil {
		if s.Schedule.Cron != "" {
			f.Values["schedule_type"], f.Values["schedule"] = "cron", s.Schedule.Cron
		} else {
			f.Values["schedule_type"], f.Values["schedule"] = "period", s.Schedule.Period.String()
		}
		f.Values["timezone"], f.Values["grace"] = s.Timezone, s.Grace.String()
		if s.MaxRuntime != 0 {
			f.Values["max_runtime"] = s.MaxRuntime.String()
		}
		f.Values["failure_threshold"] = strconv.Itoa(s.FailureThreshold)
		f.Values["recovery_threshold"] = strconv.Itoa(s.RecoveryThreshold)
		if len(s.Methods) == 1 && s.Methods[0] == "POST" {
			f.Values["methods"] = "post"
		}
		if s.BodyLimit != 0 {
			f.Values["body_limit"] = bytesWord(s.BodyLimit)
		}
		f.AdvancedOpen = s.MaxRuntime != 0 || s.FailureThreshold != 1 || s.RecoveryThreshold != 1 || len(s.Methods) > 0 || s.BodyLimit != 0
	}
	f.Timezones = h.timezoneOptions(c, f.Values["timezone"])
	return f
}

// parseMonitorForm turns form values (post or query) into a monitor,
// collecting parse errors per field.
func parseMonitorForm(values map[string][]string, f *formData) *domain.Monitor {
	get := func(k string) string {
		v := ""
		if vs := values[k]; len(vs) > 0 {
			v = strings.TrimSpace(vs[0])
		}
		f.Values[k] = v
		return v
	}
	kind := get("kind")
	if kind == "" {
		kind = string(domain.KindHeartbeat)
	}
	f.Kind = kind
	m := &domain.Monitor{Kind: domain.Kind(kind), Name: get("name"), Slug: get("slug")}
	if !m.Kind.Valid() {
		f.Errors["kind"] = "Unknown kind."
	} else if m.Kind != domain.KindHeartbeat {
		f.Errors["kind"] = "HTTP, TCP, DNS, TLS and ICMP monitors arrive later in phase 1."
	}
	if m.Slug == "" && m.Name != "" {
		m.Slug = domain.Slugify(m.Name)
		f.Values["slug"] = m.Slug
	}
	spec := &domain.HeartbeatSpec{}
	scheduleType := get("schedule_type")
	if scheduleType == "" {
		scheduleType = "period"
		f.Values["schedule_type"] = scheduleType
	}
	schedule := get("schedule")
	switch scheduleType {
	case "period":
		f.SchedulePlaceholder = "1h"
		if schedule != "" {
			d, err := domain.ParseDuration(schedule)
			if err != nil {
				f.Errors["schedule"] = "Use a duration such as 1h or 1d."
			}
			spec.Schedule.Period = d
		}
	case "cron":
		f.SchedulePlaceholder = "0 3 * * *"
		spec.Schedule.Cron = schedule
	default:
		f.SchedulePlaceholder = "Mon..Fri 09:00"
		f.Errors["schedule"] = "OnCalendar schedules arrive later in phase 1; use Period or Cron."
	}
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
	if v := get("max_runtime"); v != "" && v != "none" {
		d, err := domain.ParseDuration(v)
		if err != nil {
			f.Errors["max_runtime"] = "Use a duration such as 2h."
		}
		spec.MaxRuntime = d
	}
	if get("methods") == "post" {
		spec.Methods = []string{"POST"}
	} else {
		f.Values["methods"] = "any"
	}
	for _, k := range []string{"failure_threshold", "recovery_threshold"} {
		if v := get(k); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				f.Errors[k] = "Must be a whole number."
			}
			if k == "failure_threshold" {
				spec.FailureThreshold = n
			} else {
				spec.RecoveryThreshold = n
			}
		}
	}
	if v := get("body_limit"); v != "" {
		n, ok := parseBytes(v)
		if !ok {
			f.Errors["body_limit"] = "Use a size such as 64 KB."
		}
		spec.BodyLimit = n
	}
	m.Heartbeat = spec
	f.AdvancedOpen = f.AdvancedOpen || f.Errors["failure_threshold"] != "" || f.Errors["recovery_threshold"] != "" || f.Errors["max_runtime"] != "" || f.Errors["methods"] != "" || f.Errors["body_limit"] != ""
	return m
}

// fillHints computes the sentences and the YAML for the current values.
func (h *Web) fillHints(c *reqCtx, f *formData, m *domain.Monitor) {
	spec := m.Heartbeat
	if spec != nil {
		cp := *spec
		cp.Normalize()
		spec = &cp
	}
	hs := describe(spec, c.project.Timezone, c.now, h.svc.Config().BodyLimit)
	f.ScheduleHint, f.GraceHint, f.AdvancedSummary = hs.Schedule, hs.Grace, hs.Advanced
	if f.Errors["schedule"] == "" && f.Values["schedule"] == "" {
		f.ScheduleHint = "How often a ping is expected: a period such as 1h, or a cron expression."
	}
	preview := *m
	preview.Heartbeat = spec
	if preview.Slug == "" {
		preview.Slug = "slug"
	}
	f.YAML = domain.MonitorYAML(&preview)
}

// applyValidation maps service field errors onto form fields.
func applyValidation(f *formData, err error) bool {
	ve, ok := domain.AsValidation(err)
	if !ok {
		return false
	}
	for _, fe := range ve.Errors {
		field := fe.Field
		if field == "spec" {
			field = "schedule"
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
	title := "New monitor"
	if f.Edit {
		title = "Edit " + f.Values["name"]
	}
	page := formPage{base: d.base, List: d, Form: f}
	page.Title = title
	return h.render(c, status, "monitor_form", "layout", page)
}

// newMonitor renders the create form; query values prefill it, which is
// how the kind switch keeps Name, Slug and Tags.
func (h *Web) newMonitor(c *reqCtx) error {
	if !c.scope.CanEdit() {
		return domain.ErrForbidden
	}
	f := h.newForm(c, c.r.URL.Query().Get("kind"))
	q := c.r.URL.Query()
	if len(q) > 0 && (q.Has("name") || q.Has("kind")) {
		m := parseMonitorForm(q, &f)
		delete(f.Errors, "schedule")
		if f.Values["grace"] == "" {
			f.Values["grace"] = "5m"
		}
		h.fillHints(c, &f, m)
	} else {
		h.fillHints(c, &f, &domain.Monitor{Kind: domain.KindHeartbeat, Heartbeat: &domain.HeartbeatSpec{Schedule: domain.Schedule{Period: domain.MustDuration("1h")}}})
		f.YAML = "slug: <slug>\nkind: heartbeat\nschedule: {period: 1h}"
	}
	return h.renderForm(c, http.StatusOK, f)
}

func (h *Web) createMonitor(c *reqCtx) error {
	if err := c.r.ParseForm(); err != nil {
		return err
	}
	f := h.newForm(c, "")
	m := parseMonitorForm(c.r.PostForm, &f)
	h.fillHints(c, &f, m)
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
	f := h.formFromMonitor(c, m)
	h.fillHints(c, &f, m)
	return h.renderForm(c, http.StatusOK, f)
}

func (h *Web) updateMonitor(c *reqCtx) error {
	if err := c.r.ParseForm(); err != nil {
		return err
	}
	cur, err := h.svc.MonitorBySlug(c.r.Context(), c.scope, c.r.PathValue("slug"))
	if err != nil {
		return err
	}
	f := h.formFromMonitor(c, cur)
	m := parseMonitorForm(c.r.PostForm, &f)
	m.Slug, m.Kind = cur.Slug, cur.Kind
	delete(f.Errors, "kind")
	f.Values["slug"], f.Kind = cur.Slug, string(cur.Kind)
	h.fillHints(c, &f, m)
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

// previewMonitor validates the posted form without saving and returns the
// hint sentences, the Advanced summary and the YAML as partials.
func (h *Web) previewMonitor(c *reqCtx) error {
	if err := c.r.ParseForm(); err != nil {
		return err
	}
	f := h.newForm(c, "")
	m := parseMonitorForm(c.r.PostForm, &f)
	if slug := c.r.PathValue("slug"); slug != "" {
		m.Slug = slug
	}
	h.fillHints(c, &f, m)
	if f.Errors["schedule"] != "" {
		f.ScheduleHint = f.Errors["schedule"]
	}
	if f.Errors["grace"] != "" {
		f.GraceHint = f.Errors["grace"]
	}
	return h.render(c, http.StatusOK, "monitor_form", "preview", f)
}
