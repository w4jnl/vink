package ui

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
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
		return string(Button(ButtonProps{Label: str("label"), Variant: str("variant"), Confirm: str("confirm"), Disabled: boolean("disabled")}))
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
		return string(Field(FieldProps{ID: str("id"), Label: str("label"), Value: str("value"), Placeholder: str("placeholder"), Hint: str("hint"), Error: str("error"), Mono: boolean("mono")}))
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
	got := string(Field(FieldProps{ID: "pw", Label: "Password", Name: "password", Type: "password", Attrs: Attr("autocomplete", "current-password")}))
	if got != `<div class="vk-field"><label class="vk-field__label" for="pw">Password</label><input class="vk-input" id="pw" name="password" type="password" value="" placeholder="" autocomplete="current-password"></div>` {
		t.Errorf("field extras: %s", got)
	}
	got = string(Button(ButtonProps{Label: "Save", Variant: "primary", Type: "submit"}))
	if got != `<button type="submit" class="vk-btn vk-btn--primary">Save</button>` {
		t.Errorf("submit button: %s", got)
	}
	got = string(Field(FieldProps{ID: "c", Label: "Config", Textarea: true, Value: `{"a":"<b>"}`}))
	if got != `<div class="vk-field"><label class="vk-field__label" for="c">Config</label><textarea class="vk-input vk-input--area" id="c" placeholder="">{&quot;a&quot;:&quot;&lt;b&gt;&quot;}</textarea></div>` {
		t.Errorf("textarea: %s", got)
	}
	if esc(`<a href="x">'&'</a>`) != `&lt;a href=&quot;x&quot;&gt;&#39;&amp;&#39;&lt;/a&gt;` {
		t.Error("esc")
	}
}
