// Package metrics is the Prometheus endpoint: counters the service
// increments as it works, and gauges read from the database on scrape.
package metrics

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// MonitorCount is one row of the monitors gauge.
type MonitorCount struct {
	Org, Project, Kind, State string
	N                         int
}

// IncidentCount is one row of the open incidents gauge.
type IncidentCount struct {
	Org, Project string
	N            int
}

// Source reads the gauges on scrape.
type Source interface {
	MetricMonitors(ctx context.Context) ([]MonitorCount, error)
	MetricOpenIncidents(ctx context.Context) ([]IncidentCount, error)
	MetricPendingDeliveries(ctx context.Context) (int, error)
}

// Metrics holds the registry and the instruments.
type Metrics struct {
	reg          *prometheus.Registry
	Pings        *prometheus.CounterVec
	Checks       *prometheus.CounterVec
	CheckLatency *prometheus.HistogramVec
	Deliveries   *prometheus.CounterVec
	SchedulerLag prometheus.Gauge
	PoolLag      prometheus.Gauge
	monitors     *prometheus.Desc
	incidents    *prometheus.Desc
	pending      *prometheus.Desc
	source       Source
}

// New builds the instruments on a fresh registry.
func New(version string) *Metrics {
	m := &Metrics{
		reg:          prometheus.NewRegistry(),
		Pings:        prometheus.NewCounterVec(prometheus.CounterOpts{Name: "vink_pings_total", Help: "Pings received, by project and result (ok, fail, start, log, rejected)."}, []string{"project", "result"}),
		Checks:       prometheus.NewCounterVec(prometheus.CounterOpts{Name: "vink_checks_total", Help: "Pull check attempts, by project, kind and outcome."}, []string{"project", "kind", "ok"}),
		CheckLatency: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "vink_check_latency_seconds", Help: "Latency of pull check attempts.", Buckets: []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}}, []string{"kind"}),
		Deliveries:   prometheus.NewCounterVec(prometheus.CounterOpts{Name: "vink_deliveries_total", Help: "Notification deliveries attempted, by channel kind and result (ok, error)."}, []string{"kind", "result"}),
		SchedulerLag: prometheus.NewGauge(prometheus.GaugeOpts{Name: "vink_scheduler_lag_seconds", Help: "How far behind the heartbeat scheduler ran on its last pass."}),
		PoolLag:      prometheus.NewGauge(prometheus.GaugeOpts{Name: "vink_checks_lag_seconds", Help: "How far behind the checker pool ran on its last pass."}),
		monitors:     prometheus.NewDesc("vink_monitors", "Monitors by org, project, kind and state.", []string{"org", "project", "kind", "state"}, nil),
		incidents:    prometheus.NewDesc("vink_incidents_open", "Open incidents by org and project.", []string{"org", "project"}, nil),
		pending:      prometheus.NewDesc("vink_deliveries_pending", "Deliveries waiting in the outbox.", nil, nil),
	}
	info := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "vink_build_info", Help: "Build information."}, []string{"version"})
	info.WithLabelValues(version).Set(1)
	m.reg.MustRegister(m.Pings, m.Checks, m.CheckLatency, m.Deliveries, m.SchedulerLag, m.PoolLag, info)
	m.reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return m
}

// SetSource installs the database reader for the gauges.
func (m *Metrics) SetSource(s Source) {
	m.source = s
	m.reg.MustRegister(m)
}

// Describe implements prometheus.Collector.
func (m *Metrics) Describe(ch chan<- *prometheus.Desc) {
	ch <- m.monitors
	ch <- m.incidents
	ch <- m.pending
}

// Collect implements prometheus.Collector: one query per gauge on scrape.
func (m *Metrics) Collect(ch chan<- prometheus.Metric) {
	if m.source == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if rows, err := m.source.MetricMonitors(ctx); err == nil {
		for _, r := range rows {
			ch <- prometheus.MustNewConstMetric(m.monitors, prometheus.GaugeValue, float64(r.N), r.Org, r.Project, r.Kind, r.State)
		}
	}
	if rows, err := m.source.MetricOpenIncidents(ctx); err == nil {
		for _, r := range rows {
			ch <- prometheus.MustNewConstMetric(m.incidents, prometheus.GaugeValue, float64(r.N), r.Org, r.Project)
		}
	}
	if n, err := m.source.MetricPendingDeliveries(ctx); err == nil {
		ch <- prometheus.MustNewConstMetric(m.pending, prometheus.GaugeValue, float64(n))
	}
}

// Handler serves the registry; with a token, only "Authorization: Bearer
// <token>" may scrape.
func (m *Metrics) Handler(token string) http.Handler {
	h := promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token != "" {
			got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
				w.Header().Set("WWW-Authenticate", `Bearer realm="vink metrics"`)
				http.Error(w, "metrics token required", http.StatusUnauthorized)
				return
			}
		}
		w.Header().Set("Cache-Control", "no-store")
		h.ServeHTTP(w, r)
	})
}

// LagObserver returns a function the scheduler calls with its lag.
func (m *Metrics) LagObserver(g prometheus.Gauge) func(time.Duration) {
	return func(d time.Duration) { g.Set(d.Seconds()) }
}
