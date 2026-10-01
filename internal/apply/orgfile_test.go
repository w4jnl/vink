package apply

import (
	"strings"
	"testing"
)

const projectSample = "version: 1\nmonitors:\n  - slug: m\n    schedule: {period: 1h}\n"

const orgSample = `version: 1
org: homelab
projects:
  - slug: prod
    name: Production
    timezone: Europe/Amsterdam
    channels:
      - name: ops
        kind: webhook
        url: https://hooks.example.com/x
    monitors:
      - slug: web
        kind: http
        http: {url: https://example.com}
  - slug: lab
    monitors:
      - slug: nightly
        schedule: {period: 1d}
        grace: 1h
`

func TestOrgFileParseEncodeValidate(t *testing.T) {
	f, err := ParseOrg([]byte(orgSample), true)
	if err != nil {
		t.Fatal(err)
	}
	if f.Org != "homelab" || len(f.Projects) != 2 || f.Projects[0].Timezone != "Europe/Amsterdam" || len(f.Projects[0].Channels) != 1 || f.Projects[1].Slug != "lab" || f.Projects[1].Monitors[0].Grace.String() != "1h" {
		t.Fatalf("parsed: %+v", f)
	}
	pf := f.Projects[0].File()
	if pf.Project.Slug != "prod" || len(pf.Monitors) != 1 || pf.Channels[0].Config["url"] != "https://hooks.example.com/x" {
		t.Fatalf("entry as file: %+v", pf)
	}
	if e := EntryFrom(pf); e.Slug != "prod" || e.Name != "Production" || len(e.Monitors) != 1 {
		t.Fatalf("entry from file: %+v", e)
	}
	out, err := EncodeOrg(f)
	if err != nil {
		t.Fatal(err)
	}
	again, err := ParseOrg(out, true)
	if err != nil || len(again.Projects) != 2 || again.Projects[0].Channels[0].Config["url"] != "https://hooks.example.com/x" {
		t.Fatalf("round trip: %v\n%s", err, out)
	}
	// either shape through ParseAny; the wrong parser refuses the other shape
	if p, o, err := ParseAny([]byte(orgSample), true); err != nil || p != nil || o == nil {
		t.Fatalf("ParseAny org: %v %v %v", p, o, err)
	}
	if p, o, err := ParseAny([]byte(projectSample), true); err != nil || p == nil || o != nil {
		t.Fatalf("ParseAny project: %v %v %v", p, o, err)
	}
	if _, err := Parse([]byte(orgSample), true); err == nil || !strings.Contains(err.Error(), "org file") {
		t.Fatalf("Parse on an org file: %v", err)
	}
	if _, err := ParseOrg([]byte(projectSample), true); err == nil || !strings.Contains(err.Error(), "project file") {
		t.Fatalf("ParseOrg on a project file: %v", err)
	}
	// the org branch of the schema, with errors that name the place
	for _, c := range []struct{ doc, want string }{
		{"version: 1\norg: homelab\nprojects:\n  - name: x\n", "/projects/0: missing property 'slug'"},
		{"version: 1\nprojects: []\n", "missing property 'org'"},
		{"version: 1\norg: homelab\nproject: {slug: x}\nprojects: []\n", "'project'"},
		{"version: 1\norg: homelab\nprojects:\n  - slug: p\n    monitors:\n      - slug: m\n        kind: nope\n", "/projects/0/monitors/0/kind"},
	} {
		_, err := ParseOrg([]byte(c.doc), true)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: want %q, got %v", c.doc, c.want, err)
		}
	}
	// a project file still validates against its own branch
	if _, err := Parse([]byte("version: 1\nmonitors:\n  - slug: m\n    kind: nope\n"), true); err == nil || !strings.Contains(err.Error(), "/monitors/0/kind") {
		t.Fatalf("project branch: %v", err)
	}
}
