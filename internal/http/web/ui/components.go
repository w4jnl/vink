// Package ui renders the design system's components as HTML. Each function
// produces exactly the markup the matching function in
// docs/design-system/components/bundle.js returns for the same props;
// golden tests keep them equal. Values are escaped here, so the results
// are safe to place in templates as template.HTML.
package ui

import (
	"html/template"
	"strconv"
	"strings"
)

// HTML is a fragment that is already escaped.
type HTML = template.HTML

// esc escapes exactly the five characters bundle.js escapes.
func esc(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;").Replace(s)
}

var states = map[string]bool{"up": true, "late": true, "down": true, "paused": true, "new": true}

// st normalises a state word; unknown states render as new.
func st(s string) string {
	if states[s] {
		return s
	}
	return "new"
}

// Attr renders one extra attribute, escaped, with a leading space.
func Attr(name, value string) string {
	return " " + name + `="` + esc(value) + `"`
}

// Glyph is the state shape: disc, half disc, diamond, two bars, dashed ring.
func Glyph(state, extra string) HTML {
	cls := "vk-glyph vk-glyph--" + st(state)
	if extra != "" {
		cls += " " + extra
	}
	return HTML(`<i class="` + cls + `" aria-hidden="true"></i>`)
}

// StateBadgeProps: shape + colour + word for one state.
type StateBadgeProps struct {
	State string
	Pill  bool
	Label string
	Since string
}

// StateBadge renders the state as glyph, colour and word.
func StateBadge(p StateBadgeProps) HTML {
	s := st(p.State)
	var b strings.Builder
	b.WriteString(`<span class="vk-state vk-state--` + s)
	if p.Pill {
		b.WriteString(" vk-state--pill")
	}
	b.WriteString(`">`)
	b.WriteString(string(Glyph(s, "")))
	label := p.Label
	if label == "" {
		label = s
	}
	b.WriteString(esc(label))
	if p.Since != "" {
		b.WriteString(` <span class="vk-state__since">` + esc(p.Since) + `</span>`)
	}
	b.WriteString("</span>")
	return HTML(b.String())
}

// ButtonProps: a 32px action.
type ButtonProps struct {
	Label    string
	Variant  string // quiet (default), primary, danger
	Confirm  string
	Disabled bool
	// Type overrides type="button", for example submit.
	Type string
	// Attrs are extra attributes rendered as given (use Attr).
	Attrs string
}

// Button renders a button.
func Button(p ButtonProps) HTML {
	v := p.Variant
	if v == "" {
		v = "quiet"
	}
	typ := p.Type
	if typ == "" {
		typ = "button"
	}
	var b strings.Builder
	b.WriteString(`<button type="` + esc(typ) + `" class="vk-btn`)
	if v != "quiet" {
		b.WriteString(" vk-btn--" + esc(v))
	}
	b.WriteString(`"`)
	if p.Confirm != "" {
		b.WriteString(` data-confirm="` + esc(p.Confirm) + `"`)
	}
	if p.Disabled {
		b.WriteString(" disabled")
	}
	b.WriteString(p.Attrs)
	b.WriteString(">" + esc(p.Label) + "</button>")
	return HTML(b.String())
}

// ChipProps: a filter toggle.
type ChipProps struct {
	Label   string
	Count   *int
	Pressed bool
	State   string
	Type    string
	Attrs   string
}

// Count returns a pointer for ChipProps.Count.
func Count(n int) *int { return &n }

// Chip renders a filter chip.
func Chip(p ChipProps) HTML {
	typ := p.Type
	if typ == "" {
		typ = "button"
	}
	var b strings.Builder
	b.WriteString(`<button type="` + esc(typ) + `" class="vk-chip" aria-pressed="` + strconv.FormatBool(p.Pressed) + `"`)
	b.WriteString(p.Attrs)
	b.WriteString(">")
	if p.State != "" {
		b.WriteString(string(Glyph(p.State, "")))
	}
	b.WriteString(esc(p.Label))
	if p.Count != nil {
		b.WriteString(`<span class="vk-chip__n">` + strconv.Itoa(*p.Count) + `</span>`)
	}
	b.WriteString("</button>")
	return HTML(b.String())
}

// Tag renders a monitor tag.
func Tag(label string) HTML {
	return HTML(`<span class="vk-tag">` + esc(label) + `</span>`)
}

var kinds = map[string]string{
	"heartbeat": `<path d="M1.5 9 H4.4 L6.4 4 L9.4 12.5 L11.1 8 H14.5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" fill="none"/>`,
	"http":      `<g stroke="currentColor" stroke-width="1.5" fill="none"><circle cx="8" cy="8" r="5.75"/><ellipse cx="8" cy="8" rx="2.4" ry="5.75"/><path d="M2.4 8 H13.6" stroke-linecap="round"/></g>`,
	"tcp":       `<g stroke="currentColor" stroke-width="1.5" fill="none" stroke-linecap="round"><circle cx="3.6" cy="8" r="1.9"/><circle cx="12.4" cy="8" r="1.9"/><path d="M5.6 8 H10.4"/></g>`,
	"dns":       `<g stroke="currentColor" stroke-width="1.5" fill="none" stroke-linecap="round" stroke-linejoin="round"><path d="M5 2.2 V13.8"/><path d="M5 3.6 H11.2 L13.2 5.5 L11.2 7.4 H5"/></g>`,
	"tls":       `<g stroke="currentColor" stroke-width="1.5" fill="none" stroke-linecap="round" stroke-linejoin="round"><rect x="3.4" y="7.2" width="9.2" height="6.6" rx="1.6"/><path d="M5.6 7.2 V5.2 a2.4 2.4 0 0 1 4.8 0 V7.2"/></g>`,
	"icmp":      `<g stroke="currentColor" stroke-width="1.5" fill="none" stroke-linecap="round"><path d="M3.2 8.6 a4.2 4.2 0 0 1 4.2 4.2"/><path d="M3.2 4.4 a8.4 8.4 0 0 1 8.4 8.4"/></g><circle cx="3.6" cy="12.4" r="1.4" fill="currentColor"/>`,
}

// KindIcon renders the 16px monoline glyph for a kind.
func KindIcon(kind string) HTML {
	path, ok := kinds[kind]
	if !ok {
		kind, path = "http", kinds["http"]
	}
	return HTML(`<span class="vk-kind" title="` + kind + `"><svg viewBox="0 0 16 16" role="img" aria-label="` + kind + `">` + path + `</svg></span>`)
}

// FieldProps: label, input and one line of help.
type FieldProps struct {
	ID          string
	Label       string
	Value       string
	Placeholder string
	Hint        string
	Error       string
	Mono        bool
	// Name defaults to ID; Type defaults to text. Attrs are extra input
	// attributes. Textarea renders a textarea instead of an input.
	Name     string
	Type     string
	Attrs    string
	Textarea bool
}

// Field renders a form field.
func Field(p FieldProps) HTML {
	id := p.ID
	if id == "" {
		id = "f"
	}
	id = esc(id)
	hid := id + "-msg"
	var b strings.Builder
	b.WriteString(`<div class="vk-field`)
	if p.Error != "" {
		b.WriteString(" vk-field--error")
	}
	b.WriteString(`"><label class="vk-field__label" for="` + id + `">` + esc(p.Label) + `</label>`)
	cls := "vk-input"
	if p.Mono {
		cls += " vk-input--mono"
	}
	extra := ""
	if p.Name != "" {
		extra += Attr("name", p.Name)
	}
	if p.Type != "" && !p.Textarea {
		extra += Attr("type", p.Type)
	}
	describe := ""
	if p.Error != "" || p.Hint != "" {
		describe = ` aria-describedby="` + hid + `"`
	}
	invalid := ""
	if p.Error != "" {
		invalid = ` aria-invalid="true"`
	}
	if p.Textarea {
		b.WriteString(`<textarea class="` + cls + ` vk-input--area" id="` + id + `"` + extra + ` placeholder="` + esc(p.Placeholder) + `"` + describe + invalid + p.Attrs + `>` + esc(p.Value) + `</textarea>`)
	} else {
		b.WriteString(`<input class="` + cls + `" id="` + id + `"` + extra + ` value="` + esc(p.Value) + `" placeholder="` + esc(p.Placeholder) + `"` + describe + invalid + p.Attrs + `>`)
	}
	switch {
	case p.Error != "":
		b.WriteString(`<span class="vk-field__error" id="` + hid + `">` + string(Glyph("down", "")) + esc(p.Error) + `</span>`)
	case p.Hint != "":
		b.WriteString(`<span class="vk-field__hint" id="` + hid + `">` + esc(p.Hint) + `</span>`)
	}
	b.WriteString("</div>")
	return HTML(b.String())
}

// PingURL renders the ping URL with the slug highlighted and a Copy button.
// base ends with "/ping/".
func PingURL(base, key, slug string) HTML {
	if base == "" {
		base = "https://vink.example.com/ping/"
	}
	if key == "" {
		key = "k7f3q9x2mz"
	}
	if slug == "" {
		slug = "nightly-backup"
	}
	base, key, slug = esc(base), esc(key), esc(slug)
	url := base + key + "/" + slug
	return HTML(`<div class="vk-ping"><code class="vk-ping__url">` + base + key + `/<b>` + slug + `</b></code><button type="button" class="vk-btn vk-copy" data-copy="` + url + `">Copy</button></div>`)
}

func f1(v float64) string { return strconv.FormatFloat(v, 'f', 1, 64) }

// Sparkline renders a 96x20 latency trend with the last point marked.
func Sparkline(points []float64, state string) HTML {
	const w, h = 96.0, 20.0
	n := len(points)
	if n == 0 {
		return HTML(`<svg class="vk-spark" viewBox="0 0 96 20" aria-hidden="true"></svg>`)
	}
	mn, mx := points[0], points[0]
	for _, v := range points {
		mn, mx = min(mn, v), max(mx, v)
	}
	span := mx - mn
	if span == 0 {
		span = 1
	}
	xy := make([]string, n)
	for i, v := range points {
		x := w
		if n > 1 {
			x = float64(i) * w / float64(n-1)
		}
		xy[i] = f1(x) + "," + f1(h-2-(v-mn)/span*(h-4))
	}
	cls := "vk-spark"
	if state != "" && state != "up" {
		cls += " vk-spark--" + st(state)
	}
	last := strings.SplitN(xy[n-1], ",", 2)
	return HTML(`<svg class="` + cls + `" viewBox="0 0 96 20" preserveAspectRatio="none" aria-hidden="true"><polyline points="` + strings.Join(xy, " ") +
		`" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linejoin="round" stroke-linecap="round" vector-effect="non-scaling-stroke"/><circle cx="` + last[0] + `" cy="` + last[1] + `" r="2" fill="currentColor"/></svg>`)
}

// MonitorRowProps: one monitor in the list.
type MonitorRowProps struct {
	State   string
	Name    string
	Slug    string
	Kind    string
	Last    string
	LastAbs string
	Next    string
	Points  []float64 // nil for heartbeats
	Tags    []string
	Href    string
	Current bool
	// Attrs are extra anchor attributes, for htmx.
	Attrs string
}

// MonitorRow renders one list row.
func MonitorRow(p MonitorRowProps) HTML {
	s := st(p.State)
	href := p.Href
	if href == "" {
		href = "#"
	}
	var b strings.Builder
	b.WriteString(`<a class="vk-row" href="` + esc(href) + `"`)
	if p.Current {
		b.WriteString(` aria-current="true"`)
	}
	b.WriteString(p.Attrs)
	b.WriteString(">")
	b.WriteString(string(Glyph(s, "vk-row__glyph")))
	b.WriteString(`<span class="vk-row__name"><span>` + esc(p.Name) + `</span><span class="vk-row__slug">` + esc(p.Slug) + `</span></span>`)
	b.WriteString(string(KindIcon(p.Kind)))
	lastCls := ""
	if s == "down" || s == "late" {
		lastCls = " vk-row__data--" + s
	}
	b.WriteString(`<span class="vk-row__data` + lastCls + `" title="` + esc(p.LastAbs) + `">` + esc(p.Last) + `</span>`)
	if p.Points != nil {
		b.WriteString(string(Sparkline(p.Points, s)))
	} else {
		b.WriteString(`<span class="vk-row__data">` + esc(p.Next) + `</span>`)
	}
	b.WriteString(`<span class="vk-row__tags">`)
	for _, t := range p.Tags {
		b.WriteString(string(Tag(t)))
	}
	b.WriteString("</span></a>")
	return HTML(b.String())
}

// UptimeBar renders one cell per period: up, late, down or none.
func UptimeBar(cells []string, compact bool, label string, legend []string) HTML {
	var b strings.Builder
	b.WriteString(`<div><div class="vk-uptime`)
	if compact {
		b.WriteString(" vk-uptime--compact")
	}
	if label == "" {
		label = strconv.Itoa(len(cells)) + " days"
	}
	b.WriteString(`" role="img" aria-label="` + esc(label) + `">`)
	for _, c := range cells {
		b.WriteString(`<span class="vk-uptime__cell`)
		if c != "up" {
			b.WriteString(" vk-uptime__cell--" + esc(c))
		}
		b.WriteString(`"></span>`)
	}
	b.WriteString("</div>")
	if len(legend) == 3 {
		b.WriteString(`<div class="vk-uptime__legend"><span>` + esc(legend[0]) + `</span><span>` + esc(legend[1]) + `</span><span>` + esc(legend[2]) + `</span></div>`)
	}
	b.WriteString("</div>")
	return HTML(b.String())
}

// StatusBanner renders the status page headline.
func StatusBanner(state, text, detail string) HTML {
	if state == "" {
		state = "up"
	}
	g := state
	if state == "maintenance" {
		g = "paused"
	}
	var b strings.Builder
	b.WriteString(`<div class="vk-banner vk-banner--` + esc(state) + `" role="status">` + string(Glyph(g, "")) + `<span>` + esc(text) + `</span>`)
	if detail != "" {
		b.WriteString(`<span class="vk-banner__detail">` + esc(detail) + `</span>`)
	}
	b.WriteString("</div>")
	return HTML(b.String())
}

// EmptyState renders the first-run panel with the ping URL in a crontab line.
func EmptyState(base, key string) HTML {
	if base == "" {
		base = "https://vink.example.com/ping/"
	}
	if key == "" {
		key = "k7f3q9x2mz"
	}
	url := esc(base) + esc(key)
	return HTML(`<div class="vk-empty"><h3>No monitors yet</h3><p>Point a cron job at your project’s ping URL. The first ping creates the monitor.</p>` +
		`<pre class="vk-code"><i># every night at 03:00, then tell vink it ran</i>` + "\n" + `0 3 * * * restic backup &amp;&amp; curl -fsS <b>` + url + `/nightly-backup?create=1</b></pre>` +
		`<p>Or run <span class="vk-mono">vink apply -f vink.yaml</span> to declare them all at once.</p></div>`)
}

const markPaths = `<g stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" fill="none"><path d="M8 3.2 H6 a2.2 2.2 0 0 0 -2.2 2.2 V18.6 a2.2 2.2 0 0 0 2.2 2.2 H8"/><path d="M16 3.2 H18 a2.2 2.2 0 0 1 2.2 2.2 V18.6 a2.2 2.2 0 0 1 -2.2 2.2 H16"/></g>` +
	`<path d="M7.5 10.1 L10.5 12.9 L16.5 7.1" stroke="currentColor" stroke-width="2.9" stroke-linecap="round" stroke-linejoin="round" fill="none"/><rect x="9.6" y="16.2" width="4.8" height="2.2" rx="1.1" fill="currentColor"/>`

// Mark renders the vink mark as inline SVG in currentColor.
func Mark(size int) HTML {
	if size <= 0 {
		size = 24
	}
	s := strconv.Itoa(size)
	return HTML(`<svg viewBox="0 0 24 24" width="` + s + `" height="` + s + `" aria-hidden="true">` + markPaths + `</svg>`)
}
