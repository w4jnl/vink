package web

import (
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

// The history page is one timeline of a monitor's observations and state
// changes, newest first and grouped by day, with a kind filter, a period
// or an exact window, and an Older link that loads itself when it scrolls
// into view. The head polls; the stream is a snapshot and the head offers
// a reload when newer rows exist.

const historyPageSize = 50

var historyPeriods = []struct {
	key, label string
	span       time.Duration
}{
	{"24h", "24 h", 24 * time.Hour},
	{"7d", "7 d", 7 * 24 * time.Hour},
	{"30d", "30 d", 30 * 24 * time.Hour},
	{"90d", "90 d", 90 * 24 * time.Hour},
}

const historyDefaultPeriod = "7d"

var historyKinds = []struct{ value, label, state string }{
	{"", "All", ""},
	{service.KindOK, "ok", "up"},
	{service.KindFail, "failures", "down"},
	{service.KindRun, "runs", "new"},
	{service.KindChange, "changes", ""},
}

// historyQuery is the page's URL: the filters, the cursor of the page being
// loaded, and the two hints the partials carry.
type historyQuery struct {
	kind   string
	period string    // one of historyPeriods, or "" for an exact window
	since  time.Time // the exact window; zero when a period is set
	until  time.Time
	before string // "<millis>.<id>": the row the previous page ended on
	at     time.Time
	id     string
	day    string // rows partial: the day heading already on screen
	newest string // head partial: the stream's newest row, or "none"
}

// parseHistoryQuery reads the URL. A period wins over an exact window; an
// exact window needs at least one parseable bound; a malformed cursor is a
// page that does not exist.
func parseHistoryQuery(q url.Values) (historyQuery, error) {
	var h historyQuery
	if k := q.Get("kind"); service.ValidHistoryKind(k) {
		h.kind = k
	}
	for _, p := range historyPeriods {
		if q.Get("period") == p.key {
			h.period = p.key
		}
	}
	if h.period == "" {
		h.since, _ = parseTime(q.Get("since"))
		h.until, _ = parseTime(q.Get("until"))
		if h.since.IsZero() && h.until.IsZero() {
			h.period = historyDefaultPeriod
		}
	}
	if b := q.Get("before"); b != "" {
		ms, id, ok := strings.Cut(b, ".")
		n, err := strconv.ParseInt(ms, 10, 64)
		if !ok || err != nil || !domain.IsID(id) {
			return h, domain.NotFound("page")
		}
		h.before, h.at, h.id = b, domain.FromMillis(n), id
	}
	h.day = q.Get("day")
	h.newest = q.Get("newest")
	return h, nil
}

// parseTime accepts RFC 3339 or Unix seconds, as the API does.
func parseTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil && n > 0 {
		return time.Unix(n, 0).UTC(), true
	}
	return time.Time{}, false
}

// window is the time range the query asks for; a zero until is open-ended.
func (q historyQuery) window(now time.Time) (since, until time.Time) {
	for _, p := range historyPeriods {
		if p.key == q.period {
			return now.Add(-p.span), time.Time{}
		}
	}
	return q.since, q.until
}

// values rebuilds the filters as a query, without the cursor or the hints,
// so links and the poll keep what the person chose.
func (q historyQuery) values() url.Values {
	v := url.Values{}
	if q.kind != "" {
		v.Set("kind", q.kind)
	}
	switch {
	case q.period != "":
		v.Set("period", q.period)
	default:
		if !q.since.IsZero() {
			v.Set("since", q.since.Format(time.RFC3339))
		}
		if !q.until.IsZero() {
			v.Set("until", q.until.Format(time.RFC3339))
		}
	}
	return v
}

func (q historyQuery) page() service.HistoryPage {
	return service.HistoryPage{Kind: q.kind, CursorAt: q.at, CursorID: q.id}
}

type historyData struct {
	base
	Head   historyHead
	Stream historyStream
}

type historyHead struct {
	Path, HistoryPath, PollPath, ReloadPath, CSRF string
	Name, Slug                                    string
	Badge                                         ui.StateBadgeProps
	Tags                                          []string
	Pull, Paused                                  bool
	Summary                                       string
	HourCells, HourLegend, DayCells, DayLegend    []string
	Notice                                        bool
}

type historyStream struct {
	PagePath     string
	Chips        []ui.ChipProps
	Period       string
	Since, Until string
	Groups       []historyDay
	Older        *historyOlder
	Empty        string
	NewestKey    string
}

type historyDay struct {
	Label, Date, Key, Href string
	Continued              bool
	Rows                   []obsRow
}

// historyOlder is the sentinel: Page is the plain link, Rows the partial
// the sentinel fetches when it scrolls into view.
type historyOlder struct{ Page, Rows string }

func (h *Web) history(c *reqCtx) error {
	ctx := c.r.Context()
	m, err := h.svc.MonitorBySlug(ctx, c.scope, c.r.PathValue("slug"))
	if err != nil {
		return err
	}
	q, err := parseHistoryQuery(c.r.URL.Query())
	if err != nil {
		return err
	}
	partial := c.r.URL.Query().Get("partial")
	if partial == "head" {
		head, err := h.historyHead(c, m, q, q.newest, q.newest != "")
		if err != nil {
			return err
		}
		return h.render(c, http.StatusOK, "history", "history-head", head)
	}
	stream, err := h.historyStream(c, m, q)
	if err != nil {
		return err
	}
	switch {
	case partial == "rows":
		return h.render(c, http.StatusOK, "history", "history-rows", stream)
	case partial == "stream" || c.htmx():
		return h.render(c, http.StatusOK, "history", "history-stream", stream)
	}
	head, err := h.historyHead(c, m, q, stream.NewestKey, false)
	if err != nil {
		return err
	}
	d := historyData{base: h.baseFor(c, m.Name+" · history", "monitors"), Head: head, Stream: stream}
	return h.render(c, http.StatusOK, "history", "layout", d)
}

// historyHead builds the top of the page. newest is the stream's first
// row, carried on the poll URL; when check is set the head compares it
// with the current first row and offers a reload if they differ.
func (h *Web) historyHead(c *reqCtx, m *domain.Monitor, q historyQuery, newest string, check bool) (historyHead, error) {
	ctx := c.r.Context()
	loc := h.location(c, m)
	path := c.projectPath() + "/m/" + m.Slug
	d := historyHead{
		Path: path, HistoryPath: path + "/history", CSRF: c.csrf(), Name: m.Name, Slug: m.Slug,
		Badge: ui.StateBadgeProps{State: string(m.State), Pill: true}, Tags: m.Tags, Pull: m.Pull != nil, Paused: m.Paused,
		Summary: h.summaryLine(c, m, loc),
	}
	if m.State != domain.StateNew {
		d.Badge.Since = view.For(m.StateSince, c.now)
	}
	events, err := h.svc.EventsSince(ctx, c.scope, m, c.now.Add(-90*24*time.Hour))
	if err != nil {
		return d, err
	}
	d.HourCells = view.HourCells(c.now, m.CreatedAt, m.State, events)
	d.HourLegend = []string{"24 h ago", upLabel(view.UpPercent(c.now, m.CreatedAt, m.State, events, 24*time.Hour), ""), "now"}
	d.DayCells = view.DayCells(c.now, m.CreatedAt, m.State, events)
	d.DayLegend = []string{"90 days ago", upLabel(view.UpPercent(c.now, m.CreatedAt, m.State, events, 90*24*time.Hour), ""), "today"}
	v := q.values()
	d.ReloadPath = d.HistoryPath + "?" + v.Encode()
	if check {
		since, until := q.window(c.now)
		p := q.page()
		p.Since, p.Until, p.CursorAt, p.CursorID, p.Limit = since, until, time.Time{}, "", 1
		res, err := h.svc.History(ctx, c.scope, m.Slug, p)
		if err != nil {
			return d, err
		}
		d.Notice = historyKey(res) != newest
	}
	v.Set("partial", "head")
	if newest != "" {
		v.Set("newest", newest)
	}
	d.PollPath = d.HistoryPath + "?" + v.Encode()
	return d, nil
}

// historyKey names the newest row of a page: "<millis>.<id>", or "none".
func historyKey(res service.HistoryResult) string {
	if len(res.Items) == 0 {
		return "none"
	}
	return strconv.FormatInt(domain.Millis(res.Items[0].At), 10) + "." + res.Items[0].ID
}

// historyStream builds the filter bar and one page of rows.
func (h *Web) historyStream(c *reqCtx, m *domain.Monitor, q historyQuery) (historyStream, error) {
	ctx := c.r.Context()
	loc := h.location(c, m)
	path := c.projectPath() + "/m/" + m.Slug + "/history"
	d := historyStream{PagePath: path, Period: q.period}
	if q.period == "" {
		if !q.since.IsZero() {
			d.Since = q.since.Format(time.RFC3339)
		}
		if !q.until.IsZero() {
			d.Until = q.until.Format(time.RFC3339)
		}
	}
	for _, k := range historyKinds {
		pressed := q.kind == k.value
		value := k.value
		if pressed && value != "" {
			value = "" // clicking the pressed chip clears it
		}
		d.Chips = append(d.Chips, ui.ChipProps{Label: k.label, State: k.state, Pressed: pressed, Type: "submit", Attrs: ui.Attr("name", "kind") + ui.Attr("value", value)})
	}
	since, until := q.window(c.now)
	p := q.page()
	p.Since, p.Until, p.Limit = since, until, historyPageSize
	res, err := h.svc.History(ctx, c.scope, m.Slug, p)
	if err != nil {
		return d, err
	}
	if q.before == "" {
		d.NewestKey = historyKey(res)
	}
	match := ""
	if m.Pull != nil && m.Pull.HTTP != nil {
		match = bodyMatch(m.Pull.HTTP)
	}
	for _, it := range res.Items {
		label, date, key := view.DayLabel(it.At, c.now, loc)
		if len(d.Groups) == 0 || d.Groups[len(d.Groups)-1].Key != key {
			dayStart := time.Date(it.At.In(loc).Year(), it.At.In(loc).Month(), it.At.In(loc).Day(), 0, 0, 0, 0, loc)
			dv := url.Values{}
			if q.kind != "" {
				dv.Set("kind", q.kind)
			}
			dv.Set("since", dayStart.Format(time.RFC3339))
			dv.Set("until", dayStart.AddDate(0, 0, 1).Add(-time.Millisecond).Format(time.RFC3339))
			g := historyDay{Label: label, Date: date, Key: key, Href: path + "?" + dv.Encode()}
			g.Continued = len(d.Groups) == 0 && key == q.day
			d.Groups = append(d.Groups, g)
		}
		g := &d.Groups[len(d.Groups)-1]
		if it.Obs != nil {
			row := obsRowFor(it.Obs, loc, match)
			if it.Obs.HasBody {
				row.BodyPath = c.projectPath() + "/m/" + m.Slug + "/obs/" + it.Obs.ID + "/body"
			}
			g.Rows = append(g.Rows, row)
		} else {
			g.Rows = append(g.Rows, eventObsRow(it.Event, loc))
		}
	}
	if res.More {
		next := q.values()
		next.Set("before", strconv.FormatInt(domain.Millis(res.NextAt), 10)+"."+res.NextID)
		page := path + "?" + next.Encode()
		next.Set("partial", "rows")
		next.Set("day", d.Groups[len(d.Groups)-1].Key)
		d.Older = &historyOlder{Page: page, Rows: path + "?" + next.Encode()}
	}
	if len(res.Items) == 0 && q.before == "" {
		d.Empty = historyEmpty(q)
	}
	return d, nil
}

// eventObsRow renders a state change in the row grid the observations use.
func eventObsRow(e *domain.Event, loc *time.Location) obsRow {
	row := obsRow{State: string(e.To), Clock: view.Clock(e.At, loc), Abs: view.Abs(e.At, loc), Text: string(e.From) + " → " + string(e.To)}
	if e.Reason != "" {
		row.Text += " · " + e.Reason
	}
	return row
}

// historyEmpty words an empty page: "No failures in the last 7 days".
func historyEmpty(q historyQuery) string {
	what := "Nothing"
	for _, k := range historyKinds {
		if k.value == q.kind && k.value != "" {
			what = "No " + k.label
		}
	}
	for _, p := range historyPeriods {
		if p.key == q.period {
			span := strings.Replace(p.label, " h", " hours", 1)
			span = strings.Replace(span, " d", " days", 1)
			return what + " in the last " + span
		}
	}
	return what + " in this window"
}
