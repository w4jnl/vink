package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/http/web/ui"
	"github.com/w4jnl/vink/internal/http/web/view"
	"github.com/w4jnl/vink/internal/service"
	"github.com/w4jnl/vink/internal/timefmt"
)

// Public status pages: no identity, no script, cacheable for 30 s.

type statusData struct {
	Title     string
	Slug      string
	Down      bool
	Banner    ui.HTML
	Groups    []statusGroup
	Incidents []statusIncident
	Updated   string
	Error     string
}

type statusGroup struct {
	Name  string
	Items []statusItem
}

type statusItem struct {
	Name  string
	Badge ui.HTML
	Bar   ui.HTML
}

type statusIncident struct{ Name, Since string }

func (h *Web) mountStatus(mux *http.ServeMux) {
	mux.Handle("GET /s/{slug}", h.publicPage(h.statusPage))
	mux.Handle("POST /s/{slug}", h.publicPage(h.statusUnlock))
	mux.Handle("GET /s/{slug}/badge/{file}", h.publicPage(h.statusBadge))
}

// publicPage serves a route that never reads identity: headers for a
// page without scripts that may sit in a same-origin frame.
func (h *Web) publicPage(fn func(w http.ResponseWriter, r *http.Request) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("Content-Security-Policy", "default-src 'none'; img-src 'self' data:; style-src 'self'; font-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'self'")
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("Referrer-Policy", "same-origin")
		hd.Set("X-Frame-Options", "SAMEORIGIN")
		if err := fn(w, r); err != nil {
			switch {
			case errors.Is(err, domain.ErrNotFound):
				hd.Set("Cache-Control", "no-store")
				http.Error(w, "not found", http.StatusNotFound)
			default:
				h.log.Error("status page error", "err", err, "path", r.URL.Path)
				http.Error(w, "something broke", http.StatusInternalServerError)
			}
		}
	})
}

// CustomDomains serves a page on its own host name: a request for / on
// that host is the page, /badge/... its badges.
func (h *Web) CustomDomains(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || strings.HasPrefix(r.URL.Path, "/badge/") {
			host := r.Host
			if hp, _, err := net.SplitHostPort(host); err == nil {
				host = hp
			}
			if page, err := h.svc.StatusPageByDomain(r.Context(), host); err == nil {
				r2 := r.Clone(r.Context())
				r2.URL.Path = "/s/" + page.Slug + strings.TrimSuffix(r.URL.Path, "/")
				r2.URL.RawPath = ""
				next.ServeHTTP(w, r2)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func statusCookie(slug string) string { return "vk_status_" + slug }

func (h *Web) unlocked(r *http.Request, page *domain.StatusPage) bool {
	if !page.HasPassword() {
		return true
	}
	c, err := r.Cookie(statusCookie(page.Slug))
	return err == nil && h.svc.VerifyStatusToken(page, c.Value)
}

func (h *Web) statusPage(w http.ResponseWriter, r *http.Request) error {
	page, err := h.svc.StatusPageBySlug(r.Context(), r.PathValue("slug"))
	if err != nil {
		return err
	}
	if !h.unlocked(r, page) {
		return h.renderLocked(w, page, "", http.StatusOK)
	}
	return h.renderStatus(w, r, page)
}

func (h *Web) statusUnlock(w http.ResponseWriter, r *http.Request) error {
	page, err := h.svc.StatusPageBySlug(r.Context(), r.PathValue("slug"))
	if err != nil {
		return err
	}
	if err := r.ParseForm(); err != nil {
		return err
	}
	if !h.svc.CheckStatusPassword(page, r.PostFormValue("password")) {
		return h.renderLocked(w, page, "Wrong password.", http.StatusUnauthorized)
	}
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // Secure follows the instance's base URL scheme; a plain-http homelab has no TLS to require
		Name: statusCookie(page.Slug), Value: h.svc.StatusToken(page), Path: "/", MaxAge: 24 * 60 * 60, HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Secure: r.TLS != nil || strings.HasPrefix(h.svc.Config().BaseURL, "https://"),
	})
	http.Redirect(w, r, "/s/"+page.Slug, http.StatusSeeOther) //nolint:gosec // the slug comes from the database row, validated on save
	return nil
}

// upLabel reads "99.9% up …", or "no data" alone.
func upLabel(pct, tail string) string {
	if pct == "no data" {
		return pct
	}
	return pct + " up" + tail
}

func (h *Web) renderLocked(w http.ResponseWriter, page *domain.StatusPage, msg string, status int) error {
	out, err := h.tmpl.Render("status", "status-locked", statusData{Title: page.Title, Slug: page.Slug, Error: msg})
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(out)
	return nil
}

func (h *Web) renderStatus(w http.ResponseWriter, r *http.Request, page *domain.StatusPage) error {
	now := h.now()
	st, err := h.svc.PublicStatus(r.Context(), page, now)
	if err != nil {
		return err
	}
	loc, err := time.LoadLocation(st.Project.Timezone)
	if err != nil {
		loc = time.UTC
	}
	d := statusData{Title: page.Title, Slug: page.Slug, Down: st.Down > 0, Updated: "updated " + timefmt.Clock(now, loc) + " " + now.In(loc).Format("MST")}
	d.Banner = statusBanner(st, loc)
	for _, g := range st.Groups {
		group := statusGroup{Name: g.Name}
		for _, m := range g.Monitors {
			cells := view.DayCells(now, m.CreatedAt, m.State, st.Events[m.ID])
			pct := view.UpPercent(now, m.CreatedAt, m.State, st.Events[m.ID], 90*24*time.Hour)
			group.Items = append(group.Items, statusItem{
				Name: m.Name, Badge: ui.StateBadge(ui.StateBadgeProps{State: string(m.State)}),
				Bar: ui.UptimeBar(cells, false, upLabel(pct, " over 90 days"), []string{"90 days ago", upLabel(pct, ""), "today"}),
			})
		}
		d.Groups = append(d.Groups, group)
	}
	for _, inc := range st.Incidents {
		d.Incidents = append(d.Incidents, statusIncident{Name: inc.MonitorName, Since: "since " + timefmt.Clock(inc.OpenedAt, loc)[:5]})
	}
	out, err := h.tmpl.Render("status", "status-page", d)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if page.HasPassword() {
		w.Header().Set("Cache-Control", "private, max-age=30")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=30")
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
	return nil
}

// statusBanner words the page's overall state.
func statusBanner(st *service.PublicStatus, loc *time.Location) ui.HTML {
	switch {
	case st.Down > 0:
		detail := ""
		var oldest *time.Time
		for _, inc := range st.Incidents {
			if oldest == nil || inc.OpenedAt.Before(*oldest) {
				t := inc.OpenedAt
				oldest = &t
			}
		}
		if oldest != nil {
			detail = "since " + timefmt.Clock(*oldest, loc)[:5]
		}
		return ui.StatusBanner("down", fmt.Sprintf("%d %s down", st.Down, plural2(st.Down, "service", "services")), detail)
	case st.Maintenance:
		return ui.StatusBanner("maintenance", "Maintenance", "")
	case st.Late > 0:
		return ui.StatusBanner("late", fmt.Sprintf("%d %s late", st.Late, plural2(st.Late, "service", "services")), "")
	}
	return ui.StatusBanner("up", "All systems operational", "")
}

// statusBadge answers /s/{slug}/badge/{monitor}.svg and .json.
func (h *Web) statusBadge(w http.ResponseWriter, r *http.Request) error {
	page, err := h.svc.StatusPageBySlug(r.Context(), r.PathValue("slug"))
	if err != nil {
		return err
	}
	if !h.unlocked(r, page) {
		return domain.NotFound("badge")
	}
	file := r.PathValue("file")
	slug, ext, ok := strings.Cut(file, ".")
	if !ok || (ext != "svg" && ext != "json") {
		return domain.NotFound("badge")
	}
	st, err := h.svc.PublicStatus(r.Context(), page, h.now())
	if err != nil {
		return err
	}
	var m *domain.Monitor
	for _, cand := range st.Monitors {
		if cand.Slug == slug {
			m = cand
		}
	}
	if m == nil {
		return domain.NotFound("badge")
	}
	w.Header().Set("Cache-Control", "public, max-age=30")
	if ext == "json" {
		w.Header().Set("Content-Type", "application/json")
		return json.NewEncoder(w).Encode(map[string]any{"schemaVersion": 1, "label": m.Name, "message": string(m.State), "color": badgeColorName(m.State)})
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	_, err = w.Write([]byte(badgeSVG(m.Name, string(m.State), badgeColor(m.State))))
	return err
}

func badgeColor(s domain.State) string {
	switch s {
	case domain.StateUp:
		return "#4c1"
	case domain.StateLate:
		return "#dfb317"
	case domain.StateDown:
		return "#e05d44"
	}
	return "#9f9f9f"
}

func badgeColorName(s domain.State) string {
	switch s {
	case domain.StateUp:
		return "brightgreen"
	case domain.StateLate:
		return "yellow"
	case domain.StateDown:
		return "red"
	}
	return "lightgrey"
}

// badgeSVG draws a flat two-part badge in the Shields layout.
func badgeSVG(label, value, color string) string {
	lw := len(label)*7 + 10
	vw := len(value)*7 + 10
	w := lw + vw
	e := esc
	return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="20" role="img" aria-label="%s: %s"><title>%s: %s</title>`+
		`<rect width="%d" height="20" fill="#555"/><rect x="%d" width="%d" height="20" fill="%s"/>`+
		`<g fill="#fff" text-anchor="middle" font-family="Verdana,Geneva,DejaVu Sans,sans-serif" font-size="11">`+
		`<text x="%d" y="14">%s</text><text x="%d" y="14">%s</text></g></svg>`,
		w, e(label), e(value), e(label), e(value), lw, lw, vw, color, lw/2, e(label), lw+vw/2, e(value))
}
