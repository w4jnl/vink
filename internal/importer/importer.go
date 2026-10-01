// Package importer turns other monitors' exports into a vink apply file:
// the Healthchecks API listing and the Uptime Kuma backup. What vink has
// no equivalent for is skipped with a reason, so nothing is lost quietly.
package importer

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/w4jnl/vink/internal/apply"
	"github.com/w4jnl/vink/internal/domain"
)

// Result is the file plus what the import could not carry over.
type Result struct {
	File *apply.File
	// Skipped names what was left out and why.
	Skipped []string
	// Notes are differences worth knowing about things that were imported.
	Notes []string
}

func (r *Result) skip(what, why string) { r.Skipped = append(r.Skipped, what+": "+why) }
func (r *Result) note(what, why string) { r.Notes = append(r.Notes, what+": "+why) }

// slugs hands out unique slugs from names.
type slugs map[string]bool

func (s slugs) take(name, fallback string) string {
	base := domain.Slugify(name)
	if base == "" {
		base = domain.Slugify(fallback)
	}
	if base == "" {
		base = "monitor"
	}
	if len(base) > 60 {
		base = strings.TrimRight(base[:60], "-")
	}
	slug := base
	for i := 2; s[slug]; i++ {
		slug = base + "-" + strconv.Itoa(i)
	}
	s[slug] = true
	return slug
}

// seconds turns a count of seconds into a duration, floored at min.
func seconds(n float64, min time.Duration) domain.Duration {
	d := time.Duration(n * float64(time.Second))
	if d < min {
		d = min
	}
	return domain.Duration(d.Round(time.Second))
}

func describe(name, slug string) string {
	if name == "" || name == slug {
		return slug
	}
	return fmt.Sprintf("%s (%s)", name, slug)
}
