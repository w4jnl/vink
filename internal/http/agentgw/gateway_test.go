package agentgw

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/w4jnl/vink/internal/agentproto"
	"github.com/w4jnl/vink/internal/db/dbtest"
	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/service"
)

type env struct {
	svc    *service.Service
	gw     *Gateway
	srv    *httptest.Server
	member domain.Scope
	admin  domain.Scope
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := service.New(dbtest.Open(t), nil, quiet, service.DefaultConfig())
	root := domain.Scope{InstanceAdmin: true, Role: domain.RoleOwner, Actor: "test"}
	org, err := svc.CreateOrg(ctx, root, "homelab", "Homelab")
	if err != nil {
		t.Fatal(err)
	}
	project, err := svc.CreateProject(ctx, root, org.ID, "prod", "Prod", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	gw := New(svc, quiet)
	gw.Sweep = 50 * time.Millisecond
	mux := http.NewServeMux()
	gw.Mount(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go func() { _ = gw.Run(ctx) }()
	return &env{
		svc: svc, gw: gw, srv: srv,
		member: domain.Scope{OrgID: org.ID, ProjectID: project.ID, UserID: "u1", Role: domain.RoleMember, Actor: "user:j"},
		admin:  domain.Scope{OrgID: org.ID, UserID: "u2", Role: domain.RoleAdmin, Actor: "user:a"},
	}
}

// dial connects as an agent; on failure it returns the handshake status.
func (e *env) dial(t *testing.T, token string) (*websocket.Conn, int) {
	t.Helper()
	url := "ws" + strings.TrimPrefix(e.srv.URL, "http") + agentproto.Path
	h := http.Header{}
	if token != "" {
		h.Set("Authorization", "Bearer "+token)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: h})
	status := 0
	if resp != nil {
		status = resp.StatusCode
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
	}
	if err != nil {
		return nil, status
	}
	return c, status
}

func send(t *testing.T, c *websocket.Conn, msg agentproto.Message) {
	t.Helper()
	data, _ := json.Marshal(msg)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Write(ctx, websocket.MessageText, data); err != nil {
		t.Fatalf("write %s: %v", msg.Type, err)
	}
}

func recv(t *testing.T, c *websocket.Conn) agentproto.Message {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, data, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var msg agentproto.Message
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatal(err)
	}
	return msg
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestGatewayRoundTrip(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	agent, token, err := e.svc.CreateAgent(ctx, e.admin, "dc1", nil)
	if err != nil {
		t.Fatal(err)
	}
	m, err := e.svc.CreateMonitor(ctx, e.member, &domain.Monitor{Slug: "web", Kind: domain.KindHTTP, Pull: &domain.PullSpec{Location: "site=dc1", HTTP: &domain.HTTPCheck{URL: "https://web.internal"}}})
	if err != nil {
		t.Fatal(err)
	}

	// no token, a bad token
	if c, status := e.dial(t, ""); c != nil || status != 401 {
		t.Fatalf("no token: %d", status)
	}
	if c, status := e.dial(t, "vat_nope"); c != nil || status != 401 {
		t.Fatalf("bad token: %d", status)
	}

	c, _ := e.dial(t, token)
	if c == nil {
		t.Fatal("dial failed")
	}
	defer func() { _ = c.CloseNow() }()
	// labels from the command line are adopted on first contact
	send(t, c, agentproto.Message{Type: agentproto.TypeHello, Version: "0.2.0", Labels: map[string]string{"site": "dc1"}, Checks: []string{"http", "tcp"}})
	waitFor(t, "connected", func() bool { return e.gw.Connected(agent.ID) })
	got, err := e.svc.Agent(ctx, e.admin, "dc1")
	if err != nil || got.Labels["site"] != "dc1" || got.Version != "0.2.0" || got.LastSeenAt == nil || got.State(true) != domain.AgentConnected {
		t.Fatalf("after hello: %+v %v", got, err)
	}

	// the assignment arrives with the spec
	msg := recv(t, c)
	if msg.Type != agentproto.TypeAssign || msg.MonitorID != m.ID || msg.Kind != "http" || msg.Interval != "1m" || msg.Version == "" || !strings.Contains(string(msg.Spec), "web.internal") {
		t.Fatalf("assign: %+v", msg)
	}
	var spec domain.PullSpec
	if err := json.Unmarshal(msg.Spec, &spec); err != nil || spec.HTTP == nil {
		t.Fatalf("spec: %v", err)
	}

	// a result turns the monitor up
	send(t, c, agentproto.Message{Type: agentproto.TypeResult, MonitorID: m.ID, AttemptAt: time.Now().UTC().Truncate(time.Millisecond), OK: true, LatencyMs: 40, Detail: map[string]any{"status": 200}})
	waitFor(t, "up", func() bool {
		cur, _ := e.svc.MonitorBySlug(ctx, e.member, "web")
		return cur != nil && cur.State == domain.StateUp
	})

	// editing the spec re-sends it with a newer version
	if _, err := e.svc.UpdateMonitor(ctx, e.member, "web", &domain.Monitor{Name: "Web", Pull: &domain.PullSpec{Location: "site=dc1", HTTP: &domain.HTTPCheck{URL: "https://web2.internal"}}}); err != nil {
		t.Fatal(err)
	}
	first := msg.Version
	msg = recv(t, c)
	if msg.Type != agentproto.TypeAssign || !strings.Contains(string(msg.Spec), "web2.internal") || msg.Version == first {
		t.Fatalf("reassign: %+v", msg)
	}

	// moving it to this server revokes it
	if _, err := e.svc.UpdateMonitor(ctx, e.member, "web", &domain.Monitor{Name: "Web", Pull: &domain.PullSpec{HTTP: &domain.HTTPCheck{URL: "https://web2.internal"}}}); err != nil {
		t.Fatal(err)
	}
	msg = recv(t, c)
	if msg.Type != agentproto.TypeRevoke || msg.MonitorID != m.ID {
		t.Fatalf("revoke: %+v", msg)
	}
	send(t, c, agentproto.Message{Type: agentproto.TypeHeartbeat})

	// a second socket for the same agent replaces the first
	c2, _ := e.dial(t, token)
	if c2 == nil {
		t.Fatal("second dial failed")
	}
	defer func() { _ = c2.CloseNow() }()
	send(t, c2, agentproto.Message{Type: agentproto.TypeHello, Version: "0.2.1"})
	rctx, rcancel := context.WithTimeout(ctx, 5*time.Second)
	defer rcancel()
	if _, _, err := c.Read(rctx); err == nil || !strings.Contains(err.Error(), "replaced") {
		t.Fatalf("first socket must close: %v", err)
	}
	waitFor(t, "still connected", func() bool { return e.gw.Connected(agent.ID) && e.gw.ConnectedCount() == 1 })

	// closing the live socket releases the agent
	_ = c2.Close(websocket.StatusNormalClosure, "bye")
	waitFor(t, "disconnected", func() bool { return !e.gw.Connected(agent.ID) })
	if got, _ := e.svc.Agent(ctx, e.admin, "dc1"); got.State(e.gw.Connected(agent.ID)) != domain.AgentOffline {
		t.Fatalf("after close: %+v", got)
	}
}

func TestGatewayRejectsWithoutHello(t *testing.T) {
	e := newEnv(t)
	_, token, err := e.svc.CreateAgent(context.Background(), e.admin, "dc1", nil)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := e.dial(t, token)
	if c == nil {
		t.Fatal("dial failed")
	}
	defer func() { _ = c.CloseNow() }()
	send(t, c, agentproto.Message{Type: agentproto.TypeHeartbeat})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, _, err := c.Read(ctx); err == nil || !strings.Contains(err.Error(), "hello") {
		t.Fatalf("want a policy close, got %v", err)
	}
}
