package domain

import (
	"regexp"
	"strings"
	"time"
)

var (
	slugRe = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?$`)
	tagRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,31}$`)
)

const (
	MaxNameLen = 120
	MaxTags    = 20
)

// ValidSlug reports whether s is a lowercase slug of at most 64 characters.
func ValidSlug(s string) bool { return slugRe.MatchString(s) }

// Slugify derives a slug from a name: lowercase, non-alphanumerics to
// hyphens, trimmed to 64 characters. It returns "" when nothing usable is
// left.
func Slugify(name string) string {
	var b strings.Builder
	lastDash := true
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > 64 {
		s = strings.TrimRight(s[:64], "-")
	}
	return s
}

// ValidTag reports whether t is a lowercase tag without a hash sign.
func ValidTag(t string) bool { return tagRe.MatchString(t) }

// NormalizeTags trims, lowercases and de-duplicates while keeping order.
func NormalizeTags(tags []string) []string {
	seen := make(map[string]bool, len(tags))
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		t = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(t, "#")))
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

// ValidateTags records problems for tags into ve under field.
func ValidateTags(ve *ValidationError, field string, tags []string) {
	if len(tags) > MaxTags {
		ve.Addf(field, "at most %d tags", MaxTags)
	}
	for _, t := range tags {
		if !ValidTag(t) {
			ve.Addf(field, "tag %q must be lowercase letters, digits, dots, dashes or underscores, at most 32 characters", t)
		}
	}
}

// ValidTimezone reports whether name is an IANA zone Go can load.
func ValidTimezone(name string) bool {
	if name == "" {
		return false
	}
	_, err := time.LoadLocation(name)
	return err == nil
}

// NormalizeSubject turns a sign-in name into the subject an identity
// provider's settings make of it: the realm cut off at "@" (j@CORP.EXAMPLE
// becomes j) and lowercased, each when asked.
func NormalizeSubject(name string, stripRealm, lowercase bool) string {
	name = strings.TrimSpace(name)
	if stripRealm {
		if i := strings.IndexByte(name, '@'); i > 0 {
			name = name[:i]
		}
	}
	if lowercase {
		name = strings.ToLower(name)
	}
	return name
}
