// Package ui renders the design system's components as HTML. Each function
// produces exactly the markup the matching function in
// docs/design-system/components/bundle.js returns for the same props;
// golden tests keep them equal. Values are escaped here, so the results
// are safe to place in templates as template.HTML.
package ui

import (
	"html/template"
	"regexp"
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
	// Href renders an anchor styled as a button.
	Href string
	// Type is button (default) or submit.
	Type string
	// Block fills the width.
	Block bool
	// Attrs are extra attributes rendered as given (use Attr).
	Attrs string
}

// Button renders a button, or an anchor when Href is set.
func Button(p ButtonProps) HTML {
	v := p.Variant
	if v == "" {
		v = "quiet"
	}
	cls := "vk-btn"
	if v != "quiet" {
		cls += " vk-btn--" + esc(v)
	}
	if p.Block {
		cls += " vk-btn--block"
	}
	if p.Href != "" {
		return HTML(`<a class="` + cls + `" href="` + esc(p.Href) + `"` + p.Attrs + `>` + esc(p.Label) + `</a>`)
	}
	typ := "button"
	if p.Type == "submit" {
		typ = "submit"
	}
	var b strings.Builder
	b.WriteString(`<button type="` + typ + `" class="` + cls + `"`)
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

// Option is a select or segmented choice.
type Option struct {
	Value string
	Label string
}

// Opts builds options from value, label pairs; a lone value is its own label.
func Opts(kv ...string) []Option {
	out := make([]Option, 0, (len(kv)+1)/2)
	for i := 0; i < len(kv); i += 2 {
		o := Option{Value: kv[i], Label: kv[i]}
		if i+1 < len(kv) {
			o.Label = kv[i+1]
		}
		out = append(out, o)
	}
	return out
}

func options(list []Option, value string) string {
	var b strings.Builder
	for _, o := range list {
		b.WriteString(`<option value="` + esc(o.Value) + `"`)
		if o.Value == value {
			b.WriteString(" selected")
		}
		b.WriteString(">" + esc(o.Label) + "</option>")
	}
	return b.String()
}

// FieldProps: label, control and one line of help.
type FieldProps struct {
	ID          string
	Name        string
	Label       string
	Value       string
	Placeholder string
	Hint        string
	Error       string
	Mono        bool
	// Control is input (default), select or textarea.
	Control string
	Options []Option
	Rows    int
	// Type is the input type: text (default), password, url, email, search.
	Type         string
	Prefix       string
	Suffix       string
	Disabled     bool
	Autocomplete string
	// HTML replaces the control; Before and After sit beside it. All trusted.
	HTML   HTML
	Before HTML
	After  HTML
	// Attrs are extra control attributes rendered as given (use Attr).
	Attrs string
}

// Field renders a form field.
func Field(p FieldProps) HTML {
	id := p.ID
	if id == "" {
		id = "f"
	}
	id = esc(id)
	hid := ""
	if p.ID != "" {
		hid = id + "-msg"
	}
	name := p.Name
	if name == "" {
		name = p.ID
	}
	if name == "" {
		name = "f"
	}
	attrs := ` id="` + id + `" name="` + esc(name) + `"`
	if (p.Error != "" || p.Hint != "") && hid != "" {
		attrs += ` aria-describedby="` + hid + `"`
	}
	if p.Error != "" {
		attrs += ` aria-invalid="true"`
	}
	if p.Disabled {
		attrs += " disabled"
	}
	cls := "vk-input"
	if p.Mono {
		cls += " vk-input--mono"
	}
	var el string
	switch p.Control {
	case "select":
		el = `<span class="vk-select"><select class="` + cls + `"` + attrs + p.Attrs + `>` + options(p.Options, p.Value) + `</select></span>`
	case "textarea":
		rows := p.Rows
		if rows == 0 {
			rows = 3
		}
		el = `<textarea class="` + cls + ` vk-input--area"` + attrs + ` rows="` + strconv.Itoa(rows) + `" placeholder="` + esc(p.Placeholder) + `"` + p.Attrs + `>` + esc(p.Value) + `</textarea>`
	default:
		typ := p.Type
		if typ == "" {
			typ = "text"
		}
		el = `<input class="` + cls + `"` + attrs + ` type="` + esc(typ) + `" value="` + esc(p.Value) + `" placeholder="` + esc(p.Placeholder) + `"`
		if p.Autocomplete != "" {
			el += ` autocomplete="` + esc(p.Autocomplete) + `"`
		}
		el += p.Attrs + ">"
		if p.Prefix != "" || p.Suffix != "" {
			wrapped := `<span class="vk-affix">`
			if p.Prefix != "" {
				wrapped += `<span class="vk-affix__text">` + esc(p.Prefix) + `</span>`
			}
			wrapped += el
			if p.Suffix != "" {
				wrapped += `<span class="vk-affix__text">` + esc(p.Suffix) + `</span>`
			}
			el = wrapped + "</span>"
		}
	}
	if p.HTML != "" {
		el = string(p.HTML)
	}
	if p.Before != "" || p.After != "" {
		el = `<div class="vk-field__row">` + string(p.Before) + el + string(p.After) + "</div>"
	}
	var b strings.Builder
	b.WriteString(`<div class="vk-field`)
	if p.Error != "" {
		b.WriteString(" vk-field--error")
	}
	b.WriteString(`">`)
	if p.HTML != "" {
		b.WriteString(`<span class="vk-field__label">` + esc(p.Label) + `</span>`)
	} else {
		b.WriteString(`<label class="vk-field__label" for="` + id + `">` + esc(p.Label) + `</label>`)
	}
	b.WriteString(el)
	idAttr := ""
	if hid != "" {
		idAttr = ` id="` + hid + `"`
	}
	switch {
	case p.Error != "":
		b.WriteString(`<span class="vk-field__error"` + idAttr + `>` + string(Glyph("down", "")) + esc(p.Error) + `</span>`)
	case p.Hint != "":
		b.WriteString(`<span class="vk-field__hint"` + idAttr + `>` + esc(p.Hint) + `</span>`)
	}
	b.WriteString("</div>")
	return HTML(b.String())
}

// FieldRow puts two to four rendered fields side by side.
func FieldRow(fields []HTML, lead bool) HTML {
	cls := "vk-fieldrow"
	switch {
	case lead:
		cls += " vk-fieldrow--lead"
	case len(fields) > 2:
		cls += " vk-fieldrow--" + strconv.Itoa(len(fields))
	}
	var b strings.Builder
	b.WriteString(`<div class="` + cls + `">`)
	for _, f := range fields {
		b.WriteString(string(f))
	}
	b.WriteString("</div>")
	return HTML(b.String())
}

// CheckboxProps: a native checkbox with label and optional hint.
type CheckboxProps struct {
	Label     string
	LabelHTML HTML
	Name      string
	ID        string
	Value     string
	Checked   bool
	Disabled  bool
	Hint      string
	Attrs     string
}

// Checkbox renders a checkbox.
func Checkbox(p CheckboxProps) HTML {
	name := p.Name
	if name == "" {
		name = p.ID
	}
	if name == "" {
		name = "c"
	}
	var b strings.Builder
	b.WriteString(`<label class="vk-check"><input type="checkbox" name="` + esc(name) + `"`)
	if p.Value != "" {
		b.WriteString(` value="` + esc(p.Value) + `"`)
	}
	if p.Checked {
		b.WriteString(" checked")
	}
	if p.Disabled {
		b.WriteString(" disabled")
	}
	b.WriteString(p.Attrs)
	b.WriteString(`><span class="vk-check__text">`)
	if p.LabelHTML != "" {
		b.WriteString(string(p.LabelHTML))
	} else {
		b.WriteString(esc(p.Label))
	}
	if p.Hint != "" {
		b.WriteString(`<span class="vk-check__hint">` + esc(p.Hint) + `</span>`)
	}
	b.WriteString("</span></label>")
	return HTML(b.String())
}

// Switch renders an immediate on/off control; attrs carry hx-post.
func Switch(checked bool, label string, attrs string) HTML {
	if label == "" {
		label = "Enabled"
	}
	word := "off"
	if checked {
		word = "on"
	}
	return HTML(`<button type="button" class="vk-switch" role="switch" aria-checked="` + strconv.FormatBool(checked) + `" aria-label="` + esc(label) + `"` + attrs + `>` +
		`<span class="vk-switch__track" aria-hidden="true"></span><span class="vk-switch__word">` + word + `</span></button>`)
}

// SegmentedProps: a radio or checkbox group drawn as one control.
type SegmentedProps struct {
	Name    string
	Label   string
	Options []Option
	Value   []string
	Multi   bool
	Mono    bool
	// Attrs are extra attributes on every input, for htmx triggers.
	Attrs string
}

// Segmented renders the group.
func Segmented(p SegmentedProps) HTML {
	typ, role := "radio", "radiogroup"
	if p.Multi {
		typ, role = "checkbox", "group"
	}
	name := p.Name
	if name == "" {
		name = "seg"
	}
	cls := "vk-seg"
	if p.Mono {
		cls += " vk-seg--mono"
	}
	var b strings.Builder
	b.WriteString(`<div class="` + cls + `" role="` + role + `" aria-label="` + esc(p.Label) + `">`)
	for _, o := range p.Options {
		b.WriteString(`<label class="vk-seg__opt"><input type="` + typ + `" name="` + esc(name) + `" value="` + esc(o.Value) + `"`)
		for _, v := range p.Value {
			if v == o.Value {
				b.WriteString(" checked")
				break
			}
		}
		b.WriteString(p.Attrs)
		b.WriteString(`><span>` + esc(o.Label) + `</span></label>`)
	}
	b.WriteString("</div>")
	return HTML(b.String())
}

// KindInfo lists the six kinds with their card texts.
var KindInfo = [][3]string{
	{"heartbeat", "Heartbeat", "jobs ping vink"}, {"http", "HTTP", "requests a URL"}, {"tcp", "TCP", "opens a port"},
	{"dns", "DNS", "resolves a name"}, {"tls", "TLS certificate", "checks expiry"}, {"icmp", "ICMP ping", "pings a host"},
}

// KindPicker renders the six kinds as radio cards; locked disables the
// others on edit. attrs go on every radio, for the htmx kind switch.
func KindPicker(value string, locked bool, attrs string) HTML {
	if value == "" {
		value = "heartbeat"
	}
	var b strings.Builder
	b.WriteString(`<fieldset class="vk-kinds"><legend class="vk-field__label">Kind</legend><div class="vk-kinds__grid">`)
	for _, k := range KindInfo {
		b.WriteString(`<label class="vk-kindopt"><input type="radio" name="kind" value="` + k[0] + `"`)
		if k[0] == value {
			b.WriteString(" checked")
		}
		if locked && k[0] != value {
			b.WriteString(" disabled")
		}
		b.WriteString(attrs)
		b.WriteString(`><span class="vk-kindopt__card">` + string(KindIcon(k[0])) + `<span class="vk-kindopt__name">` + k[1] + `</span><span class="vk-kindopt__desc">` + k[2] + `</span></span></label>`)
	}
	b.WriteString("</div></fieldset>")
	return HTML(b.String())
}

// Disclosure renders a details element with a mono summary.
func Disclosure(title, summary string, body HTML, open bool) HTML {
	if title == "" {
		title = "Advanced"
	}
	var b strings.Builder
	b.WriteString(`<details class="vk-details"`)
	if open {
		b.WriteString(" open")
	}
	b.WriteString(`><summary><span class="vk-details__title">` + esc(title) + `</span>`)
	if summary != "" {
		b.WriteString(`<span class="vk-details__sum">` + esc(summary) + `</span>`)
	}
	b.WriteString(`</summary><div class="vk-details__body">` + string(body) + `</div></details>`)
	return HTML(b.String())
}

var toneGlyph = map[string]string{"ok": "up", "error": "down", "warn": "late"}

// Notice renders an inline result: ok, error, warn or info.
func Notice(tone, title, text string, html HTML) HTML {
	if tone == "" {
		tone = "info"
	}
	role := "status"
	if tone == "error" {
		role = "alert"
	}
	var b strings.Builder
	b.WriteString(`<div class="vk-notice vk-notice--` + esc(tone) + `" role="` + role + `">`)
	if g, ok := toneGlyph[tone]; ok {
		b.WriteString(string(Glyph(g, "")))
	}
	b.WriteString(`<div class="vk-notice__text"><p>`)
	if title != "" {
		b.WriteString(`<b class="vk-notice__title">` + esc(title) + `</b> `)
	}
	b.WriteString(esc(text) + "</p>" + string(html) + "</div></div>")
	return HTML(b.String())
}

var yamlKeyRe = regexp.MustCompile(`^(\s*(?:- )?)([\w.-]+:)`)

// Code renders read-only code with an optional Copy; yaml dims the keys.
func Code(text string, copy bool, copyLabel string, yaml bool) HTML {
	body := esc(text)
	if yaml {
		lines := strings.Split(body, "\n")
		for i, l := range lines {
			lines[i] = yamlKeyRe.ReplaceAllString(l, "$1<i>$2</i>")
		}
		body = strings.Join(lines, "\n")
	}
	var b strings.Builder
	b.WriteString(`<div class="vk-codebox"><pre class="vk-code">` + body + `</pre>`)
	if copy {
		if copyLabel == "" {
			copyLabel = "Copy"
		}
		b.WriteString(`<button type="button" class="vk-btn vk-copy" data-copy="` + esc(text) + `">` + esc(copyLabel) + `</button>`)
	}
	b.WriteString("</div>")
	return HTML(b.String())
}

// Panel renders an inline add or edit form box.
func Panel(title, note string, body, actions HTML, id string) HTML {
	var b strings.Builder
	b.WriteString(`<section class="vk-panel"`)
	if id != "" {
		b.WriteString(` id="` + esc(id) + `"`)
	}
	b.WriteString(">")
	if title != "" {
		b.WriteString(`<div class="vk-panel__head"><h2>` + esc(title) + `</h2>`)
		if note != "" {
			b.WriteString("<p>" + esc(note) + "</p>")
		}
		b.WriteString("</div>")
	}
	b.WriteString(string(body))
	if actions != "" {
		b.WriteString(`<div class="vk-actions">` + string(actions) + `</div>`)
	}
	b.WriteString("</section>")
	return HTML(b.String())
}

// TopBarProps: the signed-in header.
type TopBarProps struct {
	Org       string
	Project   string
	Section   string // monitors (default), incidents, settings
	Incidents int
	User      string
	Hrefs     map[string]string // home, monitors, incidents, settings
	// CrumbAttrs, SearchAttrs and UserAttrs are extra attributes for the
	// app's forms; goldens leave them empty.
	CrumbAttrs  string
	SearchAttrs string
	UserAttrs   string
}

// TopBar renders the header.
func TopBar(p TopBarProps) HTML {
	org, proj := p.Org, p.Project
	if org == "" {
		org = "w4j"
	}
	if proj == "" {
		proj = "homelab"
	}
	org, proj = esc(org), esc(proj)
	cur := p.Section
	if cur == "" {
		cur = "monitors"
	}
	base := "/o/" + org + "/p/" + proj
	href := func(id, def string) string {
		if v, ok := p.Hrefs[id]; ok && v != "" {
			return esc(v)
		}
		return def
	}
	link := func(id, label, def, extra string) string {
		s := `<a class="vk-top__link" href="` + href(id, def) + `"`
		if cur == id {
			s += ` aria-current="page"`
		}
		return s + ">" + label + extra + "</a>"
	}
	user := p.User
	if user == "" {
		user = "j"
	}
	initial := strings.ToUpper(string([]rune(user)[0]))
	count := ""
	if p.Incidents > 0 {
		n := strconv.Itoa(p.Incidents)
		count = `<span class="vk-top__count" title="` + n + ` open">` + string(Glyph("down", "")) + n + `</span>`
	}
	return HTML(`<header class="vk-top"><a class="vk-top__mark" href="` + href("home", "/") + `">` + string(Mark(22)) + `<span>vink</span></a>` +
		`<nav class="vk-top__nav" aria-label="Project"><button type="button" class="vk-top__crumb" aria-haspopup="menu"` + p.CrumbAttrs + `>` + org + ` / <b>` + proj + `</b><i class="vk-caret" aria-hidden="true"></i></button>` +
		link("monitors", "Monitors", base, "") + link("incidents", "Incidents", base+"/incidents", count) + link("settings", "Settings", base+"/settings/channels", "") + `</nav>` +
		`<input class="vk-input vk-top__search" type="search" placeholder="Search monitors  /" aria-label="Search monitors"` + p.SearchAttrs + `>` +
		`<button type="button" class="vk-top__user" aria-haspopup="menu" title="` + esc(user) + `"` + p.UserAttrs + `>` + esc(initial) + `</button></header>`)
}

// Tab is one settings tab.
type Tab struct {
	ID    string
	Label string
	Count *int
	Href  string
}

// Tabs renders page sections as links.
func Tabs(tabs []Tab, current, label string) HTML {
	if label == "" {
		label = "Sections"
	}
	var b strings.Builder
	b.WriteString(`<nav class="vk-tabs" aria-label="` + esc(label) + `">`)
	for _, t := range tabs {
		href := t.Href
		if href == "" {
			href = "#"
		}
		b.WriteString(`<a class="vk-tab" href="` + esc(href) + `"`)
		if t.ID == current {
			b.WriteString(` aria-current="page"`)
		}
		b.WriteString(">" + esc(t.Label))
		if t.Count != nil {
			b.WriteString(`<span class="vk-tab__n">` + strconv.Itoa(*t.Count) + `</span>`)
		}
		b.WriteString("</a>")
	}
	b.WriteString("</nav>")
	return HTML(b.String())
}

// IncidentRowProps: one incident.
type IncidentRowProps struct {
	State     string // open (default), acked, resolved
	Name      string
	Slug      string
	Href      string
	Reason    string
	Opened    string
	OpenedAbs string
	Duration  string
	AckedBy   string
	Resolved  string
	// AckHTML replaces the plain Ack button, for a form. Trusted.
	AckHTML HTML
}

// IncidentRow renders one incident row.
func IncidentRow(p IncidentRowProps) HTML {
	s := p.State
	if s == "" {
		s = "open"
	}
	done := s == "resolved"
	var act string
	switch s {
	case "open":
		if p.AckHTML != "" {
			act = string(p.AckHTML)
		} else {
			act = string(Button(ButtonProps{Label: "Ack"}))
		}
	case "acked":
		by := p.AckedBy
		if by == "" {
			by = "j"
		}
		act = "<span>acked by " + esc(by) + "</span>"
	default:
		act = "<span>resolved " + esc(p.Resolved) + "</span>"
	}
	href := p.Href
	if href == "" {
		href = "#"
	}
	g := "down"
	if done {
		g = "up"
	}
	live := ""
	if !done {
		live = " vk-irow__data--live"
	}
	return HTML(`<div class="vk-irow vk-irow--` + esc(s) + `">` + string(Glyph(g, "")) +
		`<a class="vk-irow__name" href="` + esc(href) + `"><span>` + esc(p.Name) + `</span><span class="vk-row__slug">` + esc(p.Slug) + `</span></a>` +
		`<span class="vk-irow__data" title="` + esc(p.Reason) + `">` + esc(p.Reason) + `</span>` +
		`<span class="vk-irow__data" title="` + esc(p.OpenedAbs) + `">` + esc(p.Opened) + `</span>` +
		`<span class="vk-irow__data` + live + `">` + esc(p.Duration) + `</span>` +
		`<span class="vk-irow__act">` + act + `</span></div>`)
}

// Cell is one fixed-width cell of a settings row.
type Cell struct {
	Text string
	HTML HTML
	Size string // s, m (default), l
	Mono bool
	Ink  bool
}

// SettingsRowProps: one row of a settings list.
type SettingsRowProps struct {
	Title     string
	TitleHTML HTML
	Sub       string
	Lead      string
	HasLead   bool
	Muted     bool
	Cells     []Cell
	Actions   HTML
}

// SettingsRow renders one settings list row.
func SettingsRow(p SettingsRowProps) HTML {
	var b strings.Builder
	b.WriteString(`<div class="vk-srow`)
	if p.Muted {
		b.WriteString(" vk-srow--muted")
	}
	b.WriteString(`">`)
	if p.HasLead || p.Lead != "" {
		b.WriteString(`<span class="vk-srow__lead">` + p.Lead + `</span>`)
	}
	b.WriteString(`<div class="vk-srow__main"><span class="vk-srow__title">`)
	if p.TitleHTML != "" {
		b.WriteString(string(p.TitleHTML))
	} else {
		b.WriteString(esc(p.Title))
	}
	b.WriteString("</span>")
	if p.Sub != "" {
		b.WriteString(`<span class="vk-srow__sub" title="` + esc(p.Sub) + `">` + esc(p.Sub) + `</span>`)
	}
	b.WriteString("</div>")
	for _, c := range p.Cells {
		size := c.Size
		if size == "" {
			size = "m"
		}
		b.WriteString(`<span class="vk-srow__cell vk-srow__cell--` + size)
		if c.Mono {
			b.WriteString(" vk-srow__cell--mono")
		}
		if c.Ink {
			b.WriteString(" vk-srow__cell--ink")
		}
		b.WriteString(`">`)
		if c.HTML != "" {
			b.WriteString(string(c.HTML))
		} else {
			b.WriteString(esc(c.Text))
		}
		b.WriteString("</span>")
	}
	b.WriteString(`<span class="vk-srow__actions">` + string(p.Actions) + `</span></div>`)
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
