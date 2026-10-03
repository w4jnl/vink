package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/w4jnl/vink/internal/domain"
)

// AlertmanagerConfig posts alerts to Prometheus Alertmanager.
type AlertmanagerConfig struct {
	URL    string            `json:"url"`
	Labels map[string]string `json:"labels,omitempty"`
}

// Alertmanager fires MonitorDown with the monitor, project and tags as
// labels, and resolves it with endsAt when the monitor comes up.
type Alertmanager struct {
	client *http.Client
}

func (a *Alertmanager) Kind() domain.ChannelKind { return domain.ChannelAlertmanager }

var labelNameRe = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

func parseAlertmanager(cfg json.RawMessage) (AlertmanagerConfig, error) {
	var c AlertmanagerConfig
	if err := decodeConfig(cfg, &c); err != nil {
		return c, validationError(err.Error())
	}
	u, err := url.Parse(strings.TrimSpace(c.URL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return c, validationError("url must be the Alertmanager base, an absolute http(s) URL")
	}
	c.URL = strings.TrimRight(u.String(), "/")
	for k := range c.Labels {
		if !labelNameRe.MatchString(k) {
			return c, validationError("labels: " + k + " is not a label name")
		}
	}
	return c, nil
}

func (a *Alertmanager) Validate(cfg json.RawMessage) error {
	_, err := parseAlertmanager(cfg)
	return err
}

// labelName turns a tag into a label suffix.
func labelName(tag string) string {
	var b strings.Builder
	for _, r := range tag {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

func (a *Alertmanager) Send(ctx context.Context, cfg json.RawMessage, n Notification) error {
	c, err := parseAlertmanager(cfg)
	if err != nil {
		return err
	}
	labels := map[string]string{"alertname": "MonitorDown", "monitor": n.Monitor.Slug, "project": n.Project.Slug, "kind": string(n.Monitor.Kind), "severity": severity(n), "source": "vink"}
	switch n.Kind() {
	case "late":
		labels["alertname"] = "MonitorLate"
	case "test":
		labels["alertname"] = "VinkTest"
	}
	if len(n.Monitor.Tags) > 0 {
		labels["tags"] = strings.Join(n.Monitor.Tags, ",")
		for _, t := range n.Monitor.Tags {
			labels["tag_"+labelName(t)] = "true"
		}
	}
	for k, v := range c.Labels {
		labels[k] = v
	}
	annotations := map[string]string{"summary": n.Title(), "description": n.Text()}
	if n.Event.Reason != "" {
		annotations["reason"] = n.Event.Reason
	}
	if n.Message != "" {
		annotations["message"] = n.Message
	}
	if n.ExitCode != nil {
		annotations["exit_code"] = fmt.Sprint(*n.ExitCode)
	}
	if n.Links.Monitor != "" {
		annotations["monitor_url"] = n.Links.Monitor
	}
	startsAt := n.Event.At
	if n.Incident != nil && !n.Incident.OpenedAt.IsZero() {
		startsAt = n.Incident.OpenedAt
	}
	alert := map[string]any{"labels": labels, "annotations": annotations, "startsAt": startsAt.UTC().Format(time.RFC3339)}
	if n.Links.Monitor != "" {
		alert["generatorURL"] = n.Links.Monitor
	}
	if n.Kind() == "up" {
		// resolve: the same labels with an end
		alert["endsAt"] = n.Event.At.UTC().Format(time.RFC3339)
	}
	return sendJSON(ctx, a.client, http.MethodPost, c.URL+"/api/v2/alerts", nil, []any{alert}, "alertmanager")
}

var _ Notifier = (*Alertmanager)(nil)
