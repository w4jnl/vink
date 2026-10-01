package domain

import (
	"strings"
	"testing"
	"time"
)

func TestAgentValidateAndLabels(t *testing.T) {
	a := &Agent{Name: " DC2-Probe ", Labels: map[string]string{"Site": " dc2 ", "zone": "dmz"}}
	a.Normalize()
	if a.Name != "dc2-probe" || a.Labels["site"] != "dc2" {
		t.Fatalf("normalised: %+v", a)
	}
	if err := a.Validate(); err != nil {
		t.Fatal(err)
	}
	if LabelsString(a.Labels) != "site=dc2,zone=dmz" {
		t.Errorf("labels string: %s", LabelsString(a.Labels))
	}
	parsed, err := ParseLabels(" site=dc2, zone=dmz ")
	if err != nil || parsed["zone"] != "dmz" {
		t.Fatalf("parse: %v %v", parsed, err)
	}
	if _, err := ParseLabels("site"); err == nil || !strings.Contains(err.Error(), "key=value") {
		t.Errorf("bad labels: %v", err)
	}
	for name, bad := range map[string]Agent{
		"name":  {Name: "Bad Name"},
		"key":   {Name: "ok", Labels: map[string]string{"Site Name": "x"}},
		"value": {Name: "ok", Labels: map[string]string{"site": "a,b"}},
	} {
		b := bad
		b.Normalize()
		if err := b.Validate(); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	if !a.Matches(map[string]string{"site": "dc2"}) || a.Matches(map[string]string{"site": "dc1"}) || !a.Matches(nil) {
		t.Error("selector matching")
	}
	seen := time.Now()
	if (&Agent{}).State(false) != AgentWaiting || (&Agent{LastSeenAt: &seen}).State(false) != AgentOffline || (&Agent{LastSeenAt: &seen}).State(true) != AgentConnected {
		t.Error("states")
	}
	if ParseLabelsJSON(LabelsJSON(a.Labels))["site"] != "dc2" {
		t.Error("json round trip")
	}
}
