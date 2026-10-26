package pihole

import (
	"context"
	"testing"

	"github.com/timgladwell/pihole-exporter/internal/piholetest"
)

func TestCollectMetricsFetchesStatsSummary(t *testing.T) {
	t.Parallel()

	mock := piholetest.New(t)
	login := mock.ExpectLogin("app-password")
	mock.ExpectScrape(login, "sid-1", "csrf-1", 1)
	client, err := NewAuthClient(piholetest.BaseURL, "app-password", WithHTTPClient(mock.Client()))
	if err != nil {
		t.Fatalf("NewAuthClient() error = %v", err)
	}

	metrics, err := client.CollectMetrics(context.Background())
	if err != nil {
		t.Fatalf("CollectMetrics() error = %v", err)
	}

	// Expected values are read off internal/piholetest/testdata/ftl-v6.7.1.
	assertMetric(t, metrics, "pihole_summary_queries_total", nil, 7497)
	assertMetric(t, metrics, "pihole_summary_queries_percent_blocked", nil, 46.218487)
	assertMetric(t, metrics, "pihole_summary_clients_active", nil, 12)
	assertMetric(t, metrics, "pihole_summary_gravity_last_update_timestamp_seconds", nil, 1727800000)
	assertMetric(t, metrics, "pihole_summary_queries_frequency", nil, 0)
	assertMetric(t, metrics, "pihole_summary_queries_by_type", map[string]string{"type": "A"}, 4101)
	assertMetric(t, metrics, "pihole_summary_queries_by_status", map[string]string{"status": "GRAVITY"}, 3465)
	assertMetric(t, metrics, "pihole_summary_queries_by_reply", map[string]string{"reply": "IP"}, 3912)
	assertMetric(t, metrics, "pihole_summary_queries_by_reply", map[string]string{"reply": "NODATA"}, 0)
	assertMetric(t, metrics, "pihole_upstreams_forwarded_queries", nil, 4006)
	assertMetric(t, metrics, "pihole_query_types_by_type", map[string]string{"type": "AAAA"}, 2213)
}

func TestCompiledMetricSpecsAreCopied(t *testing.T) {
	specs := CompiledMetricSpecs()
	if len(specs) == 0 {
		t.Fatal("CompiledMetricSpecs() returned no specs")
	}

	specs[0].Name = "mutated"
	if CompiledMetricSpecs()[0].Name == "mutated" {
		t.Fatal("CompiledMetricSpecs() returned mutable backing storage")
	}
}

func assertMetric(t *testing.T, metrics []Metric, name string, labels map[string]string, want float64) {
	t.Helper()

	for _, metric := range metrics {
		if metric.Name != name || labelString(metric.Labels) != labelString(labels) {
			continue
		}
		if metric.Value != want {
			t.Fatalf("%s{%s} = %v, want %v", name, labelString(labels), metric.Value, want)
		}
		return
	}

	t.Fatalf("metric %s{%s} not found in %#v", name, labelString(labels), metrics)
}
