package api

import (
	"bufio"
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"testing"
)

// specRoutes reads "METHOD /path" pairs from the paths section of the
// embedded OpenAPI document without a YAML dependency: path keys are
// indented two spaces, methods four.
func specRoutes(t *testing.T) []string {
	t.Helper()
	var routes []string
	inPaths := false
	path := ""
	sc := bufio.NewScanner(bytes.NewReader(openAPI))
	for sc.Scan() {
		line := sc.Text()
		if line == "paths:" {
			inPaths = true
			continue
		}
		if !inPaths {
			continue
		}
		if strings.HasPrefix(line, "  /") && strings.HasSuffix(strings.TrimSpace(line), ":") {
			path = strings.TrimSuffix(strings.TrimSpace(line), ":")
			continue
		}
		if strings.HasPrefix(line, "    ") && !strings.HasPrefix(line, "     ") {
			method, _, _ := strings.Cut(strings.TrimSpace(line), ":")
			switch method {
			case "get", "post", "put", "patch", "delete":
				routes = append(routes, strings.ToUpper(method)+" "+path)
			}
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return routes
}

func TestOpenAPIMatchesRouter(t *testing.T) {
	a := New(nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	a.Mount(http.NewServeMux())
	got := append([]string(nil), a.Routes...)
	want := specRoutes(t)
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("router and openapi.yaml differ\nrouter:\n%s\n\nspec:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if len(want) < 25 {
		t.Fatalf("suspiciously few spec routes: %d", len(want))
	}
}
