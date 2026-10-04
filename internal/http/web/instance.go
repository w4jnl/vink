package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/w4jnl/vink/internal/auth"
	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/http/web/ui"
	"github.com/w4jnl/vink/internal/http/web/view"
	"github.com/w4jnl/vink/internal/service"
	"github.com/w4jnl/vink/internal/version"
)

// Instance admin: /admin/{orgs|users|server}, instance admins only. Anyone
// else gets a 404, the way other tenants' pages do.

var instanceTabs = []ui.Tab{{ID: "orgs", Label: "Orgs"}, {ID: "users", Label: "Users"}, {ID: "server", Label: "Server"}, {ID: "audit", Label: "Audit log"}}

// ServerFacts are the read-only facts the Server tab shows; vink serve
// fills them from its config and loops.
type ServerFacts struct {
	Build    [][2]string
	Database [][2]string
	SignIn   [][2]string
	Network  [][2]string
	// LastBackup is zero when vink admin backup never ran.
	LastBackup time.Time
}

// SetServerFacts installs the Server tab's source.
func (h *Web) SetServerFacts(fn func(ctx context.Context) ServerFacts) { h.facts = fn }

// instanceAdmin requires an instance admin; others get a 404.
func (h *Web) instanceAdmin(fn handlerFn) http.Handler {
	return h.user(func(c *reqCtx) error {
		if !c.principal.InstanceAdmin {
			return domain.NotFound("page")
		}
		c.scope = domain.Scope{InstanceAdmin: true, UserID: c.principal.User.ID, Role: domain.RoleOwner, Actor: "user:" + c.principal.User.Subject}
		return fn(c)
	})
}

type instanceData struct {
	base
	Tab     string
	TabPath string
	Tabs    []ui.Tab
	Lede    ui.HTML
	Host    string
	Version string
	Flash   string
	Tone    string
	// orgs
	OrgPanel *orgPanel
	OrgRows  []orgRow
	// users
	Chips     ui.HTML
	UserRows  []userRow
	UserPanel *userPanel
	// server
	Backup *noteData
	Facts  *ServerFacts
	// audit
	Audit *auditView
}

type orgRow struct {
	Slug, Sub, Href string
	Muted           bool
	Cells           []ui.Cell
	Actions         ui.HTML
}

type orgPanel struct {
	Title, Action, CancelPath, CSRF, EditID string
	Values, Errors                          map[string]string
	DeleteAction                            ui.HTML
}

type userRow struct {
	ID        string
	Lead      string
	TitleHTML ui.HTML
	Sub       string
	Cells     []ui.Cell
	Actions   ui.HTML
	Muted     bool
}

type userPanel struct {
	ID, Title, Sub, Action, CancelPath, CSRF string
	IsAdmin, AdminLocked                     bool
	AdminHint                                string
	Local, TOTPOn, Disabled, Self            bool
	ResetTOTPPath, ResetLinkPath             string
	DisablePath, EnablePath                  string
	Link                                     *noteData
	Error                                    string
}

func (h *Web) instanceData(c *reqCtx, tab string) (instanceData, error) {
	d := instanceData{base: h.baseFor(c, "Instance", "none"), Tab: tab, TabPath: c.href("/admin/" + tab), Version: version.Version, Flash: c.r.URL.Query().Get("flash"), Tone: "ok"}
	if u, err := url.Parse(h.svc.Config().BaseURL); err == nil {
		d.Host = u.Host
	}
	ctx := c.r.Context()
	orgs, err := h.svc.ListOrgs(ctx, c.scope)
	if err != nil {
		return d, err
	}
	users, err := h.svc.ListInstanceUsers(ctx, c.scope)
	if err != nil {
		return d, err
	}
	for _, t := range instanceTabs {
		tab := ui.Tab{ID: t.ID, Label: t.Label, Href: c.href("/admin/" + t.ID)}
		switch t.ID {
		case "orgs":
			tab.Count = ui.Count(len(orgs))
		case "users":
			tab.Count = ui.Count(len(users))
		}
		d.Tabs = append(d.Tabs, tab)
	}
	switch tab {
	case "orgs":
		d.Lede = "Instance admins create orgs and set their quotas. Everything inside an org is up to its owners and admins."
	case "users":
		d.Lede = "Everyone who has signed in or accepted an invite. Roles are set in each org; here you make instance admins, reset local sign-ins and disable accounts."
	case "audit":
		d.Lede = "Everything that happened on this instance, across orgs: changes made by people and API keys, sign-ins and access changes, and every state flip."
	case "server":
		d.Lede = `Read-only. Change these in vink.toml and restart; <span class="vk-mono">vink serve --print-config</span> prints the effective config with secrets redacted.`
	}
	return d, nil
}

func (h *Web) instanceHome(c *reqCtx) error {
	http.Redirect(c.w, c.r, c.href("/admin/orgs"), http.StatusSeeOther)
	return nil
}

func (h *Web) instanceTab(c *reqCtx) error {
	switch c.r.PathValue("tab") {
	case "orgs":
		var panel *orgPanel
		q := c.r.URL.Query()
		switch {
		case q.Get("add") == "1":
			panel = h.newOrgPanel(c)
		case q.Get("edit") != "":
			org, err := h.svc.OrgBySlug(c.r.Context(), q.Get("edit"))
			if err != nil {
				return err
			}
			panel = h.editOrgPanel(c, org)
		}
		return h.orgsTab(c, http.StatusOK, panel)
	case "users":
		var panel *userPanel
		if id := c.r.URL.Query().Get("edit"); id != "" {
			p, err := h.userPanelFor(c, id, nil)
			if err != nil {
				return err
			}
			panel = p
		}
		return h.usersTab(c, http.StatusOK, panel)
	case "server":
		return h.serverTab(c)
	case "audit":
		return h.instanceAudit(c)
	}
	return domain.NotFound("page")
}

// --- orgs -----------------------------------------------------------------

func (h *Web) newOrgPanel(c *reqCtx) *orgPanel {
	return &orgPanel{Title: "Add org", Action: c.href("/admin/orgs"), CancelPath: c.href("/admin/orgs"), CSRF: c.csrf(), Values: map[string]string{}, Errors: map[string]string{}}
}

func (h *Web) editOrgPanel(c *reqCtx, org *domain.Org) *orgPanel {
	p := &orgPanel{Title: "Edit " + org.Slug, Action: c.href("/admin/orgs/" + org.Slug), CancelPath: c.href("/admin/orgs"), CSRF: c.csrf(), EditID: org.Slug,
		Values: map[string]string{"org_slug": org.Slug, "org_name": org.Name, "org_q_mon": quotaString(org.QuotaMonitors), "org_q_ag": quotaString(org.QuotaAgents)}, Errors: map[string]string{}}
	return p
}

func quotaString(q *int64) string {
	if q == nil {
		return ""
	}
	return strconv.FormatInt(*q, 10)
}

func parseQuota(s string) (*int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return nil, errors.New("a whole number, or empty for no quota")
	}
	return &n, nil
}

func (h *Web) orgsTab(c *reqCtx, status int, panel *orgPanel) error {
	d, err := h.instanceData(c, "orgs")
	if err != nil {
		return err
	}
	d.OrgPanel = panel
	rows, err := h.svc.OrgSummaries(c.r.Context(), c.scope)
	if err != nil {
		return err
	}
	for _, o := range rows {
		sub := plural(o.Projects, "project")
		if o.Projects == 0 {
			sub = "no projects"
		}
		switch len(o.Owners) {
		case 0:
			sub += " · no owner"
		case 1:
			sub += " · owner " + o.Owners[0]
		default:
			sub += " · owners " + strings.Join(o.Owners, ", ")
		}
		row := orgRow{Slug: o.Org.Slug, Sub: sub, Href: c.href("/o/" + o.Org.Slug + "/admin/members"), Muted: o.Projects == 0,
			Cells: []ui.Cell{{HTML: ui.Usage(o.Monitors, quotaInt(o.Org.QuotaMonitors), "monitors"), Size: "l"}, {HTML: ui.Usage(o.Agents, quotaInt(o.Org.QuotaAgents), "agents"), Size: "l"}}}
		row.Actions = ui.Button(ui.ButtonProps{Label: "Edit", Href: c.href("/admin/orgs?edit=" + url.QueryEscape(o.Org.Slug))})
		if o.Projects == 0 {
			row.Actions += postForm(c, c.href("/admin/orgs/"+o.Org.Slug+"/delete"), false, ui.Button(ui.ButtonProps{Label: "Delete", Variant: "danger", Confirm: "Really delete?", Type: "submit"}))
		}
		d.OrgRows = append(d.OrgRows, row)
	}
	return h.render(c, status, "instance", "layout", d)
}

func quotaInt(q *int64) int {
	if q == nil {
		return 0
	}
	return int(*q)
}

func (h *Web) createOrgAdmin(c *reqCtx) error {
	p := h.newOrgPanel(c)
	for _, k := range []string{"org_slug", "org_name", "org_owner", "org_q_mon", "org_q_ag"} {
		p.Values[k] = strings.TrimSpace(c.r.PostFormValue(k))
	}
	qm, err := parseQuota(p.Values["org_q_mon"])
	if err != nil {
		p.Errors["org_q_mon"] = capitalise(err.Error()) + "."
	}
	qa, err := parseQuota(p.Values["org_q_ag"])
	if err != nil {
		p.Errors["org_q_ag"] = capitalise(err.Error()) + "."
	}
	if len(p.Errors) > 0 {
		return h.orgsTab(c, http.StatusUnprocessableEntity, p)
	}
	org, err := h.svc.CreateOrgWithOwner(c.r.Context(), c.scope, p.Values["org_slug"], p.Values["org_name"], p.Values["org_owner"], qm, qa)
	if err != nil {
		if applyOrgErrors(p, err) {
			return h.orgsTab(c, http.StatusUnprocessableEntity, p)
		}
		return err
	}
	return h.redirect(c, "/admin/orgs?flash="+url.QueryEscape("Org "+org.Slug+" created. Invite its people from its Members tab."))
}

func applyOrgErrors(p *orgPanel, err error) bool {
	if ve, ok := domain.AsValidation(err); ok {
		for _, fe := range ve.Errors {
			field := map[string]string{"slug": "org_slug", "name": "org_name", "owner": "org_owner", "org": "org_slug"}[fe.Field]
			if field == "" {
				field = "org_slug"
			}
			p.Errors[field] = capitalise(fe.Msg) + "."
		}
		return true
	}
	if errors.Is(err, domain.ErrConflict) {
		p.Errors["org_slug"] = "An org with this slug exists."
		return true
	}
	return false
}

func (h *Web) updateOrgAdmin(c *reqCtx) error {
	org, err := h.svc.OrgBySlug(c.r.Context(), c.r.PathValue("slug"))
	if err != nil {
		return err
	}
	p := h.editOrgPanel(c, org)
	for _, k := range []string{"org_name", "org_q_mon", "org_q_ag"} {
		p.Values[k] = strings.TrimSpace(c.r.PostFormValue(k))
	}
	qm, err := parseQuota(p.Values["org_q_mon"])
	if err != nil {
		p.Errors["org_q_mon"] = capitalise(err.Error()) + "."
	}
	qa, err := parseQuota(p.Values["org_q_ag"])
	if err != nil {
		p.Errors["org_q_ag"] = capitalise(err.Error()) + "."
	}
	if len(p.Errors) > 0 {
		return h.orgsTab(c, http.StatusUnprocessableEntity, p)
	}
	if _, err := h.svc.UpdateOrg(c.r.Context(), c.scope, org.ID, p.Values["org_name"], qm, qa); err != nil {
		if applyOrgErrors(p, err) {
			return h.orgsTab(c, http.StatusUnprocessableEntity, p)
		}
		return err
	}
	return h.redirect(c, "/admin/orgs?flash="+url.QueryEscape("Org "+org.Slug+" saved."))
}

func (h *Web) deleteOrgAdmin(c *reqCtx) error {
	org, err := h.svc.OrgBySlug(c.r.Context(), c.r.PathValue("slug"))
	if err != nil {
		return err
	}
	sc := c.scope
	sc.OrgID = org.ID
	if err := h.svc.DeleteOrg(c.r.Context(), sc); err != nil {
		if ve, ok := domain.AsValidation(err); ok {
			return h.redirect(c, "/admin/orgs?flash="+url.QueryEscape(capitalise(ve.Errors[0].Msg)+"."))
		}
		return err
	}
	return h.redirect(c, "/admin/orgs?flash="+url.QueryEscape("Org "+org.Slug+" deleted."))
}

// --- users ----------------------------------------------------------------

func userMatches(u service.InstanceUser, filter string) bool {
	switch filter {
	case "local", "proxy", "oidc":
		return u.Source == filter
	case "admins":
		return u.InstanceAdmin
	case "disabled":
		return u.Disabled()
	}
	return true
}

func userSub(u service.InstanceUser, loc *time.Location) string {
	who := u.Subject
	if u.Email != "" {
		who = u.Email
	}
	parts := []string{who, u.Source}
	switch {
	case u.Disabled():
		parts = append(parts, "disabled by "+u.DisabledBy+" on "+u.DisabledAt.In(loc).Format("2 Jan"))
	case u.Source == "local" && u.TOTPOn():
		parts = append(parts, "two-factor on")
	case u.Source == "local":
		parts = append(parts, "two-factor off")
	case u.InstanceAdmin:
		parts = append(parts, "group instance admin")
	}
	return strings.Join(parts, " · ")
}

func (h *Web) usersTab(c *reqCtx, status int, panel *userPanel) error {
	d, err := h.instanceData(c, "users")
	if err != nil {
		return err
	}
	d.UserPanel = panel
	users, err := h.svc.ListInstanceUsers(c.r.Context(), c.scope)
	if err != nil {
		return err
	}
	filter := c.r.URL.Query().Get("filter")
	counts := map[string]int{}
	for _, u := range users {
		counts[u.Source]++
		if u.InstanceAdmin {
			counts["admins"]++
		}
		if u.Disabled() {
			counts["disabled"]++
		}
	}
	var chips ui.HTML
	for _, f := range []struct{ key, label string }{{"local", "local"}, {"proxy", "proxy"}, {"oidc", "oidc"}, {"admins", "instance admins"}, {"disabled", "disabled"}} {
		value := f.key
		if filter == f.key {
			value = ""
		}
		chips += ui.Chip(ui.ChipProps{Label: f.label, Count: ui.Count(counts[f.key]), Pressed: filter == f.key, Type: "submit", Attrs: ui.Attr("name", "filter") + ui.Attr("value", value)})
	}
	d.Chips = chips
	loc := time.UTC
	for _, u := range users {
		if !userMatches(u, filter) {
			continue
		}
		self := u.ID == c.principal.User.ID
		title := ui.HTML(esc(u.Name()))
		if self {
			title += " " + ui.Tag("you")
		}
		var roles []string
		for _, m := range u.Memberships {
			roles = append(roles, m.OrgSlug+" "+string(m.Role))
		}
		flag := ui.HTML("")
		switch {
		case u.Disabled():
			flag = ui.Tag("disabled")
		case u.InstanceAdmin:
			flag = ui.Tag("instance admin")
		}
		row := userRow{ID: u.ID, Lead: string(ui.Avatar(u.Name())), TitleHTML: title, Sub: userSub(u, loc), Muted: u.Disabled(),
			Cells:   []ui.Cell{{Text: strings.Join(roles, " · "), Size: "l"}, {HTML: flag, Size: "m"}, {Text: seenCell(u.LastSeenAt, c.now), Size: "m", Mono: true}},
			Actions: ui.Button(ui.ButtonProps{Label: "Edit", Href: c.href("/admin/users?edit=" + url.QueryEscape(u.ID))}),
		}
		d.UserRows = append(d.UserRows, row)
	}
	return h.render(c, status, "instance", "layout", d)
}

func (h *Web) userPanelFor(c *reqCtx, id string, link *noteData) (*userPanel, error) {
	users, err := h.svc.ListInstanceUsers(c.r.Context(), c.scope)
	if err != nil {
		return nil, err
	}
	for _, u := range users {
		if u.ID != id {
			continue
		}
		var roles []string
		for _, m := range u.Memberships {
			roles = append(roles, m.OrgSlug+" "+string(m.Role))
		}
		sub := u.Subject + " · " + u.Source + " account"
		if len(roles) > 0 {
			sub += " · " + strings.Join(roles, " · ")
		}
		p := &userPanel{
			ID: u.ID, Title: "Edit " + u.Name(), Sub: sub, Action: c.href("/admin/users/" + u.ID), CancelPath: c.href("/admin/users"), CSRF: c.csrf(),
			IsAdmin: u.InstanceAdmin, Local: u.Source == "local", TOTPOn: u.TOTPOn(), Disabled: u.Disabled(), Self: u.ID == c.principal.User.ID,
			ResetTOTPPath: c.href("/admin/users/" + u.ID + "/totp-reset"), ResetLinkPath: c.href("/admin/users/" + u.ID + "/reset-link"),
			DisablePath: c.href("/admin/users/" + u.ID + "/disable"), EnablePath: c.href("/admin/users/" + u.ID + "/enable"), Link: link,
			AdminHint: "Can create orgs, set quotas, and see and change every org.",
		}
		if u.Source != "local" {
			p.AdminLocked = true
			p.AdminHint = "Comes from the instance_admin_group of the identity provider."
		}
		if p.Self {
			p.AdminLocked = true
			p.AdminHint = "You cannot take instance admin from yourself."
		}
		return p, nil
	}
	return nil, domain.NotFound("user")
}

func (h *Web) saveUserAdmin(c *reqCtx) error {
	p, err := h.userPanelFor(c, c.r.PathValue("id"), nil)
	if err != nil {
		return err
	}
	if !p.AdminLocked {
		u, err := h.svc.UserByID(c.r.Context(), p.ID)
		if err != nil {
			return err
		}
		want := c.r.PostFormValue("is_admin") == "1"
		if err := h.svc.SetInstanceAdmin(c.r.Context(), c.scope, u.Subject, want); err != nil {
			if ve, ok := domain.AsValidation(err); ok {
				p.Error = capitalise(ve.Errors[0].Msg) + "."
				return h.usersTab(c, http.StatusUnprocessableEntity, p)
			}
			return err
		}
	}
	return h.redirect(c, "/admin/users?flash="+url.QueryEscape("Saved."))
}

func (h *Web) resetUserTOTP(c *reqCtx) error {
	if err := h.svc.ResetTOTP(c.r.Context(), c.scope, c.r.PathValue("id")); err != nil {
		return err
	}
	return h.redirect(c, "/admin/users?edit="+url.QueryEscape(c.r.PathValue("id"))+"&flash="+url.QueryEscape("Two-factor reset. They set it up again at the next sign-in."))
}

func (h *Web) makeResetLink(c *reqCtx) error {
	token, expires, err := h.svc.CreateResetLink(c.r.Context(), c.scope, c.r.PathValue("id"))
	if err != nil {
		if ve, ok := domain.AsValidation(err); ok {
			p, perr := h.userPanelFor(c, c.r.PathValue("id"), nil)
			if perr != nil {
				return perr
			}
			p.Error = capitalise(ve.Errors[0].Msg) + "."
			return h.usersTab(c, http.StatusUnprocessableEntity, p)
		}
		return err
	}
	link := h.svc.Config().BaseURL + "/reset/" + token
	note := &noteData{Tone: "ok", Title: "Reset link created.", Text: "It works once, until " + view.In(expires, c.now) + " (" + expires.UTC().Format("Mon 2 Jan 15:04 MST") + ").",
		HTML: ui.HTML(`<div class="vk-field__row"><code class="vk-ping__url">` + esc(link) + `</code><button type="button" class="vk-btn vk-copy" data-copy="` + esc(link) + `">Copy</button></div>`)}
	p, err := h.userPanelFor(c, c.r.PathValue("id"), note)
	if err != nil {
		return err
	}
	return h.usersTab(c, http.StatusOK, p)
}

func (h *Web) setUserDisabled(disabled bool) handlerFn {
	return func(c *reqCtx) error {
		if err := h.svc.SetUserDisabled(c.r.Context(), c.scope, c.r.PathValue("id"), disabled); err != nil {
			if ve, ok := domain.AsValidation(err); ok {
				p, perr := h.userPanelFor(c, c.r.PathValue("id"), nil)
				if perr != nil {
					return perr
				}
				p.Error = capitalise(ve.Errors[0].Msg) + "."
				return h.usersTab(c, http.StatusUnprocessableEntity, p)
			}
			return err
		}
		msg := "Account enabled."
		if disabled {
			msg = "Account disabled and signed out everywhere."
		}
		return h.redirect(c, "/admin/users?flash="+url.QueryEscape(msg))
	}
}

// --- server ---------------------------------------------------------------

func (h *Web) serverTab(c *reqCtx) error {
	d, err := h.instanceData(c, "server")
	if err != nil {
		return err
	}
	facts := ServerFacts{Build: [][2]string{{"version", version.Version}, {"commit", version.Commit}, {"built", version.Date}}}
	if h.facts != nil {
		facts = h.facts(c.r.Context())
	}
	if last, ok := h.svc.LastBackupAt(c.r.Context()); ok {
		facts.LastBackup = last
	}
	switch {
	case facts.LastBackup.IsZero():
		d.Backup = &noteData{Tone: "warn", Title: "No backup yet.", Text: "Run vink admin backup on the server, or schedule it with your other backups."}
	case c.now.Sub(facts.LastBackup) > 24*time.Hour:
		d.Backup = &noteData{Tone: "warn", Title: "No backup for " + view.Span(c.now.Sub(facts.LastBackup)) + ".", Text: "Run vink admin backup on the server, or schedule it with your other backups."}
	}
	last := "never"
	if !facts.LastBackup.IsZero() {
		last = facts.LastBackup.UTC().Format("Mon 2 Jan 15:04") + ", " + view.Ago(facts.LastBackup, c.now)
	}
	facts.Database = append([][2]string{}, facts.Database...)
	facts.Database = insertFact(facts.Database, "last backup", last)
	d.Facts = &facts
	return h.render(c, http.StatusOK, "instance", "layout", d)
}

// insertFact replaces or appends one fact.
func insertFact(list [][2]string, key, value string) [][2]string {
	for i := range list {
		if list[i][0] == key {
			list[i][1] = value
			return list
		}
	}
	return append(list, [2]string{key, value})
}

// --- the reset page -------------------------------------------------------

type resetData struct {
	base
	Action string
	Who    string
	Maker  string
	Until  string
	Error  string
}

func (h *Web) resetPage(c *reqCtx) error {
	link, err := h.svc.ResetLinkByToken(c.r.Context(), c.r.PathValue("token"))
	if err != nil {
		return err
	}
	if !link.Open {
		return h.expiredReset(c, link)
	}
	return h.render(c, http.StatusOK, "reset", "layout", h.resetForm(c, link, ""))
}

func (h *Web) resetForm(c *reqCtx, link *service.ResetLink, errMsg string) resetData {
	who := link.Subject
	if link.Name != "" && link.Name != link.Subject {
		who = link.Name + " (" + link.Subject + ")"
	}
	d := resetData{base: h.baseFor(c, "Set a new password", ""), Action: c.href(c.r.URL.Path), Who: who, Maker: link.CreatedBy, Until: link.ExpiresAt.UTC().Format("Mon 2 Jan 15:04 MST"), Error: errMsg}
	d.Fill = true
	return d
}

func (h *Web) expiredReset(c *reqCtx, link *service.ResetLink) error {
	page := authPage{
		Heading: "This reset link has expired",
		Lead:    "The link for " + link.Subject + " was valid for 24 hours and works once.",
		KV:      []kv{{"account", link.Subject}, {"made by", link.CreatedBy}, {"expired", link.ExpiresAt.UTC().Format("Mon 2 Jan 15:04 MST")}},
		Note:    "Ask an instance admin for a new one. A link that was already used shows this page too.",
		Actions: []ui.ButtonProps{{Label: "Go to sign in", Variant: "primary", Href: c.href("/login")}},
	}
	return h.authPage(c, http.StatusGone, page)
}

func (h *Web) resetPassword(c *reqCtx) error {
	token := c.r.PathValue("token")
	link, err := h.svc.ResetLinkByToken(c.r.Context(), token)
	if err != nil {
		return err
	}
	if !link.Open {
		return h.expiredReset(c, link)
	}
	password := c.r.PostFormValue("password")
	u, err := h.svc.ResetPasswordByToken(c.r.Context(), token, password)
	if err != nil {
		if ve, ok := domain.AsValidation(err); ok {
			return h.render(c, http.StatusUnprocessableEntity, "reset", "layout", h.resetForm(c, link, capitalise(ve.Errors[0].Msg)+"."))
		}
		if errors.Is(err, domain.ErrNotFound) {
			return h.expiredReset(c, link)
		}
		return err
	}
	if _, err := h.authn.Login(c.w, c.r, u.Subject, password); err != nil {
		if errors.Is(err, auth.ErrNeedsCode) {
			http.Redirect(c.w, c.r, c.href("/login/code"), http.StatusSeeOther)
			return nil
		}
		http.Redirect(c.w, c.r, c.href("/login"), http.StatusSeeOther)
		return nil
	}
	http.Redirect(c.w, c.r, c.href("/"), http.StatusSeeOther)
	return nil
}
