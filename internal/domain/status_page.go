package domain

import (
	"strings"
	"time"
)

// StatusPage shows the monitors carrying its tags to people without an
// account, at /s/{slug} and on an optional custom domain. It belongs to a
// project, or, with no ProjectID, to its org and shows monitors of the
// org's projects.
type StatusPage struct {
	ID        string
	OrgID     string
	ProjectID string // empty for an org page
	Slug      string
	Title     string
	// MatchTags: any-of; with GroupBy tag each tag is a group on the page,
	// in this order. Empty shows every monitor.
	MatchTags []string
	// Projects: an org page's project ids, in page order; empty shows every
	// project of the org, new ones included. Always empty on a project page.
	Projects []string
	// GroupBy: GroupByTag, or on an org page GroupByProject.
	GroupBy string
	// Incidents: which incidents the page lists, IncidentsOpen by default.
	Incidents string
	// Public is false when a password protects the page.
	Public       bool
	PasswordHash string
	CustomDomain string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// How a page groups its monitors.
const (
	GroupByTag     = "tag"
	GroupByProject = "project"
)

// Which incidents a page lists: none, the open ones, or the open ones and
// those resolved in the last 7, 30 or 90 days.
const (
	IncidentsNone = "none"
	IncidentsOpen = "open"
)

// IncidentChoices are the values of Incidents, in the order a form offers them.
var IncidentChoices = []string{IncidentsOpen, "7d", "30d", "90d", IncidentsNone}

// IsOrg reports whether the page belongs to an org rather than a project.
func (p *StatusPage) IsOrg() bool { return p.ProjectID == "" }

// IncidentWindow says whether the page lists open incidents and over how
// many days it lists resolved ones (0 for none).
func (p *StatusPage) IncidentWindow() (open bool, days int) {
	switch p.Incidents {
	case IncidentsNone:
		return false, 0
	case "7d":
		return true, 7
	case "30d":
		return true, 30
	case "90d":
		return true, 90
	}
	return true, 0
}

// Normalize trims and lowercases what must be canonical and fills the
// defaults. Call before Validate.
func (p *StatusPage) Normalize() {
	p.Slug = strings.ToLower(strings.TrimSpace(p.Slug))
	p.Title = strings.TrimSpace(p.Title)
	p.MatchTags = NormalizeTags(p.MatchTags)
	p.CustomDomain = strings.ToLower(strings.TrimSpace(p.CustomDomain))
	p.GroupBy = strings.ToLower(strings.TrimSpace(p.GroupBy))
	if p.GroupBy == "" {
		p.GroupBy = GroupByTag
	}
	p.Incidents = strings.ToLower(strings.TrimSpace(p.Incidents))
	if p.Incidents == "" {
		p.Incidents = IncidentsOpen
	}
	seen := map[string]bool{}
	ids := make([]string, 0, len(p.Projects))
	for _, id := range p.Projects {
		if id = strings.TrimSpace(id); id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	p.Projects = ids
	if p.PasswordHash != "" {
		p.Public = false
	}
}

// Validate checks the page. Field names match the JSON form.
func (p *StatusPage) Validate() error {
	ve := &ValidationError{}
	if !ValidSlug(p.Slug) {
		ve.Add("slug", "must be lowercase letters, digits and dashes, at most 64 characters")
	}
	if p.Title == "" {
		ve.Add("title", "must not be empty")
	} else if len([]rune(p.Title)) > MaxNameLen {
		ve.Addf("title", "at most %d characters", MaxNameLen)
	}
	ValidateTags(ve, "match_tags", p.MatchTags)
	if d := p.CustomDomain; d != "" {
		if len(d) > 253 || strings.ContainsAny(d, " /:\\@?#") || strings.HasPrefix(d, ".") || strings.HasSuffix(d, ".") {
			ve.Add("custom_domain", "must be a host name such as status.example.com")
		}
	}
	if !p.Public && p.PasswordHash == "" {
		ve.Add("password", "set a password or make the page public")
	}
	switch {
	case p.GroupBy != GroupByTag && p.GroupBy != GroupByProject:
		ve.Add("group_by", "must be tag or project")
	case p.GroupBy == GroupByProject && !p.IsOrg():
		ve.Add("group_by", "only an org's page groups by project")
	}
	if len(p.Projects) > 0 && !p.IsOrg() {
		ve.Add("projects", "only an org's page lists projects")
	}
	valid := false
	for _, c := range IncidentChoices {
		valid = valid || p.Incidents == c
	}
	if !valid {
		ve.Add("incidents", "must be none, open, 7d, 30d or 90d")
	}
	return ve.OrNil()
}

// HasPassword reports whether the page asks for one.
func (p *StatusPage) HasPassword() bool { return !p.Public && p.PasswordHash != "" }

// Shows reports whether the monitor belongs on the page.
func (p *StatusPage) Shows(m *Monitor) bool {
	if len(p.MatchTags) == 0 {
		return true
	}
	for _, t := range p.MatchTags {
		if m.HasAllTags([]string{t}) {
			return true
		}
	}
	return false
}

// Group names the group a monitor sits in: its first tag in match_tags
// order, or, when the page shows everything, the monitor's own first tag.
func (p *StatusPage) Group(m *Monitor) string {
	for _, t := range p.MatchTags {
		if m.HasAllTags([]string{t}) {
			return t
		}
	}
	if len(m.Tags) > 0 {
		return m.Tags[0]
	}
	return "monitors"
}
