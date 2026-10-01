package domain

import (
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Agent is a probe that dials out to vink and runs pull checks from a
// network vink cannot reach. It belongs to an org; monitors name it by
// name or by labels.
type Agent struct {
	ID          string
	OrgID       string
	Name        string
	Labels      map[string]string
	TokenPrefix string
	Version     string
	LastSeenAt  *time.Time
	LastAddr    string
	CreatedAt   time.Time
}

// AgentState is what the org page shows for an agent.
type AgentState string

const (
	// AgentConnected has a live connection.
	AgentConnected AgentState = "connected"
	// AgentOffline connected before and is gone longer than offline_after.
	AgentOffline AgentState = "offline"
	// AgentWaiting was created and never connected.
	AgentWaiting AgentState = "waiting"
)

var labelKeyRe = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,31}$`)

// Normalize trims the name and the labels.
func (a *Agent) Normalize() {
	a.Name = strings.ToLower(strings.TrimSpace(a.Name))
	a.Version = strings.TrimSpace(a.Version)
	for k, v := range a.Labels {
		nk, nv := strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v)
		if nk != k || nv != v {
			delete(a.Labels, k)
			a.Labels[nk] = nv
		}
	}
}

// Validate checks the name and the labels.
func (a *Agent) Validate() error {
	ve := &ValidationError{}
	if !ValidSlug(a.Name) {
		ve.Add("name", "must be lowercase letters, digits and dashes, at most 64 characters")
	}
	if len(a.Labels) > 20 {
		ve.Add("labels", "at most 20 labels")
	}
	for k, v := range a.Labels {
		if !labelKeyRe.MatchString(k) {
			ve.Addf("labels", "%q is not a label name", k)
		}
		if v == "" || len(v) > 64 || strings.ContainsAny(v, ", =") {
			ve.Addf("labels", "%s needs a value without commas, spaces or =", k)
		}
	}
	return ve.OrNil()
}

// State derives the agent's state: connected while the gateway holds
// its socket, waiting when it never connected, offline otherwise.
func (a *Agent) State(connected bool) AgentState {
	switch {
	case connected:
		return AgentConnected
	case a.LastSeenAt == nil:
		return AgentWaiting
	}
	return AgentOffline
}

// ParseLabels reads "site=dc2,zone=dmz".
func ParseLabels(s string) (map[string]string, error) {
	out := map[string]string{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, v, ok := strings.Cut(part, "=")
		k, v = strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v)
		if !ok || k == "" || v == "" {
			return nil, (&ValidationError{Errors: []FieldError{{Field: "labels", Msg: part + " is not key=value"}}}).OrNil()
		}
		if !labelKeyRe.MatchString(k) {
			return nil, (&ValidationError{Errors: []FieldError{{Field: "labels", Msg: strconv.Quote(k) + " is not a label name"}}}).OrNil()
		}
		if strings.ContainsAny(v, ", =") {
			return nil, (&ValidationError{Errors: []FieldError{{Field: "labels", Msg: k + " needs a value without commas, spaces or ="}}}).OrNil()
		}
		out[k] = v
	}
	return out, nil
}

// LabelsString writes labels as "site=dc2,zone=dmz", keys sorted.
func LabelsString(labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+labels[k])
	}
	return strings.Join(parts, ",")
}

// LabelsJSON encodes labels for storage.
func LabelsJSON(labels map[string]string) string {
	if labels == nil {
		labels = map[string]string{}
	}
	b, _ := json.Marshal(labels)
	return string(b)
}

// ParseLabelsJSON decodes stored labels.
func ParseLabelsJSON(raw string) map[string]string {
	out := map[string]string{}
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

// Matches reports whether the agent carries every label of the selector.
func (a *Agent) Matches(selector map[string]string) bool {
	for k, v := range selector {
		if a.Labels[k] != v {
			return false
		}
	}
	return true
}

// AgentCommand is the line to run on the agent's host: the server's
// websocket URL, the token and the labels, ready to paste.
func AgentCommand(baseURL, token string, labels map[string]string) string {
	server := strings.TrimRight(baseURL, "/")
	switch {
	case strings.HasPrefix(server, "https://"):
		server = "wss://" + strings.TrimPrefix(server, "https://")
	case strings.HasPrefix(server, "http://"):
		server = "ws://" + strings.TrimPrefix(server, "http://")
	}
	cmd := "vink agent --server " + server + " \\\n  --token " + token
	if ls := LabelsString(labels); ls != "" {
		cmd += " \\\n  --labels " + ls
	}
	return cmd
}
