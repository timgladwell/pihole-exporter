package exporter

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/timgladwell/pihole-exporter/internal/piholetest"
	"github.com/timgladwell/pihole-exporter/pkg/pihole"

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

func TestCollectorNotReadyAfterFailedScrape(t *testing.T) {
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer broken.Close()

	client, err := pihole.NewAuthClient(broken.URL, "app-password")
	if err != nil {
		t.Fatalf("NewAuthClient() error = %v", err)
	}

	collector := NewCollector(client, 5*time.Second)
	if collector.Ready() {
		t.Fatal("Ready() = true before any scrape, want false")
	}

	registry := prometheus.NewRegistry()
	registry.MustRegister(collector)
	if _, err := registry.Gather(); err == nil {
		t.Fatal("Gather() error = nil, want scrape error")
	}

	if collector.Ready() {
		t.Fatal("Ready() = true after failed scrape, want false")
	}
}
