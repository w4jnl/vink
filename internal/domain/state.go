package domain

// State is a monitor's state. Every state has a glyph, a colour and a word
// in the UI; here it is the word.
type State string

const (
	StateNew    State = "new"
	StateUp     State = "up"
	StateLate   State = "late"
	StateDown   State = "down"
	StatePaused State = "paused"
)

// States lists every state in display order.
var States = []State{StateUp, StateLate, StateDown, StatePaused, StateNew}

// Valid reports whether s is a known state.
func (s State) Valid() bool {
	switch s {
	case StateNew, StateUp, StateLate, StateDown, StatePaused:
		return true
	}
	return false
}

// SortRank orders lists: down, late, then the rest by name.
func (s State) SortRank() int {
	switch s {
	case StateDown:
		return 0
	case StateLate:
		return 1
	default:
		return 2
	}
}

// Kind is a monitor kind. Phase 0 implements heartbeat.
type Kind string

const (
	KindHeartbeat Kind = "heartbeat"
	KindHTTP      Kind = "http"
	KindTCP       Kind = "tcp"
	KindDNS       Kind = "dns"
	KindTLS       Kind = "tls"
	KindICMP      Kind = "icmp"
)

// Valid reports whether k is a known kind.
func (k Kind) Valid() bool {
	switch k {
	case KindHeartbeat, KindHTTP, KindTCP, KindDNS, KindTLS, KindICMP:
		return true
	}
	return false
}

// Signal is what a ping or check reports.
type Signal string

const (
	SignalStart Signal = "start"
	SignalOK    Signal = "ok"
	SignalFail  Signal = "fail"
	SignalLog   Signal = "log"
	SignalExit  Signal = "exit"
)

// Valid reports whether s is a known signal.
func (s Signal) Valid() bool {
	switch s {
	case SignalStart, SignalOK, SignalFail, SignalLog, SignalExit:
		return true
	}
	return false
}
