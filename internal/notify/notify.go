// Package notify sends notifications. Each kind is one file behind the
// Notifier interface, registered by name; adding a kind is one file plus
// a registry line. Nothing here talks to the network unless a channel of
// that kind is configured and an event routes to it.
package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/w4jnl/vink/internal/domain"
)

// Notifier sends one notification for one channel kind.
type Notifier interface {
	Kind() domain.ChannelKind
	// Validate is called on channel create and update.
	Validate(cfg json.RawMessage) error
	Send(ctx context.Context, cfg json.RawMessage, n Notification) error
}

// Links are the URLs a notification points at.
type Links struct {
	Monitor  string `json:"monitor"`
	Incident string `json:"incident,omitempty"`
	Ack      string `json:"ack,omitempty"`
}

// Notification is what every notifier renders.
type Notification struct {
	Event    domain.Event
	Monitor  domain.Monitor
	Project  domain.Project
	Incident *domain.Incident
	Repeat   bool
	Links    Links
	// Test marks the synthetic notification sent by a channel test.
	Test bool
}

// Kind is the short event word: down, up, late or test.
func (n Notification) Kind() string {
	if n.Test {
		return "test"
	}
	return string(n.Event.To)
}

// Title is the one-line summary: "[vink] DOWN nightly-backup (homelab)".
func (n Notification) Title() string {
	word := strings.ToUpper(n.Kind())
	if n.Repeat {
		word = "STILL " + word
	}
	return fmt.Sprintf("[vink] %s %s (%s)", word, n.Monitor.Slug, n.Project.Slug)
}

// Text is the plain-text body.
func (n Notification) Text() string {
	var b strings.Builder
	name := n.Monitor.Name
	if name == "" {
		name = n.Monitor.Slug
	}
	switch {
	case n.Test:
		fmt.Fprintf(&b, "This is a test notification from vink for project %s.\n", n.Project.Name)
	case n.Repeat:
		fmt.Fprintf(&b, "%s is still %s since %s.\n", name, n.Event.To, n.Event.At.UTC().Format(time.RFC3339))
	default:
		fmt.Fprintf(&b, "%s is %s.\n", name, n.Event.To)
	}
	if !n.Test {
		fmt.Fprintf(&b, "\nmonitor   %s\nproject   %s\nstate     %s (was %s)\nat        %s\n", n.Monitor.Slug, n.Project.Slug, n.Event.To, n.Event.From, n.Event.At.UTC().Format(time.RFC3339))
		if n.Event.Reason != "" {
			fmt.Fprintf(&b, "reason    %s\n", n.Event.Reason)
		}
		if len(n.Monitor.Tags) > 0 {
			fmt.Fprintf(&b, "tags      %s\n", strings.Join(n.Monitor.Tags, ", "))
		}
		if n.Incident != nil {
			fmt.Fprintf(&b, "incident  opened %s\n", n.Incident.OpenedAt.UTC().Format(time.RFC3339))
		}
	}
	if n.Links.Monitor != "" {
		fmt.Fprintf(&b, "\n%s\n", n.Links.Monitor)
	}
	if n.Links.Ack != "" {
		fmt.Fprintf(&b, "acknowledge: %s\n", n.Links.Ack)
	}
	return b.String()
}

// Payload is the default JSON body for webhooks and the template context.
type Payload struct {
	Event     string     `json:"event"`
	Title     string     `json:"title"`
	Text      string     `json:"text"`
	At        time.Time  `json:"at"`
	Repeat    bool       `json:"repeat"`
	Reason    string     `json:"reason,omitempty"`
	Monitor   pMonitor   `json:"monitor"`
	Project   pProject   `json:"project"`
	Incident  *pIncident `json:"incident,omitempty"`
	FromState string     `json:"from_state,omitempty"`
	ToState   string     `json:"to_state,omitempty"`
	Links     Links      `json:"links"`
}

type pMonitor struct {
	ID         string    `json:"id"`
	Slug       string    `json:"slug"`
	Name       string    `json:"name"`
	Kind       string    `json:"kind"`
	Tags       []string  `json:"tags"`
	State      string    `json:"state"`
	StateSince time.Time `json:"state_since"`
}

type pProject struct {
	Slug     string `json:"slug"`
	Name     string `json:"name"`
	Timezone string `json:"timezone"`
}

type pIncident struct {
	ID       string     `json:"id"`
	OpenedAt time.Time  `json:"opened_at"`
	AckedAt  *time.Time `json:"acked_at,omitempty"`
}

// Payload builds the JSON form.
func (n Notification) Payload() Payload {
	tags := n.Monitor.Tags
	if tags == nil {
		tags = []string{}
	}
	p := Payload{
		Event: n.Kind(), Title: n.Title(), Text: n.Text(), At: n.Event.At.UTC(), Repeat: n.Repeat, Reason: n.Event.Reason,
		Monitor:   pMonitor{ID: n.Monitor.ID, Slug: n.Monitor.Slug, Name: n.Monitor.Name, Kind: string(n.Monitor.Kind), Tags: tags, State: string(n.Monitor.State), StateSince: n.Monitor.StateSince.UTC()},
		Project:   pProject{Slug: n.Project.Slug, Name: n.Project.Name, Timezone: n.Project.Timezone},
		FromState: string(n.Event.From), ToState: string(n.Event.To), Links: n.Links,
	}
	if n.Incident != nil {
		p.Incident = &pIncident{ID: n.Incident.ID, OpenedAt: n.Incident.OpenedAt.UTC(), AckedAt: n.Incident.AckedAt}
	}
	return p
}

// Registry maps kinds to notifiers.
type Registry struct {
	notifiers map[domain.ChannelKind]Notifier
}

// NewRegistry registers every notifier: smtp, webhook, ntfy, gotify,
// matrix, slackhook and alertmanager.
func NewRegistry(o Options) (*Registry, error) {
	client, err := newHTTPClient(o)
	if err != nil {
		return nil, err
	}
	r := &Registry{notifiers: map[domain.ChannelKind]Notifier{}}
	r.Register(&SMTP{cfg: o.SMTP, dial: dialSMTP})
	r.Register(&Webhook{client: client})
	r.Register(&Ntfy{client: client})
	r.Register(&Gotify{client: client})
	r.Register(&Matrix{client: client})
	r.Register(&Slackhook{client: client})
	r.Register(&Alertmanager{client: client})
	return r, nil
}

// Register adds or replaces a notifier.
func (r *Registry) Register(n Notifier) { r.notifiers[n.Kind()] = n }

// Kinds lists the registered kinds in order.
func (r *Registry) Kinds() []domain.ChannelKind {
	out := make([]domain.ChannelKind, 0, len(r.notifiers))
	for k := range r.notifiers {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Validate checks a channel config for its kind.
func (r *Registry) Validate(kind domain.ChannelKind, cfg []byte) error {
	n, ok := r.notifiers[kind]
	if !ok {
		return fmt.Errorf("channel kind %q is not available in this build", kind)
	}
	return n.Validate(cfg)
}

// Send delivers n through the notifier for kind.
func (r *Registry) Send(ctx context.Context, kind domain.ChannelKind, cfg []byte, n Notification) error {
	nt, ok := r.notifiers[kind]
	if !ok {
		return fmt.Errorf("channel kind %q is not available in this build", kind)
	}
	return nt.Send(ctx, cfg, n)
}

// decodeConfig strictly decodes a channel config.
func decodeConfig(cfg json.RawMessage, v any) error {
	dec := json.NewDecoder(strings.NewReader(string(cfg)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	return nil
}

// validationError builds a field-level error under "config".
func validationError(msg string) error {
	return &domain.ValidationError{Errors: []domain.FieldError{{Field: "config", Msg: msg}}}
}
