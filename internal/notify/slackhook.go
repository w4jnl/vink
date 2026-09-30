package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/w4jnl/vink/internal/domain"
)

// SlackhookConfig is a Slack-compatible incoming webhook: Slack,
// Mattermost, Rocket.Chat. The URL carries the secret.
type SlackhookConfig struct {
	URL string `json:"url"`
}

// Slackhook posts text with one coloured attachment.
type Slackhook struct {
	client *http.Client
}

func (s *Slackhook) Kind() domain.ChannelKind { return domain.ChannelSlackhook }

func parseSlackhook(cfg json.RawMessage) (SlackhookConfig, error) {
	var c SlackhookConfig
	if err := decodeConfig(cfg, &c); err != nil {
		return c, validationError(err.Error())
	}
	u, err := url.Parse(strings.TrimSpace(c.URL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return c, validationError("url must be the incoming webhook, an absolute http(s) URL")
	}
	c.URL = u.String()
	return c, nil
}

func (s *Slackhook) Validate(cfg json.RawMessage) error {
	_, err := parseSlackhook(cfg)
	return err
}

func (s *Slackhook) Send(ctx context.Context, cfg json.RawMessage, n Notification) error {
	c, err := parseSlackhook(cfg)
	if err != nil {
		return err
	}
	name := n.Monitor.Name
	if name == "" {
		name = n.Monitor.Slug
	}
	attachment := map[string]any{
		"color": colour(n), "title": name, "text": n.Text(), "footer": "vink · " + n.Project.Slug, "ts": n.Event.At.Unix(),
	}
	if n.Links.Monitor != "" {
		attachment["title_link"] = n.Links.Monitor
	}
	body := map[string]any{"text": n.Title(), "username": "vink", "attachments": []any{attachment}}
	return sendJSON(ctx, s.client, http.MethodPost, c.URL, nil, body, "slack webhook")
}

var _ Notifier = (*Slackhook)(nil)
