package main

import (
	"bytes"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/timgladwell/pihole-exporter/internal/piholetest"
	"github.com/timgladwell/pihole-exporter/pkg/pihole"
)

func TestHealthzEndpoint(t *testing.T) {
	stubPiholeServer := piholetest.StartStubPiholeServer(t)

	t.Setenv("PIHOLE_BASE_URL", stubPiholeServer.URL)
	t.Setenv(pihole.DefaultAppPasswordEnv, "app-password")

	withTestCommandLine(t, "pihole-exporter")

	server, shutdown := buildServer()
	t.Cleanup(shutdown)
	testExporter := httptest.NewServer(server.Handler)
	defer testExporter.Close()

	resp, err := http.Get(testExporter.URL + "/healthz")
	if err != nil {
		t.Fatalf("Failed to send GET request: %v", err)
	}
	defer resp.Body.Close()

	expectedStatus := http.StatusOK
	if resp.StatusCode != expectedStatus {
		t.Errorf("Expected status %d, got %d", expectedStatus, resp.StatusCode)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("Failed to read response body: %v", err)
	}

	expectedBody := "ok\n"
	actualBody := string(bodyBytes)
	if actualBody != expectedBody {
		t.Errorf("Expected body %q, got %q", expectedBody, actualBody)
	}
}

func TestPrometheusMetricsEndpoint(t *testing.T) {
	stubPiholeServer := piholetest.StartStubPiholeServer(t)

	t.Setenv("PIHOLE_BASE_URL", stubPiholeServer.URL)
	t.Setenv(pihole.DefaultAppPasswordEnv, "app-password")

	withTestCommandLine(t, "pihole-exporter")

	server, shutdown := buildServer()
	t.Cleanup(shutdown)
	testExporter := httptest.NewServer(server.Handler)
	defer testExporter.Close()

	resp, err := http.Get(testExporter.URL + "/metrics")
	if err != nil {
		t.Fatalf("Failed to send GET request: %v", err)
	}
	defer resp.Body.Close()

	expectedStatus := http.StatusOK
	if resp.StatusCode != expectedStatus {
		t.Errorf("Expected status %d, got %d", expectedStatus, resp.StatusCode)
	}

	actualBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("Failed to read response body: %v", err)
	}

	filePath := filepath.Join("..", "..", "testdata", "metrics_prometheus_response_body.txt")
	expectedBytes, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("Failed to read test fixture file: %v", err)
	}

	if !bytes.Equal(stripDurationLine(actualBytes), stripDurationLine(expectedBytes)) {
		t.Errorf("metrics body mismatch\ngot:\n%s\nwant:\n%s", actualBytes, expectedBytes)
	}
}

// stripDurationLine removes the pihole_exporter_scrape_duration_seconds sample
// line before golden-file comparison. The duration value is a real wall-clock
// measurement and will differ on every run; the metric's presence is already
// verified by the # HELP and # TYPE lines that remain in the fixture.
func stripDurationLine(b []byte) []byte {
	const prefix = "pihole_exporter_scrape_duration_seconds "
	var out [][]byte
	for _, line := range bytes.Split(b, []byte("\n")) {
		if !bytes.HasPrefix(line, []byte(prefix)) {
			out = append(out, line)
		}
	}
	return bytes.Join(out, []byte("\n"))
}

func TestParseConfigUnsetsPasswordEnvByDefault(t *testing.T) {
	t.Setenv("PIHOLE_BASE_URL", "http://127.0.0.1:8080")
	t.Setenv(pihole.DefaultAppPasswordEnv, "app-password")

	withTestCommandLine(t, "pihole-exporter")

	cfg := parseConfig()

	if cfg.password != "app-password" {
		t.Fatalf("password = %q, want app-password", cfg.password)
	}
	if _, ok := os.LookupEnv(pihole.DefaultAppPasswordEnv); ok {
		t.Fatalf("%s is still set", pihole.DefaultAppPasswordEnv)
	}
}

func TestParseConfigDefaultsToPrometheusMetricsExporter(t *testing.T) {
	t.Setenv("PIHOLE_BASE_URL", "http://127.0.0.1:8080")
	t.Setenv(pihole.DefaultAppPasswordEnv, "app-password")

	withTestCommandLine(t, "pihole-exporter")

	cfg := parseConfig()

	if cfg.metricsExporter != metricsExporterPrometheus {
		t.Fatalf("metricsExporter = %q, want %q", cfg.metricsExporter, metricsExporterPrometheus)
	}
}

func TestParseConfigReadsOpenTelemetryMetricsExporterEnv(t *testing.T) {
	t.Setenv("PIHOLE_BASE_URL", "http://127.0.0.1:8080")
	t.Setenv(pihole.DefaultAppPasswordEnv, "app-password")
	t.Setenv("OTEL_METRICS_EXPORTER", " OTLPHTTP ")

	withTestCommandLine(t, "pihole-exporter")

	cfg := parseConfig()

	if cfg.metricsExporter != metricsExporterOTLPHTTP {
		t.Fatalf("metricsExporter = %q, want %q", cfg.metricsExporter, metricsExporterOTLPHTTP)
	}
}

func withTestCommandLine(t *testing.T, args ...string) {
	t.Helper()

	oldArgs := os.Args
	oldCommandLine := flag.CommandLine

	os.Args = args
	flag.CommandLine = flag.NewFlagSet(args[0], flag.ContinueOnError)
	flag.CommandLine.SetOutput(io.Discard)

	t.Cleanup(func() {
		os.Args = oldArgs
		flag.CommandLine = oldCommandLine
	})
}
