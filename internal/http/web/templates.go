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
		"join": strings.Join,
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
		set, err := template.Must(base.Clone()).ParseFS(templateFiles, "templates/"+name)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		t.pages[strings.TrimSuffix(name, ".html")] = set
	}
	return t, nil
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
