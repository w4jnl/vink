package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/w4jnl/vink/internal/domain"
)

// NtfyConfig targets a self-hosted or public ntfy server.
type NtfyConfig struct {
	URL      string `json:"url"`
	Topic    string `json:"topic"`
	Token    string `json:"token,omitempty"`
	Priority int    `json:"priority,omitempty"`
}

// Ntfy publishes to an ntfy topic with title, priority, tags and the
// monitor link as the click action.
type Ntfy struct {
	client *http.Client
}

func (n *Ntfy) Kind() domain.ChannelKind { return domain.ChannelNtfy }

func parseNtfy(cfg json.RawMessage) (NtfyConfig, error) {
	var c NtfyConfig
	if err := decodeConfig(cfg, &c); err != nil {
		return c, validationError(err.Error())
	}
	u, err := url.Parse(strings.TrimSpace(c.URL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return c, validationError("url must be the ntfy server, an absolute http(s) URL")
	}
	c.URL = strings.TrimRight(u.String(), "/")
	c.Topic = strings.Trim(strings.TrimSpace(c.Topic), "/")
	if c.Topic == "" || strings.ContainsAny(c.Topic, "/ ") {
		return c, validationError("topic must be a single ntfy topic name")
	}
	if c.Priority < 0 || c.Priority > 5 {
		return c, validationError("priority must be between 1 and 5")
	}
	return c, nil
}

func (n *Ntfy) Validate(cfg json.RawMessage) error {
	_, err := parseNtfy(cfg)
	return err
}

func (n *Ntfy) Send(ctx context.Context, cfg json.RawMessage, no Notification) error {
	c, err := parseNtfy(cfg)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+"/"+c.Topic, strings.NewReader(no.Text()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	req.Header.Set("User-Agent", userAgent(""))
	req.Header.Set("Title", no.Title())
	req.Header.Set("Tags", ntfyTags(no))
	priority := c.Priority
	if priority == 0 {
		priority = ntfyPriority(no)
	}
	req.Header.Set("Priority", fmt.Sprint(priority))
	if no.Links.Monitor != "" {
		req.Header.Set("Click", no.Links.Monitor)
	}
	if no.Links.Ack != "" {
		req.Header.Set("Actions", "view, Acknowledge, "+no.Links.Ack)
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := n.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("ntfy returned %s: %s", resp.Status, strings.TrimSpace(string(snippet)))
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return nil
}

func ntfyTags(n Notification) string {
	tags := []string{"vink", n.Kind()}
	tags = append(tags, n.Monitor.Tags...)
	return strings.Join(tags, ",")
}

func ntfyPriority(n Notification) int {
	switch n.Kind() {
	case "down":
		return 4
	case "late":
		return 3
	default:
		return 2
	}
}

var _ Notifier = (*Ntfy)(nil)
