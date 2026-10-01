package service

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/w4jnl/vink/internal/db"
	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/engine"
)

// Reasons the sweep writes on a remote monitor that nobody runs.
const (
	ReasonAgentOffline = "agent offline"
	ReasonNoAgent      = "no matching agent"
)

// AgentResult is one finished attempt sequence an agent reports.
type AgentResult struct {
	MonitorID string
	At        time.Time
	OK        bool
	Warn      bool
	LatencyMs int64
	Reason    string
	Detail    map[string]any
}

// Assignment is one change AssignAgents made.
type Assignment struct {
	MonitorID string
	From, To  string // agent ids; "" is unassigned
}

// checkLocation refuses a location that names an agent the org does not
// have; a selector may match agents that arrive later.
func (s *Service) checkLocation(ctx context.Context, orgID string, m *domain.Monitor) error {
	if m.Pull == nil {
		return nil
	}
	loc := m.Pull.ParsedLocation()
	if loc.Agent == "" {
		return nil
	}
	if _, err := s.db.Read().GetAgentByName(ctx, db.GetAgentByNameParams{OrgID: orgID, Name: loc.Agent}); err != nil {
		if db.IsNotFound(err) {
			return (&domain.ValidationError{Errors: []domain.FieldError{{Field: "location", Msg: "no agent named " + loc.Agent + " in this org"}}}).OrNil()
		}
		return err
	}
	return nil
}

// AssignAgents gives every remote monitor of the org to a connected agent
// that matches its location: the one it already has when that agent is
// still connected and still matches (sticky), otherwise the least loaded
// candidate, or nobody. It returns what changed.
func (s *Service) AssignAgents(ctx context.Context, orgID string) ([]Assignment, error) {
	rows, err := s.db.Read().ListAgents(ctx, orgID)
	if err != nil {
		return nil, err
	}
	connected := map[string]*domain.Agent{}
	for _, r := range rows {
		if s.AgentConnected(r.ID) {
			connected[r.ID] = agentFromRow(r)
		}
	}
	mrows, err := s.db.Read().ListRemoteMonitors(ctx, orgID)
	if err != nil {
		return nil, err
	}
	monitors := make([]*domain.Monitor, 0, len(mrows))
	load := map[string]int{}
	for _, r := range mrows {
		m, err := monitorFromRow(r)
		if err != nil {
			return nil, err
		}
		monitors = append(monitors, m)
		if _, ok := connected[m.AgentID]; ok {
			load[m.AgentID]++
		}
	}
	matches := func(a *domain.Agent, loc domain.Location) bool {
		if loc.Agent != "" {
			return a.Name == loc.Agent
		}
		return a.Matches(loc.Labels)
	}
	var changes []Assignment
	for _, m := range monitors {
		loc := m.Pull.ParsedLocation()
		if cur, ok := connected[m.AgentID]; ok && matches(cur, loc) && !m.Paused {
			continue
		}
		want := ""
		if !m.Paused {
			var best *domain.Agent
			for _, a := range connected {
				if !matches(a, loc) {
					continue
				}
				if best == nil || load[a.ID] < load[best.ID] || (load[a.ID] == load[best.ID] && a.Name < best.Name) {
					best = a
				}
			}
			if best != nil {
				want = best.ID
			}
		}
		if want == m.AgentID {
			continue
		}
		if err := s.db.Write().SetMonitorAgent(ctx, db.SetMonitorAgentParams{AgentID: ptrs(want), ID: m.ID}); err != nil {
			return nil, err
		}
		if m.AgentID != "" {
			load[m.AgentID]--
		}
		if want != "" {
			load[want]++
		}
		changes = append(changes, Assignment{MonitorID: m.ID, From: m.AgentID, To: want})
		s.log.Info("monitor assigned", "project_id", m.ProjectID, "monitor", m.Slug, "from", m.AgentID, "to", want)
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].MonitorID < changes[j].MonitorID })
	return changes, nil
}

// AgentMonitors lists the monitors assigned to an agent, for the gateway
// to hand over.
func (s *Service) AgentMonitors(ctx context.Context, agentID string) ([]*domain.Monitor, error) {
	rows, err := s.db.Read().ListAgentMonitors(ctx, ptrs(agentID))
	if err != nil {
		return nil, err
	}
	out := make([]*domain.Monitor, 0, len(rows))
	for _, r := range rows {
		m, err := monitorFromRow(r)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

// AgentDisconnected releases the agent's monitors so another agent can
// take them; the gateway calls it when the socket closes.
func (s *Service) AgentDisconnected(ctx context.Context, agentID string) error {
	n, err := s.db.Write().ClearAgentMonitors(ctx, ptrs(agentID))
	if err != nil {
		return err
	}
	if n > 0 {
		s.log.Info("agent released monitors", "agent_id", agentID, "monitors", n)
	}
	return nil
}

// RecordAgentResult stores one result from an agent and runs the check
// state machine on it. A result for a monitor the agent no longer holds
// is dropped; one already stored for the same attempt time is ignored.
func (s *Service) RecordAgentResult(ctx context.Context, agent *domain.Agent, r AgentResult) error {
	if agent == nil || r.MonitorID == "" {
		return fmt.Errorf("agent result without an agent or monitor")
	}
	now := s.now()
	if r.At.IsZero() {
		r.At = now
	}
	source := "agent:" + agent.Name
	var (
		decision engine.Decision
		m        *domain.Monitor
	)
	err := s.db.Tx(ctx, func(q *db.Queries) error {
		row, err := q.GetMonitorByID(ctx, r.MonitorID)
		if err != nil {
			if db.IsNotFound(err) {
				return nil
			}
			return err
		}
		cur, err := monitorFromRow(row)
		if err != nil {
			return err
		}
		if cur.AgentID != agent.ID || cur.Paused || cur.Pull == nil {
			s.log.Debug("agent result dropped", "agent", agent.Name, "monitor", cur.Slug, "assigned_to", cur.AgentID)
			return nil
		}
		n, err := q.CountObservationsAt(ctx, db.CountObservationsAtParams{MonitorID: cur.ID, At: domain.Millis(r.At), Source: source})
		if err != nil {
			return err
		}
		if n > 0 {
			return nil
		}
		detail := make(map[string]any, len(r.Detail)+2)
		for k, v := range r.Detail {
			detail[k] = v
		}
		if !r.OK || r.Warn {
			detail["reason"] = r.Reason
		}
		if r.Warn {
			detail["warn"] = true
		}
		latency := r.LatencyMs
		obs := &domain.Observation{
			ID: domain.NewID(), MonitorID: cur.ID, ProjectID: cur.ProjectID, At: r.At, Source: source, Signal: domain.SignalOK, OK: r.OK, LatencyMs: &latency, Detail: detail, RemoteAddr: agent.LastAddr,
		}
		if !r.OK {
			obs.Signal = domain.SignalFail
		}
		if err := insertObservation(ctx, q, obs, nil, ""); err != nil {
			return err
		}
		decision, err = engine.ApplyCheck(cur, obs, r.OK && r.Warn, r.At)
		if err != nil {
			return err
		}
		m = cur
		return s.persistDecision(ctx, q, cur, obs, decision, now)
	})
	if err != nil || m == nil {
		return err
	}
	if s.metrics != nil {
		project := m.ProjectID
		if p, err := s.ProjectByID(ctx, m.ProjectID); err == nil {
			project = p.Slug
		}
		s.metrics.Checks.WithLabelValues(project, string(m.Kind), strconv.FormatBool(r.OK)).Inc()
		s.metrics.CheckLatency.WithLabelValues(string(m.Kind)).Observe(float64(r.LatencyMs) / 1000)
	}
	if decision.Changed {
		s.bus.Publish(engine.MonitorChanged{ProjectID: m.ProjectID, MonitorID: m.ID})
	}
	return nil
}

// SweepOfflineAgents turns the monitors of agents quiet for longer than
// offlineAfter late with reason agent offline, and remote monitors nobody
// has picked up for as long late with reason no matching agent. It
// returns how many monitors flipped.
func (s *Service) SweepOfflineAgents(ctx context.Context, now time.Time, offlineAfter time.Duration) (int, error) {
	if offlineAfter <= 0 {
		offlineAfter = 2 * time.Minute
	}
	cutoff := now.Add(-offlineAfter)
	flipped := 0
	agents, err := s.db.Read().ListAgentsSeenBefore(ctx, ptri(domain.Millis(cutoff)))
	if err != nil {
		return 0, err
	}
	for _, a := range agents {
		if s.AgentConnected(a.ID) {
			continue
		}
		monitors, err := s.AgentMonitors(ctx, a.ID)
		if err != nil {
			return flipped, err
		}
		for _, m := range monitors {
			n, err := s.markAgentOffline(ctx, m.ID, ReasonAgentOffline, now)
			if err != nil {
				return flipped, err
			}
			flipped += n
		}
	}
	rows, err := s.db.Read().ListUnassignedRemoteMonitors(ctx, domain.Millis(cutoff))
	if err != nil {
		return flipped, err
	}
	for _, r := range rows {
		n, err := s.markAgentOffline(ctx, r.ID, ReasonNoAgent, now)
		if err != nil {
			return flipped, err
		}
		flipped += n
	}
	return flipped, nil
}

func (s *Service) markAgentOffline(ctx context.Context, monitorID, reason string, now time.Time) (int, error) {
	var m *domain.Monitor
	var d engine.Decision
	err := s.db.Tx(ctx, func(q *db.Queries) error {
		row, err := q.GetMonitorByID(ctx, monitorID)
		if err != nil {
			if db.IsNotFound(err) {
				return nil
			}
			return err
		}
		cur, err := monitorFromRow(row)
		if err != nil {
			return err
		}
		d = engine.ApplyAgentOffline(cur, reason)
		if !d.Changed {
			return nil
		}
		m = cur
		return s.persistDecision(ctx, q, cur, nil, d, now)
	})
	if err != nil || m == nil {
		return 0, err
	}
	s.bus.Publish(engine.MonitorChanged{ProjectID: m.ProjectID, MonitorID: m.ID})
	return 1, nil
}
