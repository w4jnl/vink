package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/w4jnl/vink/internal/domain"
)

// GotifyConfig targets a Gotify server with an application token.
type GotifyConfig struct {
	URL      string `json:"url"`
	Token    string `json:"token"`
	Priority int    `json:"priority,omitempty"`
}

// Gotify posts to /message with the token header; the monitor link is
// the notification's click action.
type Gotify struct {
	client *http.Client
}

func (g *Gotify) Kind() domain.ChannelKind { return domain.ChannelGotify }

func parseGotify(cfg json.RawMessage) (GotifyConfig, error) {
	var c GotifyConfig
	if err := decodeConfig(cfg, &c); err != nil {
		return c, validationError(err.Error())
	}
	u, err := url.Parse(strings.TrimSpace(c.URL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return c, validationError("url must be the Gotify server, an absolute http(s) URL")
	}
	c.URL = strings.TrimRight(u.String(), "/")
	c.Token = strings.TrimSpace(c.Token)
	if c.Token == "" {
		return c, validationError("token must be an application token")
	}
	if c.Priority < 0 || c.Priority > 10 {
		return c, validationError("priority must be between 0 and 10")
	}
	return c, nil
}

func (g *Gotify) Validate(cfg json.RawMessage) error {
	_, err := parseGotify(cfg)
	return err
}

func (g *Gotify) Send(ctx context.Context, cfg json.RawMessage, n Notification) error {
	c, err := parseGotify(cfg)
	if err != nil {
		return err
	}
	priority := c.Priority
	if priority == 0 {
		switch n.Kind() {
		case "down":
			priority = 8
		case "late":
			priority = 5
		default:
			priority = 2
		}
	}
	extras := map[string]any{"client::display": map[string]any{"contentType": "text/plain"}}
	if n.Links.Monitor != "" {
		extras["client::notification"] = map[string]any{"click": map[string]any{"url": n.Links.Monitor}}
	}
	body := map[string]any{"title": n.Title(), "message": n.Text(), "priority": priority, "extras": extras}
	return sendJSON(ctx, g.client, http.MethodPost, c.URL+"/message", map[string]string{"X-Gotify-Key": c.Token}, body, "gotify")
}

var _ Notifier = (*Gotify)(nil)
