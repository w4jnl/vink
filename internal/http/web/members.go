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
)

// The members tab of the org settings: people and their roles, invites
// as one-time links, and the owner actions.

type memberRow struct {
	UserID    string
	Lead      string
	TitleHTML ui.HTML
	Sub       string
	Cells     []ui.Cell
	Actions   ui.HTML
	Muted     bool
	Note      ui.HTML
}

type inviteRow struct {
	ID      string
	Lead    string
	Title   string
	Sub     string
	Cells   []ui.Cell
	Actions ui.HTML
	Muted   bool
	Note    ui.HTML
}

type invitePanel struct {
	Action, CancelPath, CSRF string
	Values, Errors           map[string]string
	Roles                    []ui.Option
	// ByName is set when roles are set in vink, so the hint can say
	// people from the provider are added by name instead.
	ByName bool
}

// addPanel adds a person who signs in through the proxy or OIDC by the
// name the provider sends, when that provider's roles are set in vink.
type addPanel struct {
	Action, CancelPath, CSRF, InvitePath, NameHint string
	Values, Errors                                 map[string]string
	Roles                                          []ui.Option
}

type ownerActions struct {
	TransferPath, DeletePath, CSRF string
	Candidates                     []ui.Option
	DeleteSub                      string
	CanDelete                      bool
	Error                          string
	// The two rows, built here because their cells carry components.
	TransferCells   []ui.Cell
	TransferActions ui.HTML
	DeleteActions   ui.HTML
}

type membersData struct {
	adminData
	// AddByName: the tab head offers Add member instead of Invite.
	AddByName    bool
	AddPanel     *addPanel
	Panel        *invitePanel
	Members      []memberRow
	Invites      []inviteRow
	InviteCounts string
	Owner        *ownerActions
	Flash        string
	FlashTone    string
	GroupNotes   []*noteData
}

// membersOpts are what one render of the tab may carry.
type membersOpts struct {
	panel      *invitePanel
	addPanel   *addPanel
	createdID  string // the invite whose link shows once
	createdURL string // the link itself
	flash      string // a sentence under the lede
	flashTone  string // ok (default) or error
	rowErrors  map[string]string
	ownerError string
}

func (h *Web) membersTab(c *reqCtx, status int, o membersOpts) error {
	d, err := h.membersData(c, o)
	if err != nil {
		return err
	}
	return h.render(c, status, "admin", "layout", d)
}

func roleOptions(includeOwner bool) []ui.Option {
	opts := []ui.Option{}
	if includeOwner {
		opts = append(opts, ui.Option{Value: "owner", Label: "owner"})
	}
	return append(opts, ui.Option{Value: "admin", Label: "admin"}, ui.Option{Value: "member", Label: "member"}, ui.Option{Value: "viewer", Label: "viewer"})
}

func (h *Web) newInvitePanel(c *reqCtx) *invitePanel {
	return &invitePanel{
		Action: c.orgPath() + "/members/invites", CancelPath: c.orgPath() + "/members", CSRF: c.csrf(),
		Values: map[string]string{"inv_role": "member"}, Errors: map[string]string{}, Roles: roleOptions(c.principal.InstanceAdmin || c.scope.CanOwnOrg()),
		ByName: h.svc.AuthPolicy(c.r.Context()).AddSource() != "",
	}
}

func (h *Web) newAddPanel(c *reqCtx) *addPanel {
	p := &addPanel{
		Action: c.orgPath() + "/members/add", CancelPath: c.orgPath() + "/members", CSRF: c.csrf(),
		Values: map[string]string{"add_role": "member"}, Errors: map[string]string{}, Roles: roleOptions(c.principal.InstanceAdmin || c.scope.CanOwnOrg()),
		NameHint: "Exactly as the proxy sends it, like jdoe.",
	}
	if h.svc.AuthPolicy(c.r.Context()).AddSource() == "oidc" {
		p.NameHint = "Exactly as " + h.authn.OIDCDisplayName() + " sends it, like jdoe."
	}
	if h.authn.LocalEnabled() {
		p.InvitePath = c.orgPath() + "/members?invite=1"
	}
	return p
}

// memberSub is the row's second line: who they are and where the role comes from.
func (h *Web) memberSub(m domain.Member) string {
	who := m.Subject
	if m.Email != "" {
		who = m.Email
	}
	switch m.Source {
	case "header":
		return who + " · role from the proxy’s groups"
	case "oidc":
		return who + " · role from " + h.authn.OIDCDisplayName() + " groups"
	}
	switch m.UserSource {
	case "proxy":
		return who + " · proxy account"
	case "oidc":
		return who + " · oidc account"
	}
	return who + " · local account"
}

func seenCell(last *time.Time, now time.Time) string {
	switch {
	case last == nil:
		return "never"
	case now.Sub(*last) < 5*time.Minute:
		return "active now"
	}
	return view.Ago(*last, now)
}

func (h *Web) membersData(c *reqCtx, o membersOpts) (membersData, error) {
	ad, err := h.adminData(c, "members")
	if err != nil {
		return membersData{}, err
	}
	d := membersData{adminData: ad, Panel: o.panel, AddPanel: o.addPanel, Flash: o.flash, FlashTone: o.flashTone}
	d.AddByName = h.svc.AuthPolicy(c.r.Context()).AddSource() != ""
	if d.Flash == "" {
		d.Flash = c.r.URL.Query().Get("flash")
	}
	if d.FlashTone == "" {
		d.FlashTone = "ok"
	}
	ctx := c.r.Context()
	members, err := h.svc.ListMembers(ctx, c.scope)
	if err != nil {
		return d, err
	}
	owner := c.principal.InstanceAdmin || c.scope.CanOwnOrg()
	root := c.orgPath() + "/members"
	derived := map[string]int{}
	for _, m := range members {
		self := m.UserID == c.principal.User.ID
		locked := m.Source != "local" || self
		title := ui.HTML(esc(m.Name()))
		if self {
			title += " " + ui.Tag("you")
		}
		sel := ui.InlineSelect(ui.InlineSelectProps{
			Label: "Role for " + m.Name(), Name: "role", Options: roleOptions(owner || m.Role == domain.RoleOwner), Value: string(m.Role), Disabled: locked,
			Attrs: ui.Attr("hx-post", root+"/"+m.UserID+"/role") + ui.Attr("hx-trigger", "change") + ui.Attr("hx-target", "closest .vk-srow") + ui.Attr("hx-swap", "outerHTML"),
		})
		row := memberRow{
			UserID: m.UserID, Lead: string(ui.Avatar(m.Name())), TitleHTML: title, Sub: h.memberSub(m), Muted: m.Disabled,
			Cells: []ui.Cell{{HTML: sel}, {Text: seenCell(m.LastSeenAt, c.now), Mono: true}},
		}
		derived[m.Source]++
		switch {
		case self:
			row.Actions = ""
		case m.Source != "local":
			row.Actions = ui.Button(ui.ButtonProps{Label: "Remove", Disabled: true})
		default:
			row.Actions = postForm(c, root+"/"+m.UserID+"/remove", false, ui.Button(ui.ButtonProps{Label: "Remove", Variant: "danger", Confirm: "Really remove?", Type: "submit"}))
		}
		if msg := o.rowErrors[m.UserID]; msg != "" {
			row.Note = ui.Notice("error", "", msg, "")
		}
		d.Members = append(d.Members, row)
	}
	if n := derived["header"]; n > 0 {
		d.GroupNotes = append(d.GroupNotes, &noteData{Tone: "info", Title: plural(n, "member") + " " + isAre(n) + " from the proxy.", Text: h.authn.GroupHint("header", c.org.Slug)})
	}
	if n := derived["oidc"]; n > 0 {
		d.GroupNotes = append(d.GroupNotes, &noteData{Tone: "info", Title: plural(n, "member") + " " + isAre(n) + " from " + h.authn.OIDCDisplayName() + ".", Text: h.authn.GroupHint("oidc", c.org.Slug)})
	}
	invites, err := h.svc.ListInvites(ctx, c.scope)
	if err != nil {
		return d, err
	}
	loc := h.orgLocation(c)
	open, expired, used := 0, 0, 0
	for _, inv := range invites {
		state := inv.State(c.now)
		row := inviteRow{ID: inv.ID, Lead: string(ui.Avatar(inv.Note)), Title: "for " + inv.Note, Sub: "created by " + inv.CreatedBy + " · " + dayOrClock(inv.CreatedAt, c.now, loc)}
		row.Cells = []ui.Cell{{HTML: ui.Tag(string(inv.Role))}}
		switch state {
		case domain.InviteOpen:
			open++
			row.Cells = append(row.Cells, ui.Cell{Text: "expires " + inv.ExpiresAt.In(loc).Format("Mon 2 Jan"), Size: "l", Mono: true})
			row.Actions = postForm(c, root+"/invites/"+inv.ID+"/revoke", false, ui.Button(ui.ButtonProps{Label: "Revoke", Variant: "danger", Confirm: "Really revoke?", Type: "submit"}))
		case domain.InviteUsed:
			used++
			row.Muted = true
			row.Cells = append(row.Cells, ui.Cell{Text: "joined as " + inv.UsedBy, Size: "l", Mono: true})
		case domain.InviteRevoked:
			expired++
			row.Muted = true
			row.Cells = append(row.Cells, ui.Cell{Text: "revoked " + inv.RevokedAt.In(loc).Format("Mon 2 Jan"), Size: "l", Mono: true})
			row.Actions = postForm(c, root+"/invites/"+inv.ID+"/remove", false, ui.Button(ui.ButtonProps{Label: "Remove", Type: "submit"}))
		default:
			expired++
			row.Muted = true
			row.Cells = append(row.Cells, ui.Cell{Text: "expired " + inv.ExpiresAt.In(loc).Format("Mon 2 Jan"), Size: "l", Mono: true})
			row.Actions = postForm(c, root+"/invites/"+inv.ID+"/remove", false, ui.Button(ui.ButtonProps{Label: "Remove", Type: "submit"}))
		}
		if inv.ID == o.createdID && o.createdURL != "" {
			row.Note = ui.Notice("ok", "Invite link created.", "Send it to "+inv.Note+". It works once; vink keeps only a hash, so this is the only time you see it.",
				ui.HTML(`<div class="vk-field__row"><code class="vk-ping__url">`+esc(o.createdURL)+`</code><button type="button" class="vk-btn vk-copy" data-copy="`+esc(o.createdURL)+`">Copy</button></div>`))
		}
		d.Invites = append(d.Invites, row)
	}
	var parts []string
	if open > 0 {
		parts = append(parts, strconv.Itoa(open)+" open")
	}
	if expired > 0 {
		parts = append(parts, strconv.Itoa(expired)+" expired")
	}
	if used > 0 {
		parts = append(parts, strconv.Itoa(used)+" used")
	}
	d.InviteCounts = strings.Join(parts, " · ")
	if owner {
		oa := &ownerActions{TransferPath: root + "/transfer", DeletePath: root + "/delete-org", CSRF: c.csrf(), Error: o.ownerError}
		for _, m := range members {
			if m.UserID != c.principal.User.ID && !m.Disabled {
				oa.Candidates = append(oa.Candidates, ui.Option{Value: m.UserID, Label: m.Name()})
			}
		}
		sort.Slice(oa.Candidates, func(i, j int) bool { return oa.Candidates[i].Label < oa.Candidates[j].Label })
		projects, err := h.svc.ListProjects(ctx, c.scope)
		if err != nil {
			return d, err
		}
		if len(projects) == 0 {
			oa.CanDelete = true
			oa.DeleteSub = "Deletes " + c.org.Slug + " with its members, invites, agents and keys. Nothing is left."
		} else {
			oa.DeleteSub = "Possible once " + c.org.Slug + " has no projects. It has " + plural(len(projects), "project") + "."
		}
		none := len(oa.Candidates) == 0
		oa.TransferCells = []ui.Cell{{HTML: ui.InlineSelect(ui.InlineSelectProps{Label: "New owner", Name: "new_owner", Options: oa.Candidates, Disabled: none, Attrs: ui.Attr("form", "transfer-form")})}}
		oa.TransferActions = ui.Button(ui.ButtonProps{Label: "Transfer", Type: "submit", Confirm: "Really transfer?", Disabled: none, Attrs: ui.Attr("form", "transfer-form")})
		oa.DeleteActions = ui.Button(ui.ButtonProps{Label: "Delete org", Variant: "danger", Type: "submit", Confirm: "Really delete?", Disabled: !oa.CanDelete, Attrs: ui.Attr("form", "delete-org-form")})
		d.Owner = oa
	}
	return d, nil
}

// dayOrClock writes "today 14:02" or "Wed 23 Sep".
func dayOrClock(t, now time.Time, loc *time.Location) string {
	if t.In(loc).Format("2006-01-02") == now.In(loc).Format("2006-01-02") {
		return "today " + t.In(loc).Format("15:04")
	}
	return t.In(loc).Format("Mon 2 Jan")
}

// membersList is the tab; ?invite=1 opens the invite panel, ?add=1 the
// Add member panel when roles are set in vink.
func (h *Web) membersList(c *reqCtx) error {
	o := membersOpts{}
	switch {
	case c.r.URL.Query().Get("invite") == "1":
		o.panel = h.newInvitePanel(c)
	case c.r.URL.Query().Get("add") == "1" && h.svc.AuthPolicy(c.r.Context()).AddSource() != "":
		o.addPanel = h.newAddPanel(c)
	}
	return h.membersTab(c, http.StatusOK, o)
}

// addMember gives a person a role by the name their provider sends; a
// name vink has not seen becomes an account.
func (h *Web) addMember(c *reqCtx) error {
	p := h.newAddPanel(c)
	p.Values["add_name"] = strings.TrimSpace(c.r.PostFormValue("add_name"))
	p.Values["add_role"] = strings.TrimSpace(c.r.PostFormValue("add_role"))
	u, err := h.svc.AddMember(c.r.Context(), c.scope, p.Values["add_name"], domain.Role(p.Values["add_role"]))
	if err != nil {
		ve, isValidation := domain.AsValidation(err)
		switch {
		case isValidation:
			for _, fe := range ve.Errors {
				field := "add_name"
				if fe.Field == "role" {
					field = "add_role"
				}
				p.Errors[field] = capitalise(fe.Msg) + "."
			}
		case errors.Is(err, domain.ErrConflict):
			p.Errors["add_name"] = "Already a member here; change the role on the row."
		default:
			return err
		}
		return h.membersTab(c, http.StatusUnprocessableEntity, membersOpts{addPanel: p})
	}
	return h.redirect(c, c.orgPath()+"/members?flash="+url.QueryEscape(u.Subject+" added as "+p.Values["add_role"]+"."))
}

// createInvite makes the link and shows it once under the new row.
func (h *Web) createInvite(c *reqCtx) error {
	p := h.newInvitePanel(c)
	p.Values["inv_for"] = strings.TrimSpace(c.r.PostFormValue("inv_for"))
	p.Values["inv_role"] = strings.TrimSpace(c.r.PostFormValue("inv_role"))
	inv, token, err := h.svc.CreateInvite(c.r.Context(), c.scope, p.Values["inv_for"], domain.Role(p.Values["inv_role"]))
	if err != nil {
		if ve, ok := domain.AsValidation(err); ok {
			for _, fe := range ve.Errors {
				field := "inv_for"
				if fe.Field == "role" {
					field = "inv_role"
				}
				p.Errors[field] = capitalise(fe.Msg) + "."
			}
			return h.membersTab(c, http.StatusUnprocessableEntity, membersOpts{panel: p})
		}
		return err
	}
	return h.membersTab(c, http.StatusOK, membersOpts{createdID: inv.ID, createdURL: h.svc.Config().BaseURL + "/invite/" + token})
}

// setMemberRole saves a role from the row's select and answers with the row.
func (h *Web) setMemberRole(c *reqCtx) error {
	userID := c.r.PathValue("user")
	role := domain.Role(strings.TrimSpace(c.r.PostFormValue("role")))
	if err := h.svc.SetMembership(c.r.Context(), c.scope, userID, c.org.ID, role); err != nil {
		var msg string
		ve, isValidation := domain.AsValidation(err)
		switch {
		case isValidation:
			msg = capitalise(ve.Errors[0].Msg) + "."
		case errors.Is(err, domain.ErrForbidden):
			msg = "You cannot change this role."
		default:
			return err
		}
		return h.memberRowResponse(c, http.StatusUnprocessableEntity, userID, msg)
	}
	return h.memberRowResponse(c, http.StatusOK, userID, "")
}

// memberRowResponse answers a role change: the row alone for htmx, the
// whole tab otherwise.
func (h *Web) memberRowResponse(c *reqCtx, status int, userID, msg string) error {
	o := membersOpts{}
	if msg != "" {
		o.rowErrors = map[string]string{userID: msg}
	}
	if !c.htmx() {
		if msg != "" {
			o.flash, o.flashTone = msg, "error"
		}
		return h.membersTab(c, status, o)
	}
	d, err := h.membersData(c, o)
	if err != nil {
		return err
	}
	for _, row := range d.Members {
		if row.UserID == userID {
			return h.render(c, status, "admin", "member-row", row)
		}
	}
	return domain.NotFound("member")
}

func (h *Web) removeMember(c *reqCtx) error {
	err := h.svc.RemoveMembership(c.r.Context(), c.scope, c.r.PathValue("user"), c.org.ID)
	if err != nil {
		if ve, ok := domain.AsValidation(err); ok {
			return h.membersTab(c, http.StatusUnprocessableEntity, membersOpts{flash: capitalise(ve.Errors[0].Msg) + ".", flashTone: "error"})
		}
		return err
	}
	return h.redirect(c, c.orgPath()+"/members?flash="+url.QueryEscape("Member removed."))
}

func (h *Web) revokeInvite(c *reqCtx) error {
	if err := h.svc.RevokeInvite(c.r.Context(), c.scope, c.r.PathValue("id")); err != nil {
		return err
	}
	return h.redirect(c, c.orgPath()+"/members?flash="+url.QueryEscape("Invite revoked. The link no longer works."))
}

func (h *Web) removeInvite(c *reqCtx) error {
	if err := h.svc.RemoveInvite(c.r.Context(), c.scope, c.r.PathValue("id")); err != nil {
		return err
	}
	return h.redirect(c, c.orgPath()+"/members")
}

func (h *Web) transferOwnership(c *reqCtx) error {
	newOwner := strings.TrimSpace(c.r.PostFormValue("new_owner"))
	if err := h.svc.TransferOwnership(c.r.Context(), c.scope, newOwner); err != nil {
		if ve, ok := domain.AsValidation(err); ok {
			return h.membersTab(c, http.StatusUnprocessableEntity, membersOpts{ownerError: capitalise(ve.Errors[0].Msg) + "."})
		}
		return err
	}
	u, err := h.svc.UserByID(c.r.Context(), newOwner)
	if err != nil {
		return err
	}
	return h.redirect(c, c.orgPath()+"/members?flash="+url.QueryEscape("Ownership of "+c.org.Slug+" went to "+u.Name()+". You stay on as admin."))
}

func (h *Web) deleteOrg(c *reqCtx) error {
	if err := h.svc.DeleteOrg(c.r.Context(), c.scope); err != nil {
		if ve, ok := domain.AsValidation(err); ok {
			return h.membersTab(c, http.StatusUnprocessableEntity, membersOpts{ownerError: capitalise(ve.Errors[0].Msg) + "."})
		}
		return err
	}
	return h.redirect(c, "/")
}

// --- the invite page ----------------------------------------------------

type inviteFormData struct {
	base
	Action  string
	Org     string
	Role    string
	Inviter string
	Expires string
	Values  map[string]string
	Errors  map[string]string
	Error   string
}

// invitePage shows the join form for an open link, the expired card for
// any other, and a join button to someone already signed in.
func (h *Web) invitePage(c *reqCtx) error {
	inv, err := h.svc.InviteByToken(c.r.Context(), c.r.PathValue("token"))
	if err != nil {
		return err
	}
	if inv.State(c.now) != domain.InviteOpen {
		return h.expiredInvite(c, inv)
	}
	return h.render(c, http.StatusOK, "invite", "layout", h.inviteForm(c, inv, nil))
}

func (h *Web) inviteForm(c *reqCtx, inv *domain.Invite, values map[string]string) inviteFormData {
	if values == nil {
		values = map[string]string{}
	}
	d := inviteFormData{
		base: h.baseFor(c, "Join "+inv.OrgSlug, ""), Action: c.href(c.r.URL.Path), Org: inv.OrgSlug, Role: string(inv.Role), Inviter: inv.CreatedBy,
		Expires: inv.ExpiresAt.In(time.UTC).Format("Mon 2 Jan"), Values: values, Errors: map[string]string{},
	}
	d.Fill = true
	return d
}

func (h *Web) expiredInvite(c *reqCtx, inv *domain.Invite) error {
	ran := inv.ExpiresAt
	if inv.RevokedAt != nil && inv.RevokedAt.Before(ran) {
		ran = *inv.RevokedAt
	}
	if inv.UsedAt != nil {
		ran = inv.ExpiresAt
	}
	page := authPage{
		Heading: "This invite has expired",
		Lead:    "The link to join " + inv.OrgSlug + " was valid for 7 days and ran out on " + ran.UTC().Format("Mon 2 Jan") + ".",
		KV:      []kv{{"org", inv.OrgSlug}, {"invited by", inv.CreatedBy}, {"role", string(inv.Role)}, {"expired", ran.UTC().Format("Mon 2 Jan 15:04 MST")}},
		Note:    "Ask " + inv.CreatedBy + " or another admin of " + inv.OrgSlug + " for a new link. A link that was already used shows this page too.",
		Actions: []ui.ButtonProps{{Label: "Go to sign in", Variant: "primary", Href: c.href("/login")}},
	}
	return h.authPage(c, http.StatusGone, page)
}

// acceptInvite creates the account, signs it in and lands on /.
func (h *Web) acceptInvite(c *reqCtx) error {
	token := c.r.PathValue("token")
	inv, err := h.svc.InviteByToken(c.r.Context(), token)
	if err != nil {
		return err
	}
	if inv.State(c.now) != domain.InviteOpen {
		return h.expiredInvite(c, inv)
	}
	values := map[string]string{
		"username": strings.TrimSpace(c.r.PostFormValue("username")), "display_name": strings.TrimSpace(c.r.PostFormValue("display_name")),
	}
	password := c.r.PostFormValue("password")
	d := h.inviteForm(c, inv, values)
	if _, err := h.svc.AcceptInvite(c.r.Context(), token, values["username"], values["display_name"], password); err != nil {
		switch {
		case errors.Is(err, domain.ErrConflict):
			d.Errors["username"] = "That username is taken. Sign in instead, then open the link again."
		case errors.Is(err, domain.ErrNotFound):
			return h.expiredInvite(c, inv)
		default:
			ve, ok := domain.AsValidation(err)
			if !ok {
				return err
			}
			for _, fe := range ve.Errors {
				field := fe.Field
				if field == "subject" {
					field = "username"
				}
				d.Errors[field] = capitalise(fe.Msg) + "."
			}
		}
		return h.render(c, http.StatusUnprocessableEntity, "invite", "layout", d)
	}
	if _, err := h.authn.Login(c.w, c.r, values["username"], password); err != nil {
		return err
	}
	if h.authn.TOTPRequired() {
		http.Redirect(c.w, c.r, c.href("/account?setup=1"), http.StatusSeeOther)
		return nil
	}
	http.Redirect(c.w, c.r, c.href("/"), http.StatusSeeOther)
	return nil
}
