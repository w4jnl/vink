package domain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestHeartbeatSpecValidate(t *testing.T) {
	base := func() HeartbeatSpec {
		return HeartbeatSpec{Schedule: Schedule{Period: MustDuration("1h")}}
	}
	cases := []struct {
		name   string
		mutate func(*HeartbeatSpec)
		field  string // expected failing field, "" for valid
	}{
		{"defaults valid", func(*HeartbeatSpec) {}, ""},
		{"cron valid", func(s *HeartbeatSpec) { s.Schedule = Schedule{Cron: "0 3 * * *"} }, ""},
		{"cron with spaces trimmed", func(s *HeartbeatSpec) { s.Schedule = Schedule{Cron: "  0 3 * * *  "} }, ""},
		{"period too short", func(s *HeartbeatSpec) { s.Schedule.Period = MustDuration("30s") }, "schedule"},
		{"both forms", func(s *HeartbeatSpec) { s.Schedule.Cron = "0 3 * * *" }, "schedule"},
		{"no schedule", func(s *HeartbeatSpec) { s.Schedule = Schedule{} }, "schedule"},
		{"bad cron", func(s *HeartbeatSpec) { s.Schedule = Schedule{Cron: "0 3 * *"} }, "schedule"},
		{"grace too short", func(s *HeartbeatSpec) { s.Grace = MustDuration("30s") }, "grace"},
		{"grace too long", func(s *HeartbeatSpec) { s.Grace = MustDuration("366d") }, "grace"},
		{"grace 365d ok", func(s *HeartbeatSpec) { s.Grace = MustDuration("365d") }, ""},
		{"timezone ok", func(s *HeartbeatSpec) { s.Timezone = "Europe/Amsterdam" }, ""},
		{"timezone bad", func(s *HeartbeatSpec) { s.Timezone = "Mars/Olympus" }, "timezone"},
		{"failure threshold zero after normalize is 1", func(s *HeartbeatSpec) { s.FailureThreshold = 0 }, ""},
		{"failure threshold too high", func(s *HeartbeatSpec) { s.FailureThreshold = 101 }, "failure_threshold"},
		{"recovery threshold negative", func(s *HeartbeatSpec) { s.RecoveryThreshold = -1 }, "recovery_threshold"},
		{"methods ok lowercase", func(s *HeartbeatSpec) { s.Methods = []string{"post"} }, ""},
		{"methods bad", func(s *HeartbeatSpec) { s.Methods = []string{"DELETE"} }, "methods"},
		{"max runtime ok", func(s *HeartbeatSpec) { s.MaxRuntime = MustDuration("2h") }, ""},
		{"max runtime too long", func(s *HeartbeatSpec) { s.MaxRuntime = MustDuration("31d") }, "max_runtime"},
		{"body limit negative", func(s *HeartbeatSpec) { s.BodyLimit = -1 }, "body_limit"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := base()
			c.mutate(&s)
			s.Normalize()
			err := s.Validate()
			if c.field == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			ve, ok := AsValidation(err)
			if !ok {
				t.Fatalf("expected ValidationError, got %v", err)
			}
			found := false
			for _, fe := range ve.Errors {
				if fe.Field == c.field {
					found = true
				}
			}
			if !found {
				t.Fatalf("expected error on %q, got %v", c.field, ve.Errors)
			}
		})
	}
}

func TestHeartbeatSpecJSONRoundTrip(t *testing.T) {
	in := `{"schedule":{"cron":"0 3 * * *"},"timezone":"Europe/Amsterdam","grace":"30m","max_runtime":"2h","failure_threshold":1,"recovery_threshold":1,"methods":["POST"]}`
	s, err := ParseHeartbeatSpec([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if s.Grace != MustDuration("30m") || s.MaxRuntime != MustDuration("2h") || s.Schedule.Cron != "0 3 * * *" {
		t.Errorf("decoded %+v", s)
	}
	out, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"grace":"30m"`) || !strings.Contains(string(out), `"max_runtime":"2h"`) {
		t.Errorf("durations must marshal as short strings: %s", out)
	}
	if strings.Contains(string(out), `"period"`) {
		t.Errorf("unset period must be omitted: %s", out)
	}
	if !s.AcceptsMethod("POST") || s.AcceptsMethod("GET") {
		t.Error("methods restriction not applied")
	}
	loc, err := s.Location("UTC")
	if err != nil || loc.String() != "Europe/Amsterdam" {
		t.Errorf("Location = %v, %v", loc, err)
	}
	next, err := s.ExpectedAfter(time.Date(2026, 9, 27, 22, 0, 0, 0, time.UTC), loc)
	if err != nil {
		t.Fatal(err)
	}
	if !next.Equal(time.Date(2026, 9, 28, 1, 0, 0, 0, time.UTC)) {
		t.Errorf("ExpectedAfter = %s", next)
	}
}

func TestMonitorValidate(t *testing.T) {
	good := func() *Monitor {
		return &Monitor{Slug: "nightly-backup", Name: "Nightly backup", Kind: KindHeartbeat, Tags: []string{"backup", "prod"},
			Heartbeat: &HeartbeatSpec{Schedule: Schedule{Period: MustDuration("1d")}, Grace: MustDuration("1h"), FailureThreshold: 1, RecoveryThreshold: 1}}
	}
	if err := good().Validate(); err != nil {
		t.Fatalf("good monitor: %v", err)
	}
	cases := []struct {
		name   string
		mutate func(*Monitor)
		field  string
	}{
		{"slug uppercase", func(m *Monitor) { m.Slug = "Nightly" }, "slug"},
		{"slug leading dash", func(m *Monitor) { m.Slug = "-x" }, "slug"},
		{"slug too long", func(m *Monitor) { m.Slug = strings.Repeat("a", 65) }, "slug"},
		{"name empty", func(m *Monitor) { m.Name = "" }, "name"},
		{"name too long", func(m *Monitor) { m.Name = strings.Repeat("n", 121) }, "name"},
		{"kind unknown", func(m *Monitor) { m.Kind = "ping" }, "kind"},
		{"kind http without block", func(m *Monitor) { m.Kind = KindHTTP }, "http"},
		{"tag with hash", func(m *Monitor) { m.Tags = []string{"#prod"} }, "tags"},
		{"tag uppercase", func(m *Monitor) { m.Tags = []string{"Prod"} }, "tags"},
		{"too many tags", func(m *Monitor) { m.Tags = strings.Split(strings.Repeat("t,", 21), ",")[:21] }, "tags"},
		{"no spec", func(m *Monitor) { m.Heartbeat = nil }, "schedule"},
		{"bad grace flattened", func(m *Monitor) { m.Heartbeat.Grace = MustDuration("1s") }, "grace"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := good()
			c.mutate(m)
			ve, ok := AsValidation(m.Validate())
			if !ok {
				t.Fatal("expected a validation error")
			}
			for _, fe := range ve.Errors {
				if fe.Field == c.field {
					return
				}
			}
			t.Fatalf("expected error on %q, got %v", c.field, ve.Errors)
		})
	}
}

func TestSlugifyAndTags(t *testing.T) {
	for in, want := range map[string]string{
		"Nightly backup":        "nightly-backup",
		"  Public API (v2)!  ":  "public-api-v2",
		"---":                   "",
		"Ünïcode name":          "n-code-name",
		strings.Repeat("a", 70): strings.Repeat("a", 64),
	} {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
	got := NormalizeTags([]string{" Prod ", "#backup", "prod", "", "db"})
	if strings.Join(got, ",") != "prod,backup,db" {
		t.Errorf("NormalizeTags = %v", got)
	}
	m := &Monitor{Tags: []string{"prod", "backup"}}
	if !m.HasAllTags(nil) || !m.HasAllTags([]string{"prod"}) || m.HasAllTags([]string{"prod", "db"}) {
		t.Error("HasAllTags")
	}
	if m.TagsJSON() != `["prod","backup"]` || ParseTags(`["a","b"]`)[1] != "b" {
		t.Error("tags JSON round trip")
	}
}

func TestScopePermissions(t *testing.T) {
	cases := []struct {
		role                                      Role
		operate, edit, pingKey, adminProj, ownOrg bool
	}{
		{RoleViewer, false, false, false, false, false},
		{RoleMember, true, true, true, false, false},
		{RoleAdmin, true, true, true, true, false},
		{RoleOwner, true, true, true, true, true},
	}
	for _, c := range cases {
		s := Scope{Role: c.role}
		if s.CanOperate() != c.operate || s.CanEdit() != c.edit || s.CanSeePingKey() != c.pingKey || s.CanAdminProject() != c.adminProj || s.CanOwnOrg() != c.ownOrg {
			t.Errorf("role %s: operate=%v edit=%v pingkey=%v adminproj=%v own=%v", c.role, s.CanOperate(), s.CanEdit(), s.CanSeePingKey(), s.CanAdminProject(), s.CanOwnOrg())
		}
	}
	if !(Scope{KeyID: "k"}).IsKey() || (Scope{UserID: "u"}).IsKey() {
		t.Error("IsKey")
	}
}

func TestValidationErrorAndSentinels(t *testing.T) {
	ve := &ValidationError{}
	if ve.OrNil() != nil {
		t.Error("empty must be nil")
	}
	ve.Add("grace", "must be at least 60s")
	ve.Addf("name", "at most %d characters", 120)
	err := ve.OrNil()
	if err == nil || !strings.Contains(err.Error(), "grace: must be at least 60s") {
		t.Errorf("error text: %v", err)
	}
	if _, ok := AsValidation(err); !ok {
		t.Error("AsValidation")
	}
	if _, ok := AsValidation(ErrNotFound); ok {
		t.Error("AsValidation on sentinel")
	}
	if !IsID(NewID()) || IsID("nope") {
		t.Error("IDs")
	}
	if FromMillis(Millis(time.Unix(1700000000, 0))).Unix() != 1700000000 {
		t.Error("millis round trip")
	}
	now := time.Now()
	if FromMillisPtr(MillisPtr(&now)).UnixMilli() != now.UnixMilli() || MillisPtr(nil) != nil || FromMillisPtr(nil) != nil {
		t.Error("pointer millis")
	}
}

func TestRouteValidate(t *testing.T) {
	r := &Route{ChannelIDs: []string{"c"}, On: []State{StateDown, StateUp}, RepeatEvery: 4 * time.Hour, MatchTags: []string{"prod"}}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	if !r.Fires(StateDown) || r.Fires(StateLate) {
		t.Error("Fires")
	}
	bad := &Route{On: []State{StatePaused}, RepeatEvery: time.Minute}
	ve, ok := AsValidation(bad.Validate())
	if !ok || len(ve.Errors) < 3 {
		t.Errorf("expected channels, on and repeat_every errors, got %v", ve)
	}
}
