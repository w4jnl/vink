package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/w4jnl/vink/internal/version"
)

// Client talks to /api/v1 with a bearer key.
type Client struct {
	Server string
	Key    string
	HTTP   *http.Client
	// Debug prints each request line and response status to Log.
	Debug bool
	Log   io.Writer
}

// NewClient builds a client with a ten-second timeout.
func NewClient(server, key string) *Client {
	return &Client{Server: strings.TrimRight(server, "/"), Key: key, HTTP: &http.Client{Timeout: 10 * time.Second}, Log: io.Discard}
}

// Problem is the RFC 7807 body the API returns on errors.
type Problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail"`
	Errors []struct {
		Field string `json:"field"`
		Msg   string `json:"msg"`
	} `json:"errors"`
}

// Error prints the title and the first field error, one line.
func (p *Problem) Error() string {
	msg := p.Title
	if len(p.Errors) > 0 {
		msg += ": " + p.Errors[0].Field + " " + p.Errors[0].Msg
	} else if p.Detail != "" {
		msg += ": " + p.Detail
	}
	return msg
}

// Do sends a request. in is JSON-encoded when not nil; out receives the
// decoded response when not nil. Raw returns the body bytes for --json.
func (c *Client) Do(ctx context.Context, method, path string, in, out any) error {
	_, err := c.DoRaw(ctx, method, path, in, out)
	return err
}

// DoRaw is Do returning the response body as well.
func (c *Client) DoRaw(ctx context.Context, method, path string, in, out any) ([]byte, error) {
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(raw)
	}
	url := c.Server + "/api/v1" + path
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, UserError("bad request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.Key)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "vink-cli/"+version.Version)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Debug {
		fmt.Fprintf(c.Log, "> %s %s\n", method, url)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, ServerError(fmt.Errorf("%s: %w", c.Server, err))
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, ServerError(err)
	}
	if c.Debug {
		fmt.Fprintf(c.Log, "< %s (%d bytes)\n", resp.Status, len(raw))
	}
	if resp.StatusCode >= 400 {
		var p Problem
		if json.Unmarshal(raw, &p) == nil && p.Title != "" {
			code := ExitUser
			if resp.StatusCode >= 500 {
				code = ExitServer
			}
			return raw, &ExitError{Code: code, Err: &p}
		}
		code := ExitUser
		if resp.StatusCode >= 500 {
			code = ExitServer
		}
		return raw, &ExitError{Code: code, Err: fmt.Errorf("%s returned %s", url, resp.Status)}
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return raw, ServerError(fmt.Errorf("decode response from %s: %w", url, err))
		}
	}
	return raw, nil
}

// Ping sends a heartbeat ping outside the API, to the ping ingress. key
// is the ping key inside target: wherever the URL is printed, in a debug
// line or an error, it reads <ping key> instead, since the output of a
// job ends up in cron mail and pasted logs, and the key lets anyone who
// reads it ping the project's monitors.
func (c *Client) Ping(ctx context.Context, target, key string, body []byte, contentType string) error {
	hide := func(s string) string {
		if key == "" {
			return s
		}
		return strings.ReplaceAll(s, key, "<ping key>")
	}
	var rdr io.Reader
	if len(body) > 0 {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, rdr)
	if err != nil {
		return UserError("bad ping url: %s", hide(err.Error()))
	}
	req.Header.Set("User-Agent", "vink-cli/"+version.Version)
	if len(body) > 0 {
		if contentType == "" {
			contentType = "text/plain; charset=utf-8"
		}
		req.Header.Set("Content-Type", contentType)
	}
	if c.Debug {
		fmt.Fprintf(c.Log, "> POST %s (%d bytes)\n", hide(target), len(body))
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return ServerError(hiddenError{err: err, hide: hide})
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if c.Debug {
		fmt.Fprintf(c.Log, "< %s\n", resp.Status)
	}
	switch {
	case resp.StatusCode == http.StatusOK:
		return nil
	case resp.StatusCode == http.StatusNotFound:
		return UserError("ping rejected: unknown ping key or monitor (404)")
	case resp.StatusCode == http.StatusTooManyRequests:
		return ServerError(errors.New("ping rate limited (429)"))
	case resp.StatusCode >= 500:
		return ServerError(fmt.Errorf("ping failed: %s", resp.Status))
	default:
		return UserError("ping rejected: %s", resp.Status)
	}
}

// hiddenError is err with the ping key taken out of its message. The
// error stays in the chain, so errors.Is and errors.As still see it.
type hiddenError struct {
	err  error
	hide func(string) string
}

func (e hiddenError) Error() string { return e.hide(e.err.Error()) }
func (e hiddenError) Unwrap() error { return e.err }
