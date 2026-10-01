// Package audit is the record of who changed what: one entry per admin
// action, written by the service in the same transaction as the change,
// and the request facts (how the call arrived) that the HTTP layer and
// the CLI put in the context.
package audit

import (
	"context"
	"strings"
)

// Actor kinds.
const (
	KindUser   = "user"
	KindKey    = "key"
	KindSystem = "system"
)

// Via values: how the action arrived.
const (
	ViaWeb = "web"
	ViaAPI = "api"
	ViaCLI = "cli"
)

// Entry is one action to record. OrgID and ProjectID say where it shows:
// a project action carries both, an org action the org only, an instance
// action neither.
type Entry struct {
	Action    string
	Target    string
	TargetID  string
	OrgID     string
	ProjectID string
	// Before and After are the stored spec as YAML, secrets as ***, for
	// a change; empty otherwise.
	Before string
	After  string
	// Detail is small structured data for the sentence: changed fields,
	// counts, a role from and to.
	Detail map[string]any
}

// Request is how a call reached vink; the HTTP layer sets it per request
// and the CLI sets via to cli.
type Request struct {
	Via        string
	RequestID  string
	RemoteAddr string
}

type ctxKey struct{}

// WithRequest attaches the request facts to the context.
func WithRequest(ctx context.Context, r Request) context.Context {
	return context.WithValue(ctx, ctxKey{}, r)
}

// RequestFrom reads the request facts; zero when none were set.
func RequestFrom(ctx context.Context) Request {
	r, _ := ctx.Value(ctxKey{}).(Request)
	return r
}

// IsAccess says whether an action is an access event (sign-ins, roles,
// invites, keys, two-factor) rather than a change to configuration.
func IsAccess(action string) bool {
	for _, p := range []string{"user.", "member.", "invite."} {
		if strings.HasPrefix(action, p) {
			return true
		}
	}
	return strings.Contains(action, "key.")
}

// IsState says whether an action is a state flip from the events table.
func IsState(action string) bool { return strings.HasPrefix(action, "state.") }
