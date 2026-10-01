// Package agent is the probe that runs inside a closed network: it dials
// out to the server over one WebSocket, takes the checks it is assigned,
// runs them with the same internal/checks package the server uses, and
// reports the results. It keeps no state on disk and never listens.
package agent

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/w4jnl/vink/internal/agentproto"
	"github.com/w4jnl/vink/internal/checks"
	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/outbound"
)

// Options configure one agent.
type Options struct {
	// Server is the vink base URL: wss://vink.example.com (https:// and
	// http:// are accepted and mapped to the socket scheme).
	Server string
	// Token is the agent token from the org settings.
	Token string
	// Labels are announced on first contact; after that the server's
	// labels are the truth.
	Labels map[string]string
	// CAPem is a path to extra CA certificates for the server and for
	// the checks.
	CAPem string
	// Pin is the SHA-256 of the server's certificate (hex, colons
	// optional, "sha256:" prefix optional). When set, the connection
	// is accepted on the pin alone and the CA chain is not consulted.
	Pin string
	// Proxy is an http(s) proxy for the server connection and the
	// http checks.
	Proxy string
	// Version is the build the agent announces.
	Version string
	// Log receives the agent's logs.
	Log *slog.Logger
	// MinBackoff and MaxBackoff bound the reconnect delay (1 s .. 60 s).
	MinBackoff, MaxBackoff time.Duration
	// Heartbeat overrides the heartbeat interval; tests shorten it.
	Heartbeat time.Duration
}

// Agent is one running probe.
type Agent struct {
	o        Options
	log      *slog.Logger
	url      string
	client   *http.Client
	registry *checks.Registry
	pin      []byte

	mu   sync.Mutex
	runs map[string]*run
	// connected reports the current session, for tests.
	connected bool
}

// run is one assigned monitor's loop.
type run struct {
	id       string
	kind     domain.Kind
	spec     *domain.PullSpec
	version  string
	interval time.Duration
	cancel   context.CancelFunc
}

// New validates the options and prepares the checker registry; the
// agent reaches private targets, which is what it is for.
func New(o Options) (*Agent, error) {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.MinBackoff <= 0 {
		o.MinBackoff = time.Second
	}
	if o.MaxBackoff <= 0 {
		o.MaxBackoff = time.Minute
	}
	if o.Heartbeat <= 0 {
		o.Heartbeat = agentproto.HeartbeatEvery
	}
	if strings.TrimSpace(o.Token) == "" {
		return nil, errors.New("an agent token is required")
	}
	u, err := socketURL(o.Server)
	if err != nil {
		return nil, err
	}
	pin, err := parsePin(o.Pin)
	if err != nil {
		return nil, err
	}
	env, err := outbound.New(outbound.Options{Proxy: o.Proxy, CAPem: o.CAPem, AllowPrivateTargets: true})
	if err != nil {
		return nil, err
	}
	transport := env.Transport.Clone()
	if pin != nil {
		transport.TLSClientConfig = &tls.Config{
			InsecureSkipVerify: true, //nolint:gosec // G402: the pin below is the verification; the chain is deliberately not consulted
			MinVersion:         tls.VersionTLS12,
			VerifyConnection: func(cs tls.ConnectionState) error {
				if len(cs.PeerCertificates) == 0 {
					return errors.New("server sent no certificate")
				}
				sum := sha256.Sum256(cs.PeerCertificates[0].Raw)
				if !bytesEqual(sum[:], pin) {
					return fmt.Errorf("server certificate does not match the pin (got sha256:%s)", hex.EncodeToString(sum[:]))
				}
				return nil
			},
		}
	}
	registry, err := checks.NewRegistry(checks.Options{
		Outbound:  outbound.Options{Proxy: o.Proxy, CAPem: o.CAPem, AllowPrivateTargets: true},
		UserAgent: "vink-agent/" + o.Version,
	})
	if err != nil {
		return nil, err
	}
	return &Agent{
		o: o, log: o.Log, url: u, pin: pin, registry: registry, runs: map[string]*run{},
		client: &http.Client{Transport: transport, Timeout: 30 * time.Second},
	}, nil
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := range a {
		v |= a[i] ^ b[i]
	}
	return v == 0
}

// socketURL maps the server base URL to the gateway endpoint.
func socketURL(server string) (string, error) {
	s := strings.TrimRight(strings.TrimSpace(server), "/")
	if s == "" {
		return "", errors.New("a server URL is required, like wss://vink.example.com")
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", fmt.Errorf("server url: %w", err)
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	case "ws", "wss":
	default:
		return "", fmt.Errorf("server url: want wss:// or https://, got %q", u.Scheme)
	}
	if u.Host == "" {
		return "", errors.New("server url: no host")
	}
	u.Path = strings.TrimSuffix(u.Path, agentproto.Path) + agentproto.Path
	u.RawQuery, u.Fragment = "", ""
	return u.String(), nil
}

// parsePin reads a SHA-256 fingerprint.
func parsePin(s string) ([]byte, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return nil, nil
	}
	s = strings.TrimPrefix(s, "sha256:")
	s = strings.ReplaceAll(s, ":", "")
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != sha256.Size {
		return nil, errors.New("pin: want the SHA-256 of the server certificate as 64 hex digits")
	}
	return b, nil
}

// Connected reports whether a session is open.
func (a *Agent) Connected() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.connected
}

// Assigned lists the monitor ids the agent currently runs.
func (a *Agent) Assigned() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]string, 0, len(a.runs))
	for id := range a.runs {
		out = append(out, id)
	}
	return out
}

// Run connects and reconnects with backoff until ctx ends.
func (a *Agent) Run(ctx context.Context) error {
	backoff := a.o.MinBackoff
	for {
		start := time.Now()
		err := a.session(ctx)
		a.stopAll()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Since(start) > a.o.MaxBackoff {
			backoff = a.o.MinBackoff
		}
		var status websocket.CloseError
		switch {
		case errors.As(err, &status) && status.Code == websocket.StatusPolicyViolation:
			a.log.Warn("server closed the session", "reason", status.Reason, "retry_in", backoff.String())
		case err != nil:
			a.log.Warn("session ended", "err", err, "retry_in", backoff.String())
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > a.o.MaxBackoff {
			backoff = a.o.MaxBackoff
		}
	}
}

// session is one connection: hello, then assignments in and results out
// until the socket closes.
func (a *Agent) session(ctx context.Context) error {
	dialCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	h := http.Header{}
	h.Set("Authorization", "Bearer "+a.o.Token)
	h.Set("User-Agent", "vink-agent/"+a.o.Version)
	ws, resp, err := websocket.Dial(dialCtx, a.url, &websocket.DialOptions{HTTPClient: a.client, HTTPHeader: h}) //nolint:bodyclose // the library owns the handshake body
	if err != nil {
		if resp != nil {
			switch resp.StatusCode {
			case http.StatusUnauthorized:
				return errors.New("the server rejected the token: revoked, or for another server")
			case http.StatusNotFound:
				return errors.New("the server has no agent gateway at " + a.url)
			}
			return fmt.Errorf("handshake: %s", resp.Status)
		}
		return err
	}
	ws.SetReadLimit(1 << 20)
	s := &sessionConn{ws: ws}
	defer func() { _ = ws.CloseNow() }()

	kinds := a.registry.Kinds()
	checks := make([]string, len(kinds))
	for i, k := range kinds {
		checks[i] = string(k)
	}
	if err := s.write(ctx, agentproto.Message{Type: agentproto.TypeHello, Version: a.o.Version, Labels: a.o.Labels, Checks: checks}); err != nil {
		return fmt.Errorf("hello: %w", err)
	}
	a.setConnected(true)
	defer a.setConnected(false)
	a.log.Info("connected", "server", a.url)

	sessionCtx, stop := context.WithCancel(ctx)
	defer stop()
	go func() {
		t := time.NewTicker(a.o.Heartbeat)
		defer t.Stop()
		for {
			select {
			case <-sessionCtx.Done():
				return
			case <-t.C:
				if err := s.write(sessionCtx, agentproto.Message{Type: agentproto.TypeHeartbeat}); err != nil {
					return
				}
			}
		}
	}()
	for {
		_, data, err := ws.Read(sessionCtx)
		if err != nil {
			return err
		}
		var msg agentproto.Message
		if err := json.Unmarshal(data, &msg); err != nil {
			a.log.Warn("bad message from server", "err", err)
			continue
		}
		switch msg.Type {
		case agentproto.TypeAssign:
			a.assign(sessionCtx, s, msg)
		case agentproto.TypeRevoke:
			a.revoke(msg.MonitorID)
		default:
			a.log.Debug("ignoring message", "type", msg.Type)
		}
	}
}

func (a *Agent) setConnected(v bool) {
	a.mu.Lock()
	a.connected = v
	a.mu.Unlock()
}

// sessionConn serialises writes on one socket.
type sessionConn struct {
	ws *websocket.Conn
	mu sync.Mutex
}

func (s *sessionConn) write(ctx context.Context, msg agentproto.Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return s.ws.Write(ctx, websocket.MessageText, data)
}

// assign starts, restarts or ignores a monitor's loop.
func (a *Agent) assign(ctx context.Context, s *sessionConn, msg agentproto.Message) {
	spec, err := domain.ParsePullSpec(msg.Spec)
	if err == nil {
		err = spec.Validate(domain.Kind(msg.Kind))
	}
	if err != nil {
		a.log.Error("assignment refused", "monitor_id", msg.MonitorID, "err", err)
		return
	}
	interval := spec.Interval.Std()
	if interval <= 0 {
		interval = domain.DefaultInterval.Std()
	}
	a.mu.Lock()
	if cur, ok := a.runs[msg.MonitorID]; ok {
		if cur.version == msg.Version {
			a.mu.Unlock()
			return
		}
		cur.cancel()
	}
	runCtx, cancel := context.WithCancel(ctx)
	r := &run{id: msg.MonitorID, kind: domain.Kind(msg.Kind), spec: spec, version: msg.Version, interval: interval, cancel: cancel}
	a.runs[msg.MonitorID] = r
	a.mu.Unlock()
	a.log.Info("assigned", "monitor_id", r.id, "kind", r.kind, "interval", interval.String())
	go a.loop(runCtx, s, r)
}

func (a *Agent) revoke(id string) {
	a.mu.Lock()
	r, ok := a.runs[id]
	if ok {
		delete(a.runs, id)
	}
	a.mu.Unlock()
	if ok {
		r.cancel()
		a.log.Info("revoked", "monitor_id", id)
	}
}

func (a *Agent) stopAll() {
	a.mu.Lock()
	runs := a.runs
	a.runs = map[string]*run{}
	a.mu.Unlock()
	for _, r := range runs {
		r.cancel()
	}
}

// loop runs the check at its interval, the first attempt at once, and
// reports each verdict.
func (a *Agent) loop(ctx context.Context, s *sessionConn, r *run) {
	t := time.NewTicker(r.interval)
	defer t.Stop()
	for {
		a.attempt(ctx, s, r)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// attempt runs the attempt sequence with the confirm retries, as the
// server does locally, and sends one result for it.
func (a *Agent) attempt(ctx context.Context, s *sessionConn, r *run) {
	started := time.Now().UTC().Truncate(time.Millisecond)
	var res checks.Result
	total := 1
	for i := 0; ; i++ {
		res = a.registry.Attempt(ctx, r.kind, r.spec)
		total = i + 1
		if res.OK || i >= r.spec.Confirm.Retries || ctx.Err() != nil {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(r.spec.Confirm.Delay.Std()):
		}
	}
	if ctx.Err() != nil {
		return
	}
	detail := make(map[string]any, len(res.Detail)+2)
	for k, v := range res.Detail {
		detail[k] = v
	}
	if total > 1 {
		detail["attempt"] = total
		detail["attempts"] = r.spec.Confirm.Retries + 1
	}
	msg := agentproto.Message{
		Type: agentproto.TypeResult, MonitorID: r.id, AttemptAt: started, OK: res.OK, Warn: res.Warn, LatencyMs: res.LatencyMs, Reason: res.Reason, Detail: detail,
	}
	if err := s.write(ctx, msg); err != nil && ctx.Err() == nil {
		a.log.Warn("result not sent", "monitor_id", r.id, "err", err)
	}
}
