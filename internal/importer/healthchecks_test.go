package importer

import (
	"strings"
	"testing"

	"github.com/w4jnl/vink/internal/apply"
	"github.com/w4jnl/vink/internal/domain"
)

func TestHealthchecks(t *testing.T) {
	data := []byte(`{"checks": [
	 {"name": "Nightly backup", "slug": "nightly-backup", "tags": "prod backup", "grace": 1800, "n_pings": 3, "status": "up", "kind": "simple", "timeout": 86400, "methods": ""},
	 {"name": "Certbot", "slug": "", "tags": "", "grace": 3600, "kind": "cron", "schedule": "0 3 * * *", "tz": "Europe/Amsterdam", "methods": "POST", "status": "paused"},
	 {"name": "Nightly backup", "unique_key": "abc", "grace": 60, "timeout": 3600, "filter_subject": true},
	 {"name": "Weekly", "kind": "oncalendar", "schedule": "Mon *-*-* 03:00", "grace": 60},
	 {"name": "", "unique_key": "k9", "kind": "simple", "timeout": 0}
	]}`)
	r, err := Healthchecks(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.File.Monitors) != 3 {
		t.Fatalf("monitors: %+v", r.File.Monitors)
	}
	a, b, c := r.File.Monitors[0], r.File.Monitors[1], r.File.Monitors[2]
	if a.Slug != "nightly-backup" || a.Schedule.Period != domain.MustDuration("1d") || a.Grace != domain.MustDuration("30m") || strings.Join(a.Tags, ",") != "prod,backup" || len(a.Methods) != 0 {
		t.Errorf("simple: %+v", a)
	}
	if b.Slug != "certbot" || b.Schedule.Cron != "0 3 * * *" || b.Timezone != "Europe/Amsterdam" || b.Grace != domain.MustDuration("1h") || len(b.Methods) != 1 || b.Methods[0] != "POST" {
		t.Errorf("cron: %+v", b)
	}
	if c.Slug != "nightly-backup-2" || c.Schedule.Period != domain.MustDuration("1h") {
		t.Errorf("duplicate name: %+v", c)
	}
	if len(r.Skipped) != 2 || !strings.Contains(r.Skipped[0], "OnCalendar") || !strings.Contains(r.Skipped[1], "no period") {
		t.Errorf("skipped: %v", r.Skipped)
	}
	if len(r.Notes) != 2 || !strings.Contains(r.Notes[0], "paused") || !strings.Contains(r.Notes[1], "keyword") {
		t.Errorf("notes: %v", r.Notes)
	}
	// the file parses and validates as an apply file
	out, err := apply.Encode(r.File)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := apply.Parse(out, true); err != nil {
		t.Fatalf("apply file invalid: %v\n%s", err, out)
	}
	for _, bad := range []string{`{}`, `[]`, `{"checks": []}`, `nope`} {
		if _, err := Healthchecks([]byte(bad)); err == nil {
			t.Errorf("%s must fail", bad)
		}
	}
}
