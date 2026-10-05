package domain

import (
	"strings"
	"testing"
)

func TestStatusPageOwnerRules(t *testing.T) {
	cases := []struct {
		name string
		page StatusPage
		want string // a field in the error, "" for valid
	}{
		{"project page, defaults", StatusPage{ProjectID: "p", Slug: "home", Title: "Home", Public: true}, ""},
		{"project page, 30 days", StatusPage{ProjectID: "p", Slug: "home", Title: "Home", Public: true, Incidents: "30d"}, ""},
		{"project page grouped by project", StatusPage{ProjectID: "p", Slug: "home", Title: "Home", Public: true, GroupBy: "project"}, "group_by"},
		{"project page with projects", StatusPage{ProjectID: "p", Slug: "home", Title: "Home", Public: true, Projects: []string{"x"}}, "projects"},
		{"org page by project", StatusPage{OrgID: "o", Slug: "all", Title: "All", Public: true, GroupBy: "Project", Projects: []string{"a", "b", "a", " "}}, ""},
		{"org page by tag", StatusPage{OrgID: "o", Slug: "all", Title: "All", Public: true, GroupBy: "tag"}, ""},
		{"org page, defaults", StatusPage{OrgID: "o", Slug: "all", Title: "All", Public: true}, ""},
		{"unknown grouping", StatusPage{OrgID: "o", Slug: "all", Title: "All", Public: true, GroupBy: "team"}, "group_by"},
		{"unknown incidents", StatusPage{OrgID: "o", Slug: "all", Title: "All", Public: true, Incidents: "14d"}, "incidents"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := tc.page
			p.Normalize()
			err := p.Validate()
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("valid page refused: %v", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Fatalf("want an error on %s, got %v", tc.want, err)
			}
		})
	}
	p := StatusPage{OrgID: "o", Slug: "all", Title: "All", Public: true, GroupBy: " Project ", Projects: []string{"a", "b", "a", " "}}
	p.Normalize()
	if p.GroupBy != GroupByProject || p.Incidents != IncidentsOpen || strings.Join(p.Projects, ",") != "a,b" || !p.IsOrg() {
		t.Errorf("normalized: %+v", p)
	}
	defaults := []StatusPage{{OrgID: "o"}, {OrgID: "o", ProjectID: "p"}}
	for i, want := range []string{GroupByProject, GroupByTag} {
		defaults[i].Normalize()
		if defaults[i].GroupBy != want {
			t.Errorf("default grouping %d: %s, want %s", i, defaults[i].GroupBy, want)
		}
	}
}

func TestStatusPageIncidentWindow(t *testing.T) {
	for in, want := range map[string]struct {
		open bool
		days int
	}{"": {true, 0}, "open": {true, 0}, "none": {false, 0}, "7d": {true, 7}, "30d": {true, 30}, "90d": {true, 90}} {
		p := StatusPage{Incidents: in}
		if open, days := p.IncidentWindow(); open != want.open || days != want.days {
			t.Errorf("%q: open %v days %d, want %+v", in, open, days, want)
		}
	}
}
