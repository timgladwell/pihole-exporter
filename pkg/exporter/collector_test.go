package exporter

import (
	"testing"
	"time"

	"github.com/alantoch/pihole-exporter/internal/piholetest"
	"github.com/alantoch/pihole-exporter/pkg/pihole"
	"github.com/prometheus/client_golang/prometheus"

	dto "github.com/prometheus/client_model/go"
)

func TestCollectorEmitsScrapeHealthMetrics(t *testing.T) {
	stubServer := piholetest.StartStubPiholeServer(t)

	client, err := pihole.NewAuthClient(stubServer.URL, "app-password")
	if err != nil {
		t.Fatalf("NewAuthClient() error = %v", err)
	}

	registry := prometheus.NewRegistry()
	registry.MustRegister(NewCollector(client, 5*time.Second))

	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("Gather() error = %v", err)
	}

	if got := metricValue(t, families, "pihole_exporter_scrape_success"); got != 1 {
		t.Fatalf("pihole_exporter_scrape_success = %v, want 1", got)
	}
	if got := metricValue(t, families, "pihole_exporter_scrape_duration_seconds"); got < 0 {
		t.Fatalf("pihole_exporter_scrape_duration_seconds = %v, want >= 0", got)
	}
}

func metricValue(t *testing.T, families []*dto.MetricFamily, name string) float64 {
	t.Helper()

	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		metrics := family.GetMetric()
		if len(metrics) != 1 {
			t.Fatalf("%s has %d metrics, want 1", name, len(metrics))
		}
		return metrics[0].GetGauge().GetValue()
	}

	t.Fatalf("metric %s not found", name)
	return 0
}
