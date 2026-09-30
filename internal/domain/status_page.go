package domain

import (
	"strings"
	"time"
)

// StatusPage shows the monitors carrying its tags to people without an
// account, at /s/{slug} and on an optional custom domain.
type StatusPage struct {
	ID        string
	ProjectID string
	Slug      string
	Title     string
	// MatchTags: any-of; each tag is a group on the page, in this order.
	// Empty shows every monitor.
	MatchTags []string
	// Public is false when a password protects the page.
	Public       bool
	PasswordHash string
	CustomDomain string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Normalize trims and lowercases what must be canonical. Call before Validate.
func (p *StatusPage) Normalize() {
	p.Slug = strings.ToLower(strings.TrimSpace(p.Slug))
	p.Title = strings.TrimSpace(p.Title)
	p.MatchTags = NormalizeTags(p.MatchTags)
	p.CustomDomain = strings.ToLower(strings.TrimSpace(p.CustomDomain))
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
