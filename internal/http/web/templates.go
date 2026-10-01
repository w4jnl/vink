// Package web is the server-rendered UI: one list and one drawer per
// entity, htmx for the parts that change without a reload, and the
// design system's components rendered by internal/http/web/ui.
package web

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"reflect"
	"strings"

	"github.com/w4jnl/vink/internal/http/web/ui"
)

//go:embed templates/*.html
var templateFiles embed.FS

// Templates holds one parsed set per page, each sharing layout.html.
type Templates struct {
	pages map[string]*template.Template
}

// NewTemplates parses every page against the layout.
func NewTemplates(static *Static) (*Templates, error) {
	funcs := template.FuncMap{
		"static": static.URL,
		"glyph":  ui.Glyph,
		"statebadge": func(v any) (ui.HTML, error) {
			var p ui.StateBadgeProps
			err := assign(v, &p)
			return ui.StateBadge(p), err
		},
		"button":    func(v any) (ui.HTML, error) { var p ui.ButtonProps; err := assign(v, &p); return ui.Button(p), err },
		"chip":      func(v any) (ui.HTML, error) { var p ui.ChipProps; err := assign(v, &p); return ui.Chip(p), err },
		"tag":       ui.Tag,
		"kindicon":  ui.KindIcon,
		"field":     func(v any) (ui.HTML, error) { var p ui.FieldProps; err := assign(v, &p); return ui.Field(p), err },
		"pingurl":   ui.PingURL,
		"sparkline": ui.Sparkline,
		"monitorrow": func(v any) (ui.HTML, error) {
			var p ui.MonitorRowProps
			err := assign(v, &p)
			return ui.MonitorRow(p), err
		},
		"uptimebar":    ui.UptimeBar,
		"statusbanner": ui.StatusBanner,
		"emptystate":   ui.EmptyState,
		"mark":         ui.Mark,
		"attr":         ui.Attr,
		"count":        ui.Count,
		"attrs": func(kv ...string) string {
			var b strings.Builder
			for i := 0; i+1 < len(kv); i += 2 {
				b.WriteString(ui.Attr(kv[i], kv[i+1]))
			}
			return b.String()
		},
		"opts": ui.Opts,
		"list": func(v ...any) []any { return v },
		"strs": func(v ...string) []string { return v },
		"strmap": func(kv ...string) map[string]string {
			m := make(map[string]string, len(kv)/2)
			for i := 0; i+1 < len(kv); i += 2 {
				m[kv[i]] = kv[i+1]
			}
			return m
		},
		"html": toHTML,
		"fieldrow": func(items []any, lead bool) ui.HTML {
			fields := make([]ui.HTML, 0, len(items))
			for _, it := range items {
				fields = append(fields, toHTML(it))
			}
			return ui.FieldRow(fields, lead)
		},
		"checkbox": func(v any) (ui.HTML, error) { var p ui.CheckboxProps; err := assign(v, &p); return ui.Checkbox(p), err },
		"switch":   ui.Switch,
		"segmented": func(v any) (ui.HTML, error) {
			var p ui.SegmentedProps
			err := assign(v, &p)
			return ui.Segmented(p), err
		},
		"kindpicker": ui.KindPicker,
		"disclosure": func(title, summary string, body any, open bool) ui.HTML {
			return ui.Disclosure(title, summary, toHTML(body), open)
		},
		"notice": func(tone, title, text string, extra any) ui.HTML { return ui.Notice(tone, title, text, toHTML(extra)) },
		"code":   ui.Code,
		"panel": func(title, note string, body, actions any, id string) ui.HTML {
			return ui.Panel(title, note, toHTML(body), toHTML(actions), id)
		},
		"topbar": func(v any) (ui.HTML, error) { var p ui.TopBarProps; err := assign(v, &p); return ui.TopBar(p), err },
		"tabs":   ui.Tabs,
		"incidentrow": func(v any) (ui.HTML, error) {
			var p ui.IncidentRowProps
			err := assign(v, &p)
			return ui.IncidentRow(p), err
		},
		"inlineselect": func(v any) (ui.HTML, error) {
			var p ui.InlineSelectProps
			err := assign(v, &p)
			return ui.InlineSelect(p), err
		},
		"settingsrow": func(v any) (ui.HTML, error) {
			var p ui.SettingsRowProps
			err := assign(v, &p)
			return ui.SettingsRow(p), err
		},
		// partial is replaced per page set below; the base never executes.
		"partial": func(string, any) (ui.HTML, error) { return "", fmt.Errorf("partial outside a page") },
		"join":    strings.Join,
		"dict": func(kv ...any) (map[string]any, error) {
			if len(kv)%2 != 0 {
				return nil, fmt.Errorf("dict needs pairs")
			}
			m := make(map[string]any, len(kv)/2)
			for i := 0; i < len(kv); i += 2 {
				k, ok := kv[i].(string)
				if !ok {
					return nil, fmt.Errorf("dict keys must be strings")
				}
				m[k] = kv[i+1]
			}
			return m, nil
		},
	}
	// layout.html and every _*.html partial are shared by all pages.
	base, err := template.New("base").Funcs(funcs).ParseFS(templateFiles, "templates/layout.html", "templates/_*.html")
	if err != nil {
		return nil, err
	}
	entries, err := fs.ReadDir(templateFiles, "templates")
	if err != nil {
		return nil, err
	}
	t := &Templates{pages: map[string]*template.Template{}}
	for _, e := range entries {
		name := e.Name()
		if name == "layout.html" || strings.HasPrefix(name, "_") || !strings.HasSuffix(name, ".html") {
			continue
		}
		set := template.Must(base.Clone())
		set.Funcs(template.FuncMap{"partial": partialFunc(set)})
		if _, err := set.ParseFS(templateFiles, "templates/"+name); err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		t.pages[strings.TrimSuffix(name, ".html")] = set
	}
	return t, nil
}

// partialFunc executes a define of the page's own set from inside a
// template, so a component can take another partial as its body.
func partialFunc(set *template.Template) func(string, any) (ui.HTML, error) {
	return func(name string, data any) (ui.HTML, error) {
		var buf bytes.Buffer
		if err := set.ExecuteTemplate(&buf, name, data); err != nil {
			return "", err
		}
		return ui.HTML(buf.String()), nil
	}
}

// toHTML accepts trusted markup as ui.HTML or a plain string.
func toHTML(v any) ui.HTML {
	switch x := v.(type) {
	case nil:
		return ""
	case ui.HTML:
		return x
	case string:
		return ui.HTML(x)
	}
	return ui.HTML(fmt.Sprint(v))
}

// Render executes a named template of a page into a buffer.
func (t *Templates) Render(page, name string, data any) ([]byte, error) {
	set, ok := t.pages[page]
	if !ok {
		return nil, fmt.Errorf("no page %q", page)
	}
	var buf bytes.Buffer
	if err := set.ExecuteTemplate(&buf, name, data); err != nil {
		return nil, fmt.Errorf("render %s/%s: %w", page, name, err)
	}
	return buf.Bytes(), nil
}

// assign fills dst (a pointer to a props struct) from either a value of
// that struct type or a map from the dict template function.
func assign(v any, dst any) error {
	dv := reflect.ValueOf(dst).Elem()
	rv := reflect.ValueOf(v)
	if rv.IsValid() && rv.Type() == dv.Type() {
		dv.Set(rv)
		return nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("expected %s or a dict, got %T", dv.Type(), v)
	}
	for k, val := range m {
		f := dv.FieldByName(k)
		if !f.IsValid() {
			return fmt.Errorf("%s has no field %q", dv.Type(), k)
		}
		if val == nil {
			continue
		}
		x := reflect.ValueOf(val)
		switch {
		case x.Type().AssignableTo(f.Type()):
			f.Set(x)
		case x.Type().ConvertibleTo(f.Type()) && x.Kind() != reflect.String:
			f.Set(x.Convert(f.Type()))
		case f.Kind() == reflect.Pointer && f.Type().Elem().Kind() == reflect.Int && x.Kind() == reflect.Int:
			n := int(x.Int())
			f.Set(reflect.ValueOf(&n))
		case f.Kind() == reflect.Slice && f.Type().Elem().Kind() == reflect.String && x.Kind() == reflect.Slice:
			out := make([]string, x.Len())
			for i := range x.Len() {
				out[i] = fmt.Sprint(x.Index(i).Interface())
			}
			f.Set(reflect.ValueOf(out))
		default:
			return fmt.Errorf("%s.%s: cannot use %T", dv.Type(), k, val)
		}
	}
	return nil
}
