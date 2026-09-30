package metrics

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeSource struct{}

func (fakeSource) MetricMonitors(context.Context) ([]MonitorCount, error) {
	return []MonitorCount{{Org: "homelab", Project: "prod", Kind: "http", State: "up", N: 3}, {Org: "homelab", Project: "prod", Kind: "heartbeat", State: "down", N: 1}}, nil
}
func (fakeSource) MetricOpenIncidents(context.Context) ([]IncidentCount, error) {
	return []IncidentCount{{Org: "homelab", Project: "prod", N: 1}}, nil
}
func (fakeSource) MetricPendingDeliveries(context.Context) (int, error) { return 2, nil }

func TestHandlerAndGauges(t *testing.T) {
	m := New("test")
	m.SetSource(fakeSource{})
	m.Pings.WithLabelValues("prod", "ok").Inc()
	m.Checks.WithLabelValues("prod", "http", "true").Add(2)
	m.CheckLatency.WithLabelValues("http").Observe(0.084)
	m.Deliveries.WithLabelValues("ntfy", "ok").Inc()
	m.LagObserver(m.SchedulerLag)(1500 * time.Millisecond)
	srv := httptest.NewServer(m.Handler("s3cret"))
	defer srv.Close()
	get := func(url, token string) (int, string) {
		req, _ := http.NewRequest("GET", url, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, _ := get(srv.URL, ""); code != 401 {
		t.Fatalf("without token: %d", code)
	}
	code, body := get(srv.URL, "s3cret")
	if code != 200 {
		t.Fatalf("with token: %d", code)
	}
	for _, want := range []string{
		`vink_monitors{kind="http",org="homelab",project="prod",state="up"} 3`,
		`vink_monitors{kind="heartbeat",org="homelab",project="prod",state="down"} 1`,
		`vink_incidents_open{org="homelab",project="prod"} 1`,
		`vink_deliveries_pending 2`,
		`vink_pings_total{project="prod",result="ok"} 1`,
		`vink_checks_total{kind="http",ok="true",project="prod"} 2`,
		`vink_check_latency_seconds_bucket{kind="http",le="0.1"} 1`,
		`vink_deliveries_total{kind="ntfy",result="ok"} 1`,
		`vink_scheduler_lag_seconds 1.5`,
		`vink_build_info{version="test"} 1`,
		`go_goroutines`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics lack %q", want)
		}
	}
	open := httptest.NewServer(New("x").Handler(""))
	defer open.Close()
	if code, _ := get(open.URL, ""); code != 200 {
		t.Fatalf("no token configured: %d", code)
	}
}
