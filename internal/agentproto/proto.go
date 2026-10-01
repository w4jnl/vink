// Package agentproto is the wire format between vink and its probe
// agents: JSON messages over one WebSocket, as the design document lays
// them out. The gateway (internal/http/agentgw) and the agent
// (internal/agent) both speak it.
package agentproto

import (
	"encoding/json"
	"hash/fnv"
	"strconv"
	"time"
)

// Path is where the gateway listens; the agent dials wss://host/agent/v1.
const Path = "/agent/v1"

// HeartbeatEvery is how often an idle agent says it is alive.
const HeartbeatEvery = 30 * time.Second

// Message types.
const (
	TypeHello     = "hello"     // agent → server, first message
	TypeHeartbeat = "heartbeat" // agent → server
	TypeResult    = "result"    // agent → server
	TypeAssign    = "assign"    // server → agent
	TypeRevoke    = "revoke"    // server → agent
)

// Message is one frame; the fields in use depend on Type.
type Message struct {
	Type string `json:"type"`

	// Version is the agent's build in a hello and the spec's version (a
	// hash of the spec) in an assign.
	Version string `json:"version,omitempty"`

	// hello
	Labels map[string]string `json:"labels,omitempty"`
	Checks []string          `json:"checks_supported,omitempty"`

	// assign and revoke
	MonitorID string          `json:"monitor_id,omitempty"`
	Kind      string          `json:"kind,omitempty"`
	Spec      json.RawMessage `json:"spec,omitempty"`
	Interval  string          `json:"interval,omitempty"`

	// result
	AttemptAt time.Time      `json:"attempt_at,omitzero"`
	OK        bool           `json:"ok,omitempty"`
	Warn      bool           `json:"warn,omitempty"`
	LatencyMs int64          `json:"latency_ms,omitempty"`
	Reason    string         `json:"reason,omitempty"`
	Detail    map[string]any `json:"detail,omitempty"`
}

// SpecVersion is the version an assign carries: a hash of the spec, so a
// state change on the monitor does not look like an edit.
func SpecVersion(spec []byte) string {
	h := fnv.New64a()
	_, _ = h.Write(spec)
	return strconv.FormatUint(h.Sum64(), 36)
}
