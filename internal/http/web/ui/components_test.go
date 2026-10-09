package ui

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

type goldenCase struct {
	Name      string          `json:"name"`
	Component string          `json:"component"`
	Props     json.RawMessage `json:"props"`
}

// render maps a golden case onto the Go component.
func render(t *testing.T, c goldenCase) string {
	t.Helper()
	var p map[string]any
	if err := json.Unmarshal(c.Props, &p); err != nil {
		t.Fatal(err)
	}
	str := func(k string) string {
		if v, ok := p[k].(string); ok {
			return v
		}
		return ""
	}
	boolean := func(k string) bool { v, _ := p[k].(bool); return v }
	floats := func(k string) []float64 {
		raw, ok := p[k].([]any)
		if !ok {
			return nil
		}
		out := make([]float64, 0, len(raw))
		for _, v := range raw {
			out = append(out, v.(float64))
		}
		return out
	}
	strs := func(k string) []string {
		raw, ok := p[k].([]any)
		if !ok {
			return nil
		}
		out := make([]string, 0, len(raw))
		for _, v := range raw {
			out = append(out, v.(string))
		}
		return out
	}
	switch c.Component {
	case "StateBadge":
		return string(StateBadge(StateBadgeProps{State: str("state"), Pill: boolean("pill"), Label: str("label"), Since: str("since")}))
	case "Button":
		return string(Button(ButtonProps{Label: str("label"), Variant: str("variant"), Confirm: str("confirm"), Disabled: boolean("disabled"), Href: str("href"), Type: str("type"), Block: boolean("block")}))
	case "Chip":
		cp := ChipProps{Label: str("label"), Pressed: boolean("pressed"), State: str("state")}
		if v, ok := p["count"].(float64); ok {
			cp.Count = Count(int(v))
		}
		return string(Chip(cp))
	case "Tag":
		return string(Tag(str("label")))
	case "KindIcon":
		return string(KindIcon(str("kind")))
	case "Field":
		fp := FieldProps{ID: str("id"), Name: str("name"), Label: str("label"), Value: str("value"), Placeholder: str("placeholder"), Hint: str("hint"), Error: str("error"), Mono: boolean("mono"),
			Control: str("control"), Type: str("type"), Prefix: str("prefix"), Suffix: str("suffix"), Disabled: boolean("disabled"), Autocomplete: str("autocomplete"), Otp: boolean("otp"),
			HTML: HTML(str("html")), Before: HTML(str("before")), After: HTML(str("after"))}
		if v, ok := p["rows"].(float64); ok {
			fp.Rows = int(v)
		}
		fp.Options = optionsOf(p["options"])
		return string(Field(fp))
	case "FieldRow":
		var fields []HTML
		for _, f := range strs("fields") {
			fields = append(fields, HTML(f))
		}
		return string(FieldRow(fields, boolean("lead")))
	case "Checkbox":
		return string(Checkbox(CheckboxProps{Label: str("label"), LabelHTML: HTML(str("labelHtml")), Name: str("name"), ID: str("id"), Value: str("value"), Checked: boolean("checked"), Disabled: boolean("disabled"), Hint: str("hint")}))
	case "Switch":
		return string(Switch(boolean("checked"), str("label"), ""))
	case "Segmented":
		sp := SegmentedProps{Name: str("name"), Label: str("label"), Multi: boolean("multi"), Mono: boolean("mono"), Options: optionsOf(p["options"])}
		switch v := p["value"].(type) {
		case string:
			sp.Value = []string{v}
		case []any:
			sp.Value = strs("value")
		}
		return string(Segmented(sp))
	case "KindPicker":
		return string(KindPicker(str("value"), boolean("locked"), ""))
	case "Disclosure":
		return string(Disclosure(str("title"), str("summary"), HTML(str("body")), boolean("open")))
	case "Notice":
		return string(Notice(str("tone"), str("title"), str("text"), HTML(str("html"))))
	case "Code":
		return string(Code(str("text"), boolean("copy"), str("copyLabel"), boolean("yaml")))
	case "Panel":
		return string(Panel(str("title"), str("note"), HTML(str("body")), HTML(str("actions")), str("id")))
	case "TopBar":
		tp := TopBarProps{Org: str("org"), Project: str("project"), Section: str("section"), User: str("user"), Menu: HTML(str("menu")), UserMenu: HTML(str("userMenu")), Open: str("open")}
		if v, ok := p["incidents"].(float64); ok {
			tp.Incidents = int(v)
		}
		if h, ok := p["hrefs"].(map[string]any); ok {
			tp.Hrefs = map[string]string{}
			for k, v := range h {
				tp.Hrefs[k] = v.(string)
			}
		}
		return string(TopBar(tp))
	case "Tabs":
		var tabs []Tab
		for _, raw := range p["tabs"].([]any) {
			m := raw.(map[string]any)
			tab := Tab{ID: stringOf(m["id"]), Label: stringOf(m["label"]), Href: stringOf(m["href"])}
			if v, ok := m["count"].(float64); ok {
				tab.Count = Count(int(v))
			}
			tabs = append(tabs, tab)
		}
		return string(Tabs(tabs, str("current"), str("label")))
	case "IncidentRow":
		return string(IncidentRow(IncidentRowProps{State: str("state"), Name: str("name"), Slug: str("slug"), Href: str("href"), Reason: str("reason"), Opened: str("opened"), OpenedAbs: str("openedAbs"), Duration: str("duration"), AckedBy: str("ackedBy"), AckedAt: str("ackedAt"), Resolved: str("resolved")}))
	case "SettingsRow":
		sp := SettingsRowProps{Title: str("title"), TitleHTML: HTML(str("titleHtml")), Sub: str("sub"), Prose: boolean("prose"), Muted: boolean("muted"), Href: str("href"), Current: boolean("current"), Actions: HTML(str("actions"))}
		if lead, ok := p["lead"]; ok {
			sp.HasLead = true
			sp.Lead = stringOf(lead)
		}
		if cells, ok := p["cells"].([]any); ok {
			for _, raw := range cells {
				m := raw.(map[string]any)
				c := Cell{Text: stringOf(m["text"]), HTML: HTML(stringOf(m["html"])), Size: stringOf(m["size"])}
				c.Mono, _ = m["mono"].(bool)
				c.Ink, _ = m["ink"].(bool)
				sp.Cells = append(sp.Cells, c)
			}
		}
		return string(SettingsRow(sp))
	case "Menu":
		var groups []MenuGroup
		for _, raw := range p["groups"].([]any) {
			gm := raw.(map[string]any)
			g := MenuGroup{Label: stringOf(gm["label"]), Role: stringOf(gm["role"])}
			if items, ok := gm["items"].([]any); ok {
				for _, ir := range items {
					im := ir.(map[string]any)
					cur, _ := im["current"].(bool)
					quiet, _ := im["quiet"].(bool)
					g.Items = append(g.Items, MenuItem{Label: stringOf(im["label"]), Href: stringOf(im["href"]), Current: cur, Meta: HTML(stringOf(im["meta"])), Quiet: quiet})
				}
			}
			groups = append(groups, g)
		}
		return string(Menu(groups))
	case "StateCounts":
		n := func(k string) int { v, _ := p[k].(float64); return int(v) }
		return string(StateCounts(StateCountsProps{Down: n("down"), Late: n("late"), Up: n("up"), Paused: n("paused"), New: n("new"), Problems: boolean("problems")}))
	case "Avatar":
		return string(Avatar(str("name")))
	case "InlineSelect":
		return string(InlineSelect(InlineSelectProps{Label: str("label"), Name: str("name"), Options: optionsOf(p["options"]), Value: str("value"), Disabled: boolean("disabled")}))
	case "Usage":
		v, _ := p["value"].(float64)
		m, _ := p["max"].(float64)
		return string(Usage(int(v), int(m), str("label")))
	case "PingUrl":
		return string(PingURL(str("base"), str("key"), str("slug")))
	case "Sparkline":
		return string(Sparkline(floats("points"), str("state")))
	case "MonitorRow":
		mp := MonitorRowProps{State: str("state"), Name: str("name"), Slug: str("slug"), Kind: str("kind"), Last: str("last"), LastAbs: str("lastAbs"), Next: str("next"), Tags: strs("tags"), Href: str("href"), Current: boolean("current")}
		if _, ok := p["points"]; ok {
			mp.Points = floats("points")
		}
		return string(MonitorRow(mp))
	case "UptimeBar":
		return string(UptimeBar(strs("days"), boolean("compact"), str("label"), strs("legend")))
	case "StatusBanner":
		return string(StatusBanner(str("state"), str("text"), str("detail")))
	case "EmptyState":
		return string(EmptyState(str("base"), str("key")))
	case "Diff":
		return string(Diff(diffLines(p["lines"])))
	case "AuditRow":
		ap := AuditRowProps{Time: str("time"), TimeAbs: str("timeAbs"), Actor: str("actor"), ActorKind: str("actorKind"), State: str("state"), Text: HTML(str("text")), Scope: str("scope"), Via: str("via"), Open: boolean("open")}
		if _, ok := p["diff"]; ok {
			ap.Diff = diffLines(p["diff"])
		}
		if meta, ok := p["meta"].([]any); ok {
			ap.Meta = [][2]string{}
			for _, raw := range meta {
				pair := raw.([]any)
				ap.Meta = append(ap.Meta, [2]string{stringOf(pair[0]), stringOf(pair[1])})
			}
		}
		return string(AuditRow(ap))
	case "Qr":
		return string(Qr(HTML(str("svg")), str("label"), str("caption")))
	case "RecoveryCodes":
		return string(RecoveryCodes(strs("codes")))
	case "Divider":
		return string(Divider(str("label")))
	case "Mark":
		size := 0
		if v, ok := p["size"].(float64); ok {
			size = int(v)
		}
		return string(Mark(size))
	}
	t.Fatalf("unknown component %s", c.Component)
	return ""
}

// stringOf renders a JSON scalar the way JS String() would.
func stringOf(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	}
	return ""
}

// optionsOf maps JSON options (strings or {value,label}) to Options.
func optionsOf(v any) []Option {
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]Option, 0, len(raw))
	for _, o := range raw {
		switch x := o.(type) {
		case string:
			out = append(out, Option{Value: x, Label: x})
		case map[string]any:
			out = append(out, Option{Value: stringOf(x["value"]), Label: stringOf(x["label"])})
		}
	}
	return out
}

func loadCases(t *testing.T) []goldenCase {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "testdata", "golden", "props.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []goldenCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	return cases
}

// TestComponentsMatchGolden compares every Go component with the markup
// bundle.js produced for the same props.
func TestComponentsMatchGolden(t *testing.T) {
	for _, c := range loadCases(t) {
		t.Run(c.Name, func(t *testing.T) {
			want, err := os.ReadFile(filepath.Join("..", "testdata", "golden", c.Name+".html"))
			if err != nil {
				t.Fatal(err)
			}
			if got := render(t, c); got != string(want) {
				t.Fatalf("markup differs from bundle.js\n got: %s\nwant: %s", got, want)
			}
		})
	}
}

// TestGoldenFilesAreFresh regenerates the goldens with node when it is
// installed and fails if the committed files differ, so a design-system
// change cannot go unnoticed.
func TestGoldenFilesAreFresh(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; skipping freshness check")
	}
	dir := t.TempDir()
	cmd := exec.Command(node, filepath.Join("..", "gen_golden.mjs"), dir) //nolint:gosec // G204: node from PATH with fixed arguments, test only
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("node: %v\n%s", err, out)
	}
	for _, c := range loadCases(t) {
		fresh, err := os.ReadFile(filepath.Join(dir, c.Name+".html"))
		if err != nil {
			t.Fatal(err)
		}
		committed, err := os.ReadFile(filepath.Join("..", "testdata", "golden", c.Name+".html"))
		if err != nil {
			t.Fatal(err)
		}
		if string(fresh) != string(committed) {
			t.Errorf("%s: committed golden is stale; run make golden", c.Name)
		}
	}
}

func TestExtras(t *testing.T) {
	got := string(Field(FieldProps{ID: "pw", Label: "Password", Name: "password", Type: "password", Attrs: Attr("required", "")}))
	if got != `<div class="vk-field"><label class="vk-field__label" for="pw">Password</label><input class="vk-input" id="pw" name="password" type="password" value="" placeholder="" required=""></div>` {
		t.Errorf("field extras: %s", got)
	}
	got = string(Button(ButtonProps{Label: "Save", Variant: "primary", Type: "submit", Attrs: Attr("form", "f1")}))
	if got != `<button type="submit" class="vk-btn vk-btn--primary" form="f1">Save</button>` {
		t.Errorf("submit button: %s", got)
	}
	got = string(Field(FieldProps{ID: "c", Label: "Config", Control: "textarea", Value: `{"a":"<b>"}`, Attrs: Attr("hx-post", "/x")}))
	if got != `<div class="vk-field"><label class="vk-field__label" for="c">Config</label><textarea class="vk-input vk-input--area" id="c" name="c" rows="3" placeholder="" hx-post="/x">{&quot;a&quot;:&quot;&lt;b&gt;&quot;}</textarea></div>` {
		t.Errorf("textarea: %s", got)
	}
	got = string(Switch(true, "x", Attr("hx-post", "/toggle")))
	if !strings.Contains(got, `aria-checked="true" aria-label="x" hx-post="/toggle">`) {
		t.Errorf("switch attrs: %s", got)
	}
	if len(Opts("a", "A", "b")) != 2 || Opts("a", "A", "b")[1].Label != "b" {
		t.Error("Opts")
	}
	if esc(`<a href="x">'&'</a>`) != `&lt;a href=&quot;x&quot;&gt;&#39;&amp;&#39;&lt;/a&gt;` {
		t.Error("esc")
	}
}

// diffLines reads the kit's [op, text] pairs.
func diffLines(v any) []DiffLine {
	raw, _ := v.([]any)
	out := []DiffLine{}
	for _, l := range raw {
		pair := l.([]any)
		out = append(out, DiffLine{Op: stringOf(pair[0]), Text: stringOf(pair[1])})
	}
	return out
}
