package service

import (
	"context"

	"github.com/w4jnl/vink/internal/metrics"
)

// SetMetrics installs the instruments and this service as their source.
func (s *Service) SetMetrics(m *metrics.Metrics) {
	s.metrics = m
	m.SetSource(s)
}

// MetricMonitors feeds the vink_monitors gauge; a paused monitor counts as paused.
func (s *Service) MetricMonitors(ctx context.Context) ([]metrics.MonitorCount, error) {
	rows, err := s.db.Read().ListMonitorsForMetrics(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]metrics.MonitorCount, 0, len(rows))
	for _, r := range rows {
		state := r.State
		if r.Paused {
			state = "paused"
		}
		out = append(out, metrics.MonitorCount{Org: r.OrgSlug, Project: r.ProjectSlug, Kind: r.Kind, State: state, N: int(r.N)})
	}
	return out, nil
}

// MetricOpenIncidents feeds vink_incidents_open.
func (s *Service) MetricOpenIncidents(ctx context.Context) ([]metrics.IncidentCount, error) {
	rows, err := s.db.Read().CountOpenIncidentsByProject(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]metrics.IncidentCount, 0, len(rows))
	for _, r := range rows {
		out = append(out, metrics.IncidentCount{Org: r.OrgSlug, Project: r.ProjectSlug, N: int(r.N)})
	}
	return out, nil
}

// MetricPendingDeliveries feeds vink_deliveries_pending.
func (s *Service) MetricPendingDeliveries(ctx context.Context) (int, error) {
	n, err := s.db.Read().CountPendingDeliveries(ctx)
	return int(n), err
}
