package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/w4jnl/vink/internal/version"
	"github.com/w4jnl/vink/ping"
)

// PingClient makes the client `vink ping` and `vink run` send pings with:
// the ping module (github.com/w4jnl/vink/ping) with the vink-cli
// User-Agent, and with debug a request line and a status line for every
// attempt on log, the ping key hidden. opts come last, so a caller can
// tune attempts, timeout or body limit for one use.
func PingClient(base, key string, debug bool, log io.Writer, opts ...ping.Option) (*ping.Client, error) {
	hc := &http.Client{}
	if debug {
		hc.Transport = debugTransport{next: http.DefaultTransport, log: log, key: key}
	}
	pc, err := ping.New(base, key, append([]ping.Option{ping.WithUserAgent("vink-cli/" + version.Version), ping.WithHTTPClient(hc)}, opts...)...)
	if err != nil {
		return nil, UserError("%s", strings.TrimPrefix(err.Error(), "ping: "))
	}
	return pc, nil
}

// PingError gives an error from the ping module the CLI's exit code: 1
// when the caller has something to fix (a wrong key, slug or address, a
// refused ping), 2 when vink is away, failing or rate-limiting. The module
// already keeps the ping key out of its messages.
func PingError(err error) error {
	if err == nil {
		return nil
	}
	var se *ping.StatusError
	var ue *url.Error
	switch {
	case errors.Is(err, ping.ErrNotFound):
		return UserError("ping rejected: unknown ping key or monitor (404)")
	case errors.Is(err, ping.ErrRateLimited):
		return ServerError(errors.New("ping rate limited (429)"))
	case errors.As(err, &se) && se.StatusCode >= 500:
		return ServerError(fmt.Errorf("ping failed: %s", se.Status))
	case errors.As(err, &se):
		return UserError("ping rejected: %s", se.Status)
	case errors.As(err, &ue), errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return ServerError(pingErr{err})
	}
	return UserError("%s", strings.TrimPrefix(err.Error(), "ping: "))
}

// pingErr drops the module's "ping: " prefix from a message the CLI
// prints after its own "vink: ", and keeps the error in the chain.
type pingErr struct{ err error }

func (e pingErr) Error() string { return strings.TrimPrefix(e.err.Error(), "ping: ") }
func (e pingErr) Unwrap() error { return e.err }

// debugTransport prints what -d shows for a ping. The URL holds the ping
// key, and debug output gets pasted into issues, so the key is hidden.
type debugTransport struct {
	next http.RoundTripper
	log  io.Writer
	key  string
}

func (t debugTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	shown := req.URL.String()
	if t.key != "" {
		shown = strings.ReplaceAll(shown, t.key, "<ping key>")
	}
	fmt.Fprintf(t.log, "> %s %s (%d bytes)\n", req.Method, shown, req.ContentLength)
	resp, err := t.next.RoundTrip(req)
	if err == nil {
		fmt.Fprintf(t.log, "< %s\n", resp.Status)
	}
	return resp, err
}
