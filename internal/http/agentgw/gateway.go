// Package agentgw is the server end of the probe agents: one WebSocket per
// agent, authenticated by its token, over which the server hands out
// assignments and the agent sends results and heartbeats.
package agentgw

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/w4jnl/vink/internal/agentproto"
	"github.com/w4jnl/vink/internal/auth"
	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/service"
)

// Gateway holds the live agent connections and keeps their assignments
// in step with the database.
type Gateway struct {
	svc *service.Service
	log *slog.Logger
	now func() time.Time
	// OfflineAfter is how long an agent may stay quiet before its
	// monitors turn late.
	OfflineAfter time.Duration
	// Sweep is how often quiet agents are looked for.
	Sweep time.Duration
	// ReadTimeout bounds the silence on a socket; the agent sends a
	// heartbeat every 30 seconds.
	ReadTimeout time.Duration

	mu    sync.Mutex
	conns map[string]*conn
	wake  chan struct{}
}

// conn is one agent's socket with what it has been told.
type conn struct {
	agent  *domain.Agent
	ws     *websocket.Conn
	ctx    context.Context
	cancel context.CancelFunc
	since  time.Time

	writeMu sync.Mutex
	sent    map[string]string // monitor id → spec version handed over
}

// New wires a gateway on the service and registers itself as the
// service's view of which agents are connected.
func New(svc *service.Service, log *slog.Logger) *Gateway {
	g := &Gateway{
		svc: svc, log: log, now: svc.Now,
		OfflineAfter: 2 * time.Minute, Sweep: 30 * time.Second, ReadTimeout: 90 * time.Second,
		conns: map[string]*conn{}, wake: make(chan struct{}, 1),
	}
	svc.SetAgentPresence(g.Connected)
	svc.SetAgentSince(g.ConnectedSince)
	return g
}

// Mount registers the endpoint.
func (g *Gateway) Mount(mux *http.ServeMux) { mux.Handle("GET "+agentproto.Path, g) }

// Connected reports whether the agent holds a socket right now.
func (g *Gateway) Connected(agentID string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	_, ok := g.conns[agentID]
	return ok
}

// ConnectedSince is when the agent's current socket opened.
func (g *Gateway) ConnectedSince(agentID string) (time.Time, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	c, ok := g.conns[agentID]
	if !ok {
		return time.Time{}, false
	}
	return c.since, true
}

// ConnectedCount is how many agents are connected.
func (g *Gateway) ConnectedCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.conns)
}

// ServeHTTP upgrades one agent connection and serves it until it closes.
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	token, ok := auth.BearerToken(r)
	if !ok {
		w.Header().Set("WWW-Authenticate", `Bearer realm="vink agent"`)
		http.Error(w, "agent token required", http.StatusUnauthorized)
		return
	}
	agent, err := g.svc.VerifyAgentToken(r.Context(), token)
	if err != nil {
		http.Error(w, "agent token rejected", http.StatusUnauthorized)
		return
	}
	// Agents are not browsers and send no Origin; the token is the
	// credential, so the origin check does not apply.
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		g.log.Warn("agent upgrade failed", "agent", agent.Name, "err", err)
		return
	}
	ws.SetReadLimit(1 << 20)
	ctx, cancel := context.WithCancel(r.Context())
	c := &conn{agent: agent, ws: ws, ctx: ctx, cancel: cancel, sent: map[string]string{}, since: g.now()}
	defer cancel()

	hello, err := g.read(c, 15*time.Second)
	if err != nil || hello.Type != agentproto.TypeHello {
		_ = ws.Close(websocket.StatusPolicyViolation, "say hello first")
		return
	}
	addr := r.RemoteAddr // the RealIP middleware has already applied trusted proxies
	if err := g.svc.TouchAgent(ctx, agent.ID, addr, hello.Version); err != nil {
		g.log.Error("agent touch", "agent", agent.Name, "err", err)
	}
	agent.LastAddr, agent.Version = addr, hello.Version
	if len(agent.Labels) == 0 && len(hello.Labels) > 0 {
		// First contact: the labels on the command line become the
		// agent's; from then on the server's labels are the truth and
		// an admin edits them in the org settings.
		if updated, err := g.svc.AdoptAgentLabels(ctx, agent.OrgID, agent.ID, hello.Labels); err == nil {
			agent.Labels = updated.Labels
		}
	}
	g.register(c)
	g.log.Info("agent connected", "agent", agent.Name, "addr", addr, "version", hello.Version, "checks", strings.Join(hello.Checks, ","))
	g.kick()

	for {
		msg, err := g.read(c, g.ReadTimeout)
		if err != nil {
			break
		}
		switch msg.Type {
		case agentproto.TypeHeartbeat:
			if err := g.svc.TouchAgent(ctx, agent.ID, addr, agent.Version); err != nil {
				g.log.Error("agent touch", "agent", agent.Name, "err", err)
			}
		case agentproto.TypeResult:
			err := g.svc.RecordAgentResult(ctx, agent, service.AgentResult{
				MonitorID: msg.MonitorID, At: msg.AttemptAt, OK: msg.OK, Warn: msg.Warn, LatencyMs: msg.LatencyMs, Reason: msg.Reason, Detail: msg.Detail,
			})
			if err != nil {
				g.log.Error("agent result", "agent", agent.Name, "monitor_id", msg.MonitorID, "err", err)
			}
			if err := g.svc.TouchAgent(ctx, agent.ID, addr, agent.Version); err != nil {
				g.log.Error("agent touch", "agent", agent.Name, "err", err)
			}
		case agentproto.TypeHello:
			// A repeated hello is harmless.
		default:
			g.log.Warn("agent sent an unknown message", "agent", agent.Name, "type", msg.Type)
		}
	}
	if g.unregister(c) {
		// Its monitors stay with it until another agent can take them;
		// the reconcile moves what it can and the sweep marks the rest.
		g.log.Info("agent disconnected", "agent", agent.Name)
		g.kick()
	}
	_ = ws.Close(websocket.StatusNormalClosure, "bye")
}

func (g *Gateway) read(c *conn, timeout time.Duration) (agentproto.Message, error) {
	ctx, cancel := context.WithTimeout(c.ctx, timeout)
	defer cancel()
	var msg agentproto.Message
	_, data, err := c.ws.Read(ctx)
	if err != nil {
		return msg, err
	}
	if err := json.Unmarshal(data, &msg); err != nil {
		return msg, err
	}
	return msg, nil
}

func (c *conn) write(msg agentproto.Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	ctx, cancel := context.WithTimeout(c.ctx, 10*time.Second)
	defer cancel()
	return c.ws.Write(ctx, websocket.MessageText, data)
}

// register stores the connection; an agent that connects twice keeps the
// newer socket and the older one is closed.
func (g *Gateway) register(c *conn) {
	g.mu.Lock()
	old := g.conns[c.agent.ID]
	g.conns[c.agent.ID] = c
	g.mu.Unlock()
	if old != nil {
		// The close handshake first, so the peer learns why; cancelling
		// first would tear the socket down without a frame.
		g.log.Info("agent reconnected, closing the older socket", "agent", c.agent.Name)
		_ = old.ws.Close(websocket.StatusPolicyViolation, "replaced by a newer connection")
		old.cancel()
	}
}

// unregister drops the connection if it is still the live one.
func (g *Gateway) unregister(c *conn) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.conns[c.agent.ID] != c {
		return false
	}
	delete(g.conns, c.agent.ID)
	return true
}

// kick asks the loop to reconcile soon.
func (g *Gateway) kick() {
	select {
	case g.wake <- struct{}{}:
	default:
	}
}

func (g *Gateway) snapshot() []*conn {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]*conn, 0, len(g.conns))
	for _, c := range g.conns {
		out = append(out, c)
	}
	return out
}

// Reconcile assigns the remote monitors of every org with a connected
// agent and sends each agent the difference from what it holds.
func (g *Gateway) Reconcile(ctx context.Context) error {
	conns := g.snapshot()
	orgs := map[string]bool{}
	for _, c := range conns {
		orgs[c.agent.OrgID] = true
	}
	for org := range orgs {
		if _, err := g.svc.AssignAgents(ctx, org); err != nil {
			return err
		}
	}
	for _, c := range conns {
		if err := g.sync(ctx, c); err != nil && ctx.Err() == nil {
			g.log.Warn("agent sync", "agent", c.agent.Name, "err", err)
		}
	}
	return nil
}

// sync sends assign for monitors the agent should run but has not been
// told about (or whose spec changed) and revoke for the rest.
func (g *Gateway) sync(ctx context.Context, c *conn) error {
	monitors, err := g.svc.AgentMonitors(ctx, c.agent.ID)
	if err != nil {
		return err
	}
	want := map[string][]byte{}
	for _, m := range monitors {
		if m.Paused || m.Pull == nil || !m.Pull.Remote() {
			continue
		}
		spec, err := json.Marshal(m.Pull)
		if err != nil {
			return err
		}
		want[m.ID] = spec
	}
	for id := range c.sent {
		if _, ok := want[id]; !ok {
			if err := c.write(agentproto.Message{Type: agentproto.TypeRevoke, MonitorID: id}); err != nil {
				return err
			}
			delete(c.sent, id)
		}
	}
	for _, m := range monitors {
		spec, ok := want[m.ID]
		if !ok {
			continue
		}
		version := agentproto.SpecVersion(spec)
		if c.sent[m.ID] == version {
			continue
		}
		msg := agentproto.Message{Type: agentproto.TypeAssign, MonitorID: m.ID, Kind: string(m.Kind), Spec: spec, Interval: m.Pull.Interval.String(), Version: version}
		if err := c.write(msg); err != nil {
			return err
		}
		c.sent[m.ID] = version
	}
	return nil
}

// Run reconciles on every bus event, after connects and disconnects and
// at least every sweep interval, when it also looks for quiet agents.
func (g *Gateway) Run(ctx context.Context) error {
	events, cancel := g.svc.Bus().Subscribe()
	defer cancel()
	ticker := time.NewTicker(g.Sweep)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			g.closeAll()
			return ctx.Err()
		case <-events:
			for len(events) > 0 {
				<-events
			}
		case <-g.wake:
		case <-ticker.C:
			if n, err := g.svc.SweepOfflineAgents(ctx, g.now(), g.OfflineAfter); err != nil && ctx.Err() == nil {
				g.log.Error("agent sweep", "err", err)
			} else if n > 0 {
				g.log.Info("agent sweep", "late", n)
			}
		}
		if ctx.Err() != nil {
			g.closeAll()
			return ctx.Err()
		}
		if err := g.Reconcile(ctx); err != nil && ctx.Err() == nil {
			g.log.Error("agent reconcile", "err", err)
		}
	}
}

func (g *Gateway) closeAll() {
	for _, c := range g.snapshot() {
		c.cancel()
		_ = c.ws.Close(websocket.StatusGoingAway, "server stopping")
	}
}
