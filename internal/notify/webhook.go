package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"text/template"

	"github.com/w4jnl/vink/internal/domain"
)

// WebhookConfig is the generic webhook: the escape hatch for anything
// with an HTTP endpoint.
type WebhookConfig struct {
	URL     string            `json:"url"`
	Method  string            `json:"method,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	// BodyTemplate is a Go text/template over the Payload; empty sends
	// the Payload as JSON.
	BodyTemplate string `json:"body_template,omitempty"`
}

// Webhook posts a JSON payload or a rendered template.
type Webhook struct {
	client *http.Client
}

func (w *Webhook) Kind() domain.ChannelKind { return domain.ChannelWebhook }

func parseWebhook(cfg json.RawMessage) (WebhookConfig, error) {
	var c WebhookConfig
	if err := decodeConfig(cfg, &c); err != nil {
		return c, validationError(err.Error())
	}
	u, err := url.Parse(strings.TrimSpace(c.URL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return c, validationError("url must be an absolute http(s) URL")
	}
	c.URL = u.String()
	c.Method = strings.ToUpper(strings.TrimSpace(c.Method))
	if c.Method == "" {
		c.Method = http.MethodPost
	}
	switch c.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch:
	default:
		return c, validationError("method must be POST, PUT or PATCH")
	}
	if c.BodyTemplate != "" {
		if _, err := template.New("body").Funcs(templateFuncs).Parse(c.BodyTemplate); err != nil {
			return c, validationError("body_template: " + err.Error())
		}
	}
	return c, nil
}

func (w *Webhook) Validate(cfg json.RawMessage) error {
	_, err := parseWebhook(cfg)
	return err
}

var templateFuncs = template.FuncMap{
	"json": func(v any) (string, error) {
		b, err := json.Marshal(v)
		return string(b), err
	},
	"upper": strings.ToUpper,
	"lower": strings.ToLower,
}

func (w *Webhook) Send(ctx context.Context, cfg json.RawMessage, n Notification) error {
	c, err := parseWebhook(cfg)
	if err != nil {
		return err
	}
	payload := n.Payload()
	var body bytes.Buffer
	contentType := "application/json"
	if c.BodyTemplate != "" {
		t, err := template.New("body").Funcs(templateFuncs).Parse(c.BodyTemplate)
		if err != nil {
			return err
		}
		if err := t.Execute(&body, payload); err != nil {
			return fmt.Errorf("render body_template: %w", err)
		}
		contentType = "text/plain; charset=utf-8"
	} else if err := json.NewEncoder(&body).Encode(payload); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, c.Method, c.URL, &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("User-Agent", userAgent(""))
	req.Header.Set("X-Vink-Event", payload.Event)
	for k, v := range c.Headers {
		req.Header.Set(k, v)
	}
	resp, err := w.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("webhook returned %s: %s", resp.Status, strings.TrimSpace(string(snippet)))
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return nil
}

var _ Notifier = (*Webhook)(nil)
