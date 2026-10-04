package web

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/http/web/ui"
	"github.com/w4jnl/vink/internal/qr"
	"github.com/w4jnl/vink/internal/timefmt"
	"github.com/w4jnl/vink/internal/totp"
)

// /account: profile and sessions for everyone, password and two-factor
// sign-in for local accounts.

type accountData struct {
	base
	Subject string
	Source  string
	Local   bool
	Flash   *noteData
	// profile
	Name, Email   string
	ProfileLocked bool
	ProfileHint   string
	Errors        map[string]string
	// two-factor
	TOTPOn      bool
	TOTPSub     string
	TOTPCells   []ui.Cell
	TOTPActions ui.HTML
	Setup       *totpSetup
	Off         bool
	OffError    string
	Codes       []string
	CodesTitle  string
	// sessions
	HasSessions   bool
	Sessions      []sessionRow
	Others        int
	OthersConfirm string
}

type totpSetup struct {
	QR          ui.HTML
	Label       string
	Key, KeyRaw string
	Hint        string
	Error       string
}

type sessionRow struct {
	ID        string
	TitleHTML ui.HTML
	Sub       string
	Cells     []ui.Cell
	Actions   ui.HTML
}

func (h *Web) accountData(c *reqCtx) (accountData, error) {
	u := c.principal.User
	d := accountData{base: h.baseFor(c, "Account", "none"), Subject: u.Subject, Source: u.Source + " account", Local: u.Source == "local",
		Name: u.DisplayName, Email: u.Email, ProfileLocked: u.Source != "local", Errors: map[string]string{}, TOTPOn: u.TOTPOn()}
	if flash := c.r.URL.Query().Get("flash"); flash != "" {
		d.Flash = &noteData{Tone: "ok", Text: flash}
	}
	d.ProfileHint = "Shown to other members. Alert mail goes to channels, not here."
	if d.ProfileLocked {
		d.ProfileHint = "Comes from the identity provider."
	}
	ctx := c.r.Context()
	if d.TOTPOn {
		left, total, err := h.svc.RecoveryCodesLeft(ctx, u.ID)
		if err != nil {
			return d, err
		}
		d.TOTPSub = "on since " + timefmt.Ago(*u.TOTPEnabledAt, c.now) + " · " + strconv.Itoa(left) + " of " + strconv.Itoa(total) + " recovery codes left"
		d.TOTPCells = []ui.Cell{{HTML: ui.StateBadge(ui.StateBadgeProps{State: "up", Label: "on"}), Size: "m"}}
		d.TOTPActions = ui.Button(ui.ButtonProps{Label: "New codes", Type: "submit", Confirm: "Really replace the codes?", Attrs: ui.Attr("form", "codes-form")}) +
			ui.Button(ui.ButtonProps{Label: "Turn off", Variant: "danger", Href: c.href("/account?off=1")})
	} else {
		d.TOTPSub = "off · a code from your phone after the password"
		d.TOTPCells = []ui.Cell{{HTML: ui.StateBadge(ui.StateBadgeProps{State: "paused", Label: "off"}), Size: "m"}}
		d.TOTPActions = ui.Button(ui.ButtonProps{Label: "Turn on", Variant: "primary", Href: c.href("/account?setup=1")})
	}
	if c.principal.Session != nil {
		d.HasSessions = true
		sessions, err := h.svc.Sessions(ctx, u.ID)
		if err != nil {
			return d, err
		}
		loc := h.userLocation(c)
		for _, s := range sessions {
			row := sessionRow{ID: s.ID, TitleHTML: ui.HTML(esc(describeUA(s.UserAgent))), Sub: "since " + s.CreatedAt.In(loc).Format("Mon 2 Jan 15:04") + " · " + s.IP,
				Cells: []ui.Cell{{Text: seenCell(s.LastSeenAt, c.now), Size: "m", Mono: true}}}
			if s.ID == c.principal.Session.ID {
				row.TitleHTML += " " + ui.Tag("this session")
			} else {
				d.Others++
				row.Actions = postForm(c, c.href("/account/sessions/"+s.ID+"/delete"), false, ui.Button(ui.ButtonProps{Label: "Sign out", Type: "submit"}))
			}
			d.Sessions = append(d.Sessions, row)
		}
		d.OthersConfirm = "Really sign out " + strconv.Itoa(d.Others) + " " + pluralWord(d.Others, "session") + "?"
	}
	return d, nil
}

// userLocation is the timezone of the person's first project, else UTC.
func (h *Web) userLocation(c *reqCtx) *time.Location {
	projects, err := h.svc.ProjectsForUser(c.r.Context(), c.principal.User.ID, c.principal.InstanceAdmin)
	if err == nil {
		for _, p := range projects {
			if loc, err := time.LoadLocation(p.Timezone); err == nil {
				return loc
			}
		}
	}
	return time.UTC
}

// describeUA names a browser and platform from a User-Agent, "Firefox on
// macOS", without a parser library.
func describeUA(ua string) string {
	if ua == "" {
		return "Unknown device"
	}
	browser := "Browser"
	switch {
	case strings.Contains(ua, "Firefox/"):
		browser = "Firefox"
	case strings.Contains(ua, "Edg/"):
		browser = "Edge"
	case strings.Contains(ua, "OPR/"):
		browser = "Opera"
	case strings.Contains(ua, "Chrome/") || strings.Contains(ua, "CriOS/"):
		browser = "Chrome"
	case strings.Contains(ua, "Safari/"):
		browser = "Safari"
	case strings.HasPrefix(ua, "curl/"):
		browser = "curl"
	default:
		if name, _, ok := strings.Cut(ua, "/"); ok && name != "" && !strings.ContainsAny(name, " ()") {
			browser = name
		}
	}
	os := ""
	switch {
	case strings.Contains(ua, "iPhone"):
		os = "iPhone"
	case strings.Contains(ua, "iPad"):
		os = "iPad"
	case strings.Contains(ua, "Android"):
		os = "Android"
	case strings.Contains(ua, "Macintosh") || strings.Contains(ua, "Mac OS X"):
		os = "macOS"
	case strings.Contains(ua, "Windows"):
		os = "Windows"
	case strings.Contains(ua, "CrOS"):
		os = "ChromeOS"
	case strings.Contains(ua, "Linux"):
		os = "Linux"
	}
	if os == "" {
		return browser
	}
	return browser + " on " + os
}

func (h *Web) account(c *reqCtx) error {
	d, err := h.accountData(c)
	if err != nil {
		return err
	}
	q := c.r.URL.Query()
	switch {
	case q.Get("setup") == "1" && d.Local && !d.TOTPOn:
		setup, err := h.totpSetupFor(c, "")
		if err != nil {
			return err
		}
		d.Setup = setup
	case q.Get("off") == "1" && d.Local && d.TOTPOn:
		d.Off = true
	}
	return h.render(c, http.StatusOK, "account", "layout", d)
}

// totpSetupFor begins (or resumes) the setup and renders the QR and key.
func (h *Web) totpSetupFor(c *reqCtx, errMsg string) (*totpSetup, error) {
	host := "vink"
	if u, err := url.Parse(h.svc.Config().BaseURL); err == nil && u.Host != "" {
		host = u.Host
	}
	s, err := h.svc.BeginTOTP(c.r.Context(), c.scopeSelf(), c.principal.User.Subject+"@"+host)
	if err != nil {
		return nil, err
	}
	svg, err := qr.SVG(s.URI)
	if err != nil {
		return nil, err
	}
	return &totpSetup{QR: ui.HTML(svg), Label: "QR code for " + host + ", account " + c.principal.User.Subject, Key: totp.Pretty(s.Secret), KeyRaw: s.Secret,
		Hint: "Account " + c.principal.User.Subject + " at " + host + " · 6 digits · a new code every 30 s", Error: errMsg}, nil
}

// scopeSelf is the person acting on their own account.
func (c *reqCtx) scopeSelf() domain.Scope {
	return domain.Scope{UserID: c.principal.User.ID, InstanceAdmin: c.principal.InstanceAdmin, Actor: "user:" + c.principal.User.Subject}
}

func (h *Web) saveProfile(c *reqCtx) error {
	err := h.svc.UpdateProfile(c.r.Context(), c.scopeSelf(), c.r.PostFormValue("acc_name"), c.r.PostFormValue("acc_email"))
	if err != nil {
		if ve, ok := domain.AsValidation(err); ok {
			d, derr := h.accountData(c)
			if derr != nil {
				return derr
			}
			d.Name, d.Email = c.r.PostFormValue("acc_name"), c.r.PostFormValue("acc_email")
			for _, fe := range ve.Errors {
				d.Errors[fe.Field] = capitalise(fe.Msg) + "."
			}
			return h.render(c, http.StatusUnprocessableEntity, "account", "layout", d)
		}
		return err
	}
	return h.redirect(c, "/account?flash="+url.QueryEscape("Profile saved."))
}

func (h *Web) changePassword(c *reqCtx) error {
	sessionID := ""
	if c.principal.Session != nil {
		sessionID = c.principal.Session.ID
	}
	err := h.svc.ChangePassword(c.r.Context(), c.scopeSelf(), sessionID, c.r.PostFormValue("pw_old"), c.r.PostFormValue("pw_new"))
	if err != nil {
		if ve, ok := domain.AsValidation(err); ok {
			d, derr := h.accountData(c)
			if derr != nil {
				return derr
			}
			for _, fe := range ve.Errors {
				d.Errors[fe.Field] = capitalise(fe.Msg) + "."
			}
			return h.render(c, http.StatusUnprocessableEntity, "account", "layout", d)
		}
		return err
	}
	return h.redirect(c, "/account?flash="+url.QueryEscape("Password changed. Your other sessions are signed out."))
}

func (h *Web) confirmTOTP(c *reqCtx) error {
	codes, err := h.svc.ConfirmTOTP(c.r.Context(), c.scopeSelf(), c.r.PostFormValue("otp"))
	if err != nil {
		if ve, ok := domain.AsValidation(err); ok {
			d, derr := h.accountData(c)
			if derr != nil {
				return derr
			}
			setup, serr := h.totpSetupFor(c, capitalise(ve.Errors[0].Msg)+".")
			if serr != nil {
				return serr
			}
			d.Setup = setup
			return h.render(c, http.StatusUnprocessableEntity, "account", "layout", d)
		}
		return err
	}
	// the codes are shown once, so this response renders the page itself
	c.principal.User.TOTPEnabledAt = &c.now
	d, err := h.accountData(c)
	if err != nil {
		return err
	}
	d.Codes, d.CodesTitle = codes, "Two-factor is on."
	return h.render(c, http.StatusOK, "account", "layout", d)
}

func (h *Web) newRecoveryCodes(c *reqCtx) error {
	codes, err := h.svc.NewRecoveryCodes(c.r.Context(), c.scopeSelf())
	if err != nil {
		return err
	}
	d, err := h.accountData(c)
	if err != nil {
		return err
	}
	d.Codes, d.CodesTitle = codes, "New recovery codes."
	return h.render(c, http.StatusOK, "account", "layout", d)
}

func (h *Web) disableTOTP(c *reqCtx) error {
	err := h.svc.DisableTOTP(c.r.Context(), c.scopeSelf(), c.r.PostFormValue("password"))
	if err != nil {
		if ve, ok := domain.AsValidation(err); ok {
			d, derr := h.accountData(c)
			if derr != nil {
				return derr
			}
			d.Off, d.OffError = true, capitalise(ve.Errors[0].Msg)+"."
			return h.render(c, http.StatusUnprocessableEntity, "account", "layout", d)
		}
		return err
	}
	return h.redirect(c, "/account?flash="+url.QueryEscape("Two-factor is off."))
}

func (h *Web) deleteSession(c *reqCtx) error {
	if c.principal.Session != nil && c.r.PathValue("id") == c.principal.Session.ID {
		return domain.NotFound("session")
	}
	if err := h.svc.DeleteOwnSession(c.r.Context(), c.principal.User.ID, c.r.PathValue("id")); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return h.redirect(c, "/account")
		}
		return err
	}
	return h.redirect(c, "/account?flash="+url.QueryEscape("Session signed out."))
}

func (h *Web) deleteOtherSessions(c *reqCtx) error {
	keep := ""
	if c.principal.Session != nil {
		keep = c.principal.Session.ID
	}
	n, err := h.svc.DeleteOtherSessions(c.r.Context(), c.scopeSelf(), keep)
	if err != nil {
		return err
	}
	return h.redirect(c, "/account?flash="+url.QueryEscape("Signed out "+strconv.FormatInt(n, 10)+" other "+pluralWord(int(n), "session")+"."))
}
