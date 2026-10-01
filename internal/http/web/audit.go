package web

import (
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/http/web/ui"
	"github.com/w4jnl/vink/internal/service"
	"github.com/w4jnl/vink/internal/timefmt"
)

// The audit log: /o/{org}/admin/audit for every member of the org
// (members and viewers see their projects' rows only) and /admin/audit
// for instance admins across orgs. Both render the same component; the
// filters are query parameters so a filtered view is a link.

var auditPeriods = []struct {
	key, label string
	span       time.Duration
}{{"24h", "24 h", 24 * time.Hour}, {"7d", "7 d", 7 * 24 * time.Hour}, {"30d", "30 d", 30 * 24 * time.Hour}}

type auditView struct {
	Path   string
	Kinds  string
	Chips  []ui.ChipProps
	Select ui.HTML
	Period ui.HTML
	Groups []auditDay
	Older  string
	Empty  string
}

type auditDay struct {
	Label, Date string
	Rows        []ui.HTML
}

// auditQuery is the parsed filter: kinds, project, actor, period, cursor.
type auditQuery struct {
	kinds   []string
	org     string
	project string
	actor   string
	period  string
	before  string
}

func parseAuditQuery(q url.Values) auditQuery {
	a := auditQuery{org: q.Get("org"), project: q.Get("project"), actor: q.Get("actor"), period: q.Get("period"), before: q.Get("before")}
	// the hidden input carries the current set and a pressed chip the next
	// one; the last value wins
	if vals := q["kind"]; len(vals) > 0 {
		for _, k := range strings.Split(vals[len(vals)-1], ",") {
			switch k {
			case "changes", "access", "state":
				a.kinds = append(a.kinds, k)
			}
		}
	}
	if _, ok := auditPeriod(a.period); !ok {
		a.period = "7d"
	}
	return a
}

func auditPeriod(key string) (time.Duration, bool) {
	for _, p := range auditPeriods {
		if p.key == key {
			return p.span, true
		}
	}
	return 0, false
}

func (a auditQuery) has(kind string) bool {
	for _, k := range a.kinds {
		if k == kind {
			return true
		}
	}
	return false
}

// toggled returns the kind set after pressing one chip, comma-joined.
func (a auditQuery) toggled(kind string) string {
	var out []string
	for _, k := range []string{"changes", "access", "state"} {
		if (k == kind) != a.has(k) {
			out = append(out, k)
		}
	}
	return strings.Join(out, ",")
}

// values are the filters as query parameters, without the cursor.
func (a auditQuery) values() url.Values {
	v := url.Values{}
	if len(a.kinds) > 0 {
		v.Set("kind", strings.Join(a.kinds, ","))
	}
	for k, s := range map[string]string{"org": a.org, "project": a.project, "actor": a.actor} {
		if s != "" {
			v.Set(k, s)
		}
	}
	if a.period != "7d" {
		v.Set("period", a.period)
	}
	return v
}

// auditOpts is what differs between the org and the instance page.
type auditOpts struct {
	path     string
	scope    domain.Scope
	orgs     []*domain.Org            // instance page: the org select
	projects []service.ProjectSummary // the project select, already limited to the org when there is one
	loc      *time.Location
	instance bool
}

func (h *Web) auditView(c *reqCtx, o auditOpts) (*auditView, error) {
	ctx := c.r.Context()
	q := parseAuditQuery(c.r.URL.Query())
	v := &auditView{Path: o.path, Kinds: strings.Join(q.kinds, ",")}
	f := service.AuditFilter{Actor: q.actor}
	span, _ := auditPeriod(q.period)
	f.Since = c.now.Add(-span)
	for _, k := range q.kinds {
		switch k {
		case "changes":
			f.Changes = true
		case "access":
			f.Access = true
		case "state":
			f.State = true
		}
	}
	orgSlugs := map[string]string{}
	for _, org := range o.orgs {
		orgSlugs[org.ID] = org.Slug
	}
	if q.org != "" {
		for _, org := range o.orgs {
			if org.Slug == q.org {
				f.OrgID = org.ID
			}
		}
		if f.OrgID == "" {
			return nil, domain.NotFound("org")
		}
	}
	projectSlugs := map[string]string{}
	var projectOpts []ui.Option
	for _, p := range o.projects {
		if f.OrgID != "" && p.OrgID != f.OrgID {
			continue
		}
		projectSlugs[p.ID] = p.Slug
		label := p.Slug
		if o.instance {
			label = p.OrgSlug + "/" + p.Slug
		}
		projectOpts = append(projectOpts, ui.Option{Value: p.Slug, Label: label})
		if q.project != "" && p.Slug == q.project && (f.OrgID == "" || p.OrgID == f.OrgID) && f.ProjectID == "" {
			f.ProjectID = p.ID
		}
	}
	if q.project != "" && f.ProjectID == "" {
		return nil, domain.NotFound("project")
	}
	if q.before != "" {
		at, id, ok := strings.Cut(q.before, ".")
		ms, err := strconv.ParseInt(at, 10, 64)
		if !ok || err != nil {
			return nil, domain.NotFound("page")
		}
		f.BeforeAt, f.BeforeID = domain.FromMillis(ms), id
	}

	counts, err := h.svc.AuditCounts(ctx, o.scope, f)
	if err != nil {
		return nil, err
	}
	actors, err := h.svc.AuditActors(ctx, o.scope, f)
	if err != nil {
		return nil, err
	}
	page, err := h.svc.AuditLog(ctx, o.scope, f)
	if err != nil {
		return nil, err
	}

	for _, k := range []struct {
		key string
		n   int
	}{{"changes", counts.Changes}, {"access", counts.Access}, {"state", counts.State}} {
		v.Chips = append(v.Chips, ui.ChipProps{Label: k.key, Count: ui.Count(k.n), Pressed: q.has(k.key), Type: "submit", Attrs: ui.Attr("name", "kind") + ui.Attr("value", q.toggled(k.key))})
	}
	var sel ui.HTML
	if o.instance {
		opts := []ui.Option{{Value: "", Label: "All orgs"}}
		for _, org := range o.orgs {
			opts = append(opts, ui.Option{Value: org.Slug, Label: org.Slug})
		}
		sel += ui.InlineSelect(ui.InlineSelectProps{Label: "Org", Name: "org", Options: opts, Value: q.org})
	}
	sel += ui.InlineSelect(ui.InlineSelectProps{Label: "Project", Name: "project", Options: append([]ui.Option{{Value: "", Label: "All projects"}}, projectOpts...), Value: q.project})
	who := []ui.Option{{Value: "", Label: "Anyone"}}
	seen := q.actor == ""
	for _, a := range actors {
		who = append(who, ui.Option{Value: a, Label: a})
		seen = seen || a == q.actor
	}
	if !seen {
		who = append(who, ui.Option{Value: q.actor, Label: q.actor})
	}
	sel += ui.InlineSelect(ui.InlineSelectProps{Label: "Who", Name: "actor", Options: who, Value: q.actor})
	v.Select = sel
	var periodOpts []ui.Option
	for _, p := range auditPeriods {
		periodOpts = append(periodOpts, ui.Option{Value: p.key, Label: p.label})
	}
	v.Period = ui.Segmented(ui.SegmentedProps{Name: "period", Label: "Period", Options: periodOpts, Value: []string{q.period}, Mono: true})

	// rows, grouped by day in the viewer's timezone
	loc := o.loc
	if loc == nil {
		loc = time.UTC
	}
	today := c.now.In(loc).Format("2006-01-02")
	yesterday := c.now.In(loc).AddDate(0, 0, -1).Format("2006-01-02")
	opened := q.before != ""
	for _, e := range page.Entries {
		day := e.At.In(loc)
		key := day.Format("2006-01-02")
		if len(v.Groups) == 0 || v.Groups[len(v.Groups)-1].Date != day.Format("Mon 2 Jan") {
			label := day.Format("Monday")
			switch key {
			case today:
				label = "Today"
			case yesterday:
				label = "Yesterday"
			}
			v.Groups = append(v.Groups, auditDay{Label: label, Date: day.Format("Mon 2 Jan")})
		}
		scope := ""
		switch {
		case e.ProjectID != "" && o.instance:
			scope = orgSlugs[e.OrgID] + "/" + projectSlugs[e.ProjectID]
		case e.ProjectID != "":
			scope = projectSlugs[e.ProjectID]
			if scope == "" {
				scope = "deleted project"
			}
		case e.OrgID != "":
			scope = orgSlugs[e.OrgID]
			if scope == "" && c.org != nil {
				scope = c.org.Slug
			}
		}
		row := ui.AuditRowProps{
			Time: day.Format("15:04"), TimeAbs: day.Format("Mon 2 Jan 15:04:05 MST"), Actor: e.Actor, ActorKind: e.ActorKind,
			Text: auditText(e, c.now), Scope: scope, Via: auditVia(e.Via),
		}
		if e.Source == "event" {
			row.State = strings.TrimPrefix(e.Action, "state.")
		}
		row.Diff, row.Meta = auditBody(e)
		// the newest row with something to show opens, as the kit draws it
		if !opened && (len(row.Diff) > 0 || len(row.Meta) > 0) {
			row.Open, opened = true, true
		}
		g := &v.Groups[len(v.Groups)-1]
		g.Rows = append(g.Rows, ui.AuditRow(row))
	}
	if page.More {
		next := q.values()
		next.Set("before", strconv.FormatInt(domain.Millis(page.NextAt), 10)+"."+page.NextID)
		v.Older = o.path + "?" + next.Encode()
	}
	if len(page.Entries) == 0 {
		v.Empty = "Nothing in the last " + strings.ReplaceAll(q.period, "h", " h")
		if strings.HasSuffix(q.period, "d") {
			v.Empty = "Nothing in the last " + strings.TrimSuffix(q.period, "d") + " days"
		}
		if q.period == "24h" {
			v.Empty = "Nothing in the last 24 hours"
		}
		if len(q.kinds) > 0 || q.actor != "" || q.project != "" {
			v.Empty += " with these filters"
		}
	}
	return v, nil
}

// auditVia renders the via column: agent:x as "agent x".
func auditVia(via string) string {
	if strings.HasPrefix(via, "agent:") {
		return "agent " + strings.TrimPrefix(via, "agent:")
	}
	return via
}

// auditBody picks the diff and the facts under a row.
func auditBody(e service.AuditEntry) ([]ui.DiffLine, [][2]string) {
	var diff []ui.DiffLine
	var meta [][2]string
	switch {
	case e.Before != "" || e.After != "":
		diff = diffLines(e.Before, e.After)
	case e.Action == "member.role":
		from, to := detailString(e.Detail, "from"), detailString(e.Detail, "to")
		if from != "" {
			diff = []ui.DiffLine{{Op: "-", Text: "role: " + from}, {Op: "+", Text: "role: " + to}}
		}
	}
	if e.Action == "apply" {
		for _, k := range []string{"created", "updated", "recreated", "deleted", "unchanged"} {
			if n := detailInt(e.Detail, k); n > 0 {
				meta = append(meta, [2]string{k, strconv.Itoa(n)})
			}
		}
		if detailBool(e.Detail, "prune") {
			meta = append(meta, [2]string{"prune", "on"})
		}
	}
	if e.RequestID != "" {
		meta = append(meta, [2]string{"request", e.RequestID})
	}
	if e.Remote != "" {
		meta = append(meta, [2]string{"from", e.Remote})
	}
	if len(diff) == 0 && e.Source == "audit" && len(meta) > 0 && e.RequestID == "" && e.Remote != "" && e.Action != "apply" {
		// a remote address alone is not worth a disclosure
		return nil, nil
	}
	return diff, meta
}

func detailString(d map[string]any, key string) string {
	if d == nil {
		return ""
	}
	switch v := d[key].(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	}
	return ""
}

func detailInt(d map[string]any, key string) int {
	if d == nil {
		return 0
	}
	if v, ok := d[key].(float64); ok {
		return int(v)
	}
	return 0
}

func detailBool(d map[string]any, key string) bool {
	if d == nil {
		return false
	}
	v, _ := d[key].(bool)
	return v
}

func detailList(d map[string]any, key string) []string {
	if d == nil {
		return nil
	}
	var out []string
	if list, ok := d[key].([]any); ok {
		for _, v := range list {
			if s, ok := v.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

// auditText turns an entry into the sentence of its row, the target in
// <code>. Unknown actions fall back to "<action> <target>".
func auditText(e service.AuditEntry, now time.Time) ui.HTML {
	code := func(s string) string { return "<code>" + esc(s) + "</code>" }
	target := code(e.Target)
	who := esc(e.Target)
	fields := strings.Join(detailList(e.Detail, "fields"), ", ")
	kind, verb, _ := strings.Cut(e.Action, ".")
	nouns := map[string]string{"monitor": "monitor", "channel": "channel", "route": "route", "page": "status page", "maintenance": "maintenance", "project": "project", "org": "org", "agent": "agent"}
	if noun, ok := nouns[kind]; ok {
		switch verb {
		case "create":
			if kind == "maintenance" {
				return ui.HTML("scheduled maintenance " + target)
			}
			if kind == "agent" {
				return ui.HTML("added agent " + target)
			}
			return ui.HTML("created " + noun + " " + target)
		case "update":
			if fields == "" {
				return ui.HTML("changed " + noun + " " + target)
			}
			return ui.HTML("changed " + noun + " " + target + ": " + esc(fields))
		case "delete":
			return ui.HTML("deleted " + noun + " " + target)
		case "pause":
			return ui.HTML("paused " + target)
		case "resume":
			return ui.HTML("resumed " + target)
		case "check":
			return ui.HTML("ran a check on " + target)
		case "end":
			return ui.HTML("ended maintenance " + target + " early")
		case "labels":
			return ui.HTML("changed the labels of agent " + target)
		case "revoke":
			return ui.HTML("revoked agent " + target)
		case "transfer":
			return ui.HTML("transferred ownership to " + who)
		}
	}
	switch e.Action {
	case "apply":
		var parts []string
		for _, k := range []string{"created", "updated", "recreated", "deleted"} {
			if n := detailInt(e.Detail, k); n > 0 {
				parts = append(parts, strconv.Itoa(n)+" "+k)
			}
		}
		if len(parts) == 0 {
			parts = []string{"nothing changed"}
		}
		if detailBool(e.Detail, "project_created") {
			parts = append([]string{"project created"}, parts...)
		}
		return ui.HTML("applied " + esc(e.Target) + ": " + esc(strings.Join(parts, ", ")))
	case "pingkey.rotate":
		if until, err := time.Parse(time.RFC3339, detailString(e.Detail, "old_key_until")); err == nil {
			return ui.HTML("rotated the ping key of " + target + "; the old one works until " + esc(timefmt.In(until, now)))
		}
		return ui.HTML("rotated the ping key of " + target)
	case "key.create", "orgkey.create":
		noun := "API key"
		if e.Action == "orgkey.create" {
			noun = "org key"
		}
		if access := detailString(e.Detail, "access"); access != "" {
			return ui.HTML("created " + noun + " " + target + " (" + esc(access) + ")")
		}
		return ui.HTML("created " + noun + " " + target)
	case "key.revoke":
		return ui.HTML("revoked API key " + target)
	case "orgkey.revoke":
		return ui.HTML("revoked org key " + target)
	case "member.role":
		from, to := detailString(e.Detail, "from"), detailString(e.Detail, "to")
		if from == "" {
			return ui.HTML("added " + who + " as " + esc(to))
		}
		return ui.HTML("changed the role of " + who + ": " + esc(from) + " → " + esc(to))
	case "member.remove":
		return ui.HTML("removed " + who)
	case "invite.create":
		return ui.HTML("invited " + who + " as " + esc(detailString(e.Detail, "role")))
	case "invite.accept":
		return ui.HTML("joined as " + esc(detailString(e.Detail, "subject")) + " (" + esc(detailString(e.Detail, "role")) + ") through the invite for " + who)
	case "invite.revoke":
		return ui.HTML("revoked the invite for " + who)
	case "incident.ack":
		return ui.HTML("acked the incident on " + target)
	case "user.create":
		if detailString(e.Detail, "invite") != "" {
			return ui.HTML("created the account " + who + " from an invite")
		}
		return ui.HTML("created the account " + who)
	case "user.password":
		if detailBool(e.Detail, "via_link") {
			return ui.HTML("set a new password through a reset link")
		}
		return ui.HTML("changed the password of " + who)
	case "user.instance_admin":
		if detailBool(e.Detail, "admin") {
			return ui.HTML("made " + who + " instance admin")
		}
		return ui.HTML("took instance admin from " + who)
	case "user.disable":
		return ui.HTML("disabled the account " + who)
	case "user.enable":
		return ui.HTML("enabled the account " + who)
	case "user.totp_reset":
		return ui.HTML("reset two-factor for " + who)
	case "user.reset_link":
		return ui.HTML("made a reset link for " + who)
	case "user.signin":
		s := "signed in " + esc(signInMethod(detailString(e.Detail, "method")))
		if detailString(e.Detail, "method") == "recovery" {
			s += "; " + strconv.Itoa(detailInt(e.Detail, "left")) + " left"
		}
		return ui.HTML(s)
	case "user.source":
		return ui.HTML("moved the account " + who + " from " + esc(detailString(e.Detail, "from")) + " to " + esc(detailString(e.Detail, "to")))
	case "user.totp_on":
		return ui.HTML("turned on two-factor sign-in")
	case "user.totp_off":
		return ui.HTML("turned off two-factor sign-in")
	case "user.recovery_codes":
		return ui.HTML("made a new set of recovery codes")
	case "user.profile":
		if fields != "" {
			return ui.HTML("changed " + esc(fields) + " of the account " + who)
		}
		return ui.HTML("changed the account " + who)
	case "user.signin_failed":
		return ui.HTML("failed to sign in as " + who + " " + esc(signInMethod(detailString(e.Detail, "method"))))
	case "user.signout":
		if n := detailInt(e.Detail, "others"); n > 0 {
			return ui.HTML("signed out " + strconv.Itoa(n) + " other " + pluralWord(n, "session"))
		}
		return ui.HTML("signed out")
	}
	if e.Source == "event" {
		reason := detailString(e.Detail, "reason")
		from := detailString(e.Detail, "from")
		var s string
		switch strings.TrimPrefix(e.Action, "state.") {
		case "down":
			s = target + " went down"
		case "up":
			if from == "new" {
				s = target + " is up"
			} else {
				s = target + " is up again"
			}
			reason = ""
		case "late":
			s = target + " is late"
		case "paused":
			s = target + " was paused"
			reason = ""
		case "new":
			s = target + " was reset"
			reason = ""
		default:
			s = target + " went " + esc(strings.TrimPrefix(e.Action, "state."))
		}
		if reason != "" {
			s += ": " + esc(reason)
		}
		return ui.HTML(s)
	}
	return ui.HTML(esc(strings.ReplaceAll(e.Action, ".", " ")) + " " + target)
}

func pluralWord(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

func signInMethod(m string) string {
	switch m {
	case "password":
		return "with a password"
	case "totp":
		return "with a code"
	case "recovery":
		return "with a recovery code"
	case "oidc":
		return "through OIDC"
	case "proxy":
		return "through the proxy"
	case "":
		return ""
	}
	return "with " + m
}

// diffLines is a line diff of two YAML snapshots: context, "-" and "+".
func diffLines(before, after string) []ui.DiffLine {
	a := splitLines(before)
	b := splitLines(after)
	var out []ui.DiffLine
	if len(a)*len(b) > 250_000 {
		for _, l := range a {
			out = append(out, ui.DiffLine{Op: "-", Text: l})
		}
		for _, l := range b {
			out = append(out, ui.DiffLine{Op: "+", Text: l})
		}
		return out
	}
	// longest common subsequence
	n, m := len(a), len(b)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			switch {
			case a[i] == b[j]:
				lcs[i][j] = lcs[i+1][j+1] + 1
			case lcs[i+1][j] >= lcs[i][j+1]:
				lcs[i][j] = lcs[i+1][j]
			default:
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			out = append(out, ui.DiffLine{Text: a[i]})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			out = append(out, ui.DiffLine{Op: "-", Text: a[i]})
			i++
		default:
			out = append(out, ui.DiffLine{Op: "+", Text: b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		out = append(out, ui.DiffLine{Op: "-", Text: a[i]})
	}
	for ; j < m; j++ {
		out = append(out, ui.DiffLine{Op: "+", Text: b[j]})
	}
	return out
}

func splitLines(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// --- handlers -------------------------------------------------------------

// orgAudit is the org's fourth settings tab, open to every member.
func (h *Web) orgAudit(c *reqCtx) error {
	d, err := h.adminData(c, "audit")
	if err != nil {
		return err
	}
	projects, err := h.svc.ProjectsForUser(c.r.Context(), c.principal.User.ID, c.principal.InstanceAdmin)
	if err != nil {
		return err
	}
	var mine []service.ProjectSummary
	for _, p := range projects {
		if p.OrgID == c.org.ID {
			mine = append(mine, p)
		}
	}
	sort.Slice(mine, func(i, j int) bool { return mine[i].Slug < mine[j].Slug })
	v, err := h.auditView(c, auditOpts{path: c.orgPath() + "/audit", scope: c.scope, orgs: []*domain.Org{c.org}, projects: mine, loc: h.orgLocation(c)})
	if err != nil {
		return err
	}
	d.Audit = v
	if c.htmx() {
		return h.render(c, http.StatusOK, "admin", "admin-audit", d)
	}
	return h.render(c, http.StatusOK, "admin", "layout", d)
}

// instanceAudit is the same list across orgs, for instance admins.
func (h *Web) instanceAudit(c *reqCtx) error {
	d, err := h.instanceData(c, "audit")
	if err != nil {
		return err
	}
	orgs, err := h.svc.ListOrgs(c.r.Context(), c.scope)
	if err != nil {
		return err
	}
	projects, err := h.svc.ProjectsForUser(c.r.Context(), c.principal.User.ID, true)
	if err != nil {
		return err
	}
	sort.Slice(projects, func(i, j int) bool {
		if projects[i].OrgSlug != projects[j].OrgSlug {
			return projects[i].OrgSlug < projects[j].OrgSlug
		}
		return projects[i].Slug < projects[j].Slug
	})
	v, err := h.auditView(c, auditOpts{path: "/admin/audit", scope: c.scope, orgs: orgs, projects: projects, instance: true})
	if err != nil {
		return err
	}
	d.Audit = v
	if c.htmx() {
		return h.render(c, http.StatusOK, "instance", "instance-audit", d)
	}
	return h.render(c, http.StatusOK, "instance", "layout", d)
}
