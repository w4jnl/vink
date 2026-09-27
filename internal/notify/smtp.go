package notify

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"html"
	"mime/multipart"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	"github.com/w4jnl/vink/internal/domain"
)

// SMTPConfig is the instance-level transport from vink.toml.
type SMTPConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	// TLS is starttls, tls or none.
	TLS string
}

// SMTPChannel is the per-channel part: recipients and an optional From.
type SMTPChannel struct {
	To   []string `json:"to"`
	From string   `json:"from,omitempty"`
}

// SMTP sends a text plus HTML alternative through the instance transport.
type SMTP struct {
	cfg  SMTPConfig
	dial func(ctx context.Context, cfg SMTPConfig) (*smtp.Client, error)
}

func (s *SMTP) Kind() domain.ChannelKind { return domain.ChannelSMTP }

func parseSMTPChannel(cfg json.RawMessage) (SMTPChannel, error) {
	var c SMTPChannel
	if err := decodeConfig(cfg, &c); err != nil {
		return c, validationError(err.Error())
	}
	if len(c.To) == 0 {
		return c, validationError("to must list at least one address")
	}
	for i, a := range c.To {
		addr, err := mail.ParseAddress(strings.TrimSpace(a))
		if err != nil {
			return c, validationError(fmt.Sprintf("to[%d]: %q is not an email address", i, a))
		}
		c.To[i] = addr.Address
	}
	if c.From != "" {
		if _, err := mail.ParseAddress(c.From); err != nil {
			return c, validationError("from is not an email address")
		}
	}
	return c, nil
}

func (s *SMTP) Validate(cfg json.RawMessage) error {
	if _, err := parseSMTPChannel(cfg); err != nil {
		return err
	}
	if s.cfg.Host == "" {
		return validationError("no mail server is configured: set [smtp] host in vink.toml")
	}
	return nil
}

func (s *SMTP) Send(ctx context.Context, cfg json.RawMessage, n Notification) error {
	c, err := parseSMTPChannel(cfg)
	if err != nil {
		return err
	}
	if s.cfg.Host == "" {
		return fmt.Errorf("no mail server is configured")
	}
	from := c.From
	if from == "" {
		from = s.cfg.From
	}
	if from == "" {
		from = "vink@" + s.cfg.Host
	}
	msg, err := buildMessage(from, c.To, n)
	if err != nil {
		return err
	}
	client, err := s.dial(ctx, s.cfg)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	if err := client.Mail(envelopeAddress(from)); err != nil {
		return fmt.Errorf("MAIL FROM: %w", err)
	}
	for _, to := range c.To {
		if err := client.Rcpt(to); err != nil {
			return fmt.Errorf("RCPT TO %s: %w", to, err)
		}
	}
	wc, err := client.Data()
	if err != nil {
		return fmt.Errorf("DATA: %w", err)
	}
	if _, err := wc.Write(msg); err != nil {
		return err
	}
	if err := wc.Close(); err != nil {
		return fmt.Errorf("DATA: %w", err)
	}
	return client.Quit()
}

func envelopeAddress(from string) string {
	if a, err := mail.ParseAddress(from); err == nil {
		return a.Address
	}
	return from
}

// dialSMTP connects with implicit TLS, STARTTLS or plain, then
// authenticates when a username is set.
func dialSMTP(ctx context.Context, cfg SMTPConfig) (*smtp.Client, error) {
	port := cfg.Port
	if port == 0 {
		port = 587
	}
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(port))
	d := &net.Dialer{Timeout: 10 * time.Second}
	var (
		conn net.Conn
		err  error
	)
	tlsCfg := &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}
	mode := strings.ToLower(cfg.TLS)
	if mode == "tls" {
		conn, err = (&tls.Dialer{NetDialer: d, Config: tlsCfg}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return nil, fmt.Errorf("connect %s: %w", addr, err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	client, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if mode == "starttls" || mode == "" {
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err := client.StartTLS(tlsCfg); err != nil {
				_ = client.Close()
				return nil, fmt.Errorf("starttls: %w", err)
			}
		} else if mode == "starttls" {
			_ = client.Close()
			return nil, fmt.Errorf("server %s does not offer STARTTLS; set smtp.tls = \"none\" to send in clear", addr)
		}
	}
	if cfg.Username != "" {
		if err := client.Auth(smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)); err != nil {
			_ = client.Close()
			return nil, fmt.Errorf("auth: %w", err)
		}
	}
	return client, nil
}

// buildMessage renders a multipart/alternative message with text first.
func buildMessage(from string, to []string, n Notification) ([]byte, error) {
	var buf bytes.Buffer
	mp := multipart.NewWriter(&buf)
	headers := []string{
		"From: " + from,
		"To: " + strings.Join(to, ", "),
		"Subject: " + n.Title(),
		"Date: " + time.Now().UTC().Format(time.RFC1123Z),
		"Message-ID: <" + n.Event.ID + "." + n.Monitor.ID + "@vink>",
		"MIME-Version: 1.0",
		"X-Vink-Event: " + n.Kind(),
		"Auto-Submitted: auto-generated",
		"Content-Type: multipart/alternative; boundary=" + mp.Boundary(),
	}
	buf.WriteString(strings.Join(headers, "\r\n") + "\r\n\r\n")
	text, err := mp.CreatePart(textproto.MIMEHeader{"Content-Type": {"text/plain; charset=utf-8"}, "Content-Transfer-Encoding": {"8bit"}})
	if err != nil {
		return nil, err
	}
	if _, err := text.Write([]byte(strings.ReplaceAll(n.Text(), "\n", "\r\n"))); err != nil {
		return nil, err
	}
	htmlPart, err := mp.CreatePart(textproto.MIMEHeader{"Content-Type": {"text/html; charset=utf-8"}, "Content-Transfer-Encoding": {"8bit"}})
	if err != nil {
		return nil, err
	}
	if _, err := htmlPart.Write([]byte(htmlBody(n))); err != nil {
		return nil, err
	}
	if err := mp.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// htmlBody is a minimal HTML alternative: the text in a pre block and the
// links as anchors. No images, no tracking, no external assets.
func htmlBody(n Notification) string {
	var b strings.Builder
	b.WriteString("<!doctype html><html><body style=\"font-family:system-ui,sans-serif;color:#14181A\">")
	fmt.Fprintf(&b, "<p><strong>%s</strong></p>", html.EscapeString(n.Title()))
	fmt.Fprintf(&b, "<pre style=\"font-family:ui-monospace,monospace;white-space:pre-wrap\">%s</pre>", html.EscapeString(n.Text()))
	if n.Links.Monitor != "" {
		fmt.Fprintf(&b, "<p><a href=\"%s\">Open the monitor</a></p>", html.EscapeString(n.Links.Monitor))
	}
	if n.Links.Ack != "" {
		fmt.Fprintf(&b, "<p><a href=\"%s\">Acknowledge the incident</a></p>", html.EscapeString(n.Links.Ack))
	}
	b.WriteString("</body></html>")
	return b.String()
}

var _ Notifier = (*SMTP)(nil)
