package main

import (
	"bytes"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

func TestHealthCheckURLUsesLoopbackForWildcardBind(t *testing.T) {
	t.Parallel()

	if got, want := healthCheckURL(":9617"), "http://127.0.0.1:9617/healthz"; got != want {
		t.Fatalf("healthCheckURL(\":9617\") = %q, want %q", got, want)
	}
	if got, want := healthCheckURL("0.0.0.0:9617"), "http://127.0.0.1:9617/healthz"; got != want {
		t.Fatalf("healthCheckURL(\"0.0.0.0:9617\") = %q, want %q", got, want)
	}
	if got, want := healthCheckURL("192.168.0.2:9617"), "http://192.168.0.2:9617/healthz"; got != want {
		t.Fatalf("healthCheckURL(\"192.168.0.2:9617\") = %q, want %q", got, want)
	}
}

func TestProbeHealthChecksStatus(t *testing.T) {
	t.Parallel()

	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer healthy.Close()

	if err := probeHealth(strings.TrimPrefix(healthy.URL, "http://")); err != nil {
		t.Fatalf("probeHealth() error = %v", err)
	}

	unhealthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer unhealthy.Close()

	if err := probeHealth(strings.TrimPrefix(unhealthy.URL, "http://")); err == nil {
		t.Fatal("probeHealth() error = nil, want error for 503")
	}
}

func TestAliveEndpoint(t *testing.T) {
	stubPiholeServer := piholetest.StartStubPiholeServer(t)

	t.Setenv("PIHOLE_BASE_URL", stubPiholeServer.URL)
	t.Setenv(pihole.DefaultAppPasswordEnv, "app-password")

	withTestCommandLine(t, "pihole-exporter")

	server, shutdown := buildServer()
	t.Cleanup(shutdown)
	testExporter := httptest.NewServer(server.Handler)
	defer testExporter.Close()

	resp, err := http.Get(testExporter.URL + "/alive")
	if err != nil {
		t.Fatalf("Get(/alive) error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/alive status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
}

func TestReadinessProbeFailsUntilFirstScrapeSucceeds(t *testing.T) {
	stubPiholeServer := piholetest.StartStubPiholeServer(t)

	t.Setenv("PIHOLE_BASE_URL", stubPiholeServer.URL)
	t.Setenv(pihole.DefaultAppPasswordEnv, "app-password")

	withTestCommandLine(t, "pihole-exporter")

	server, shutdown := buildServer()
	t.Cleanup(shutdown)
	testExporter := httptest.NewServer(server.Handler)
	defer testExporter.Close()

	if got := getStatus(t, testExporter.URL+"/metrics?probe=true"); got != http.StatusServiceUnavailable {
		t.Fatalf("probe before first scrape = %d, want %d", got, http.StatusServiceUnavailable)
	}

	if got := getStatus(t, testExporter.URL+"/metrics"); got != http.StatusOK {
		t.Fatalf("/metrics status = %d, want %d", got, http.StatusOK)
	}

	if got := getStatus(t, testExporter.URL+"/metrics?probe=true"); got != http.StatusOK {
		t.Fatalf("probe after successful scrape = %d, want %d", got, http.StatusOK)
	}
}

// TestShutdownReleasesPiholeSession covers the shutdown half of the seat
// problem: main() runs this closure on SIGTERM, and it has to delete the
// session rather than leave it holding an API seat until its TTL expires.
func TestShutdownReleasesPiholeSession(t *testing.T) {
	var deleted []string
	stub := piholetest.Handler(t.Fatalf)
	stubPiholeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && r.URL.Path == "/api/auth" {
			deleted = append(deleted, r.Header.Get("X-FTL-SID"))
		}
		stub.ServeHTTP(w, r)
	}))
	defer stubPiholeServer.Close()

	t.Setenv("PIHOLE_BASE_URL", stubPiholeServer.URL)
	t.Setenv(pihole.DefaultAppPasswordEnv, "app-password")

	withTestCommandLine(t, "pihole-exporter")

	server, shutdown := buildServer()
	testExporter := httptest.NewServer(server.Handler)
	defer testExporter.Close()

	// Scrape once so there is a session to release.
	if got := getStatus(t, testExporter.URL+"/metrics"); got != http.StatusOK {
		t.Fatalf("/metrics status = %d, want %d", got, http.StatusOK)
	}

	shutdown()

	if len(deleted) != 1 || deleted[0] != "sid-1" {
		t.Fatalf("deleted sessions = %v, want [sid-1]", deleted)
	}
}

func getStatus(t *testing.T, url string) int {
	t.Helper()

	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("Get(%s) error = %v", url, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	return resp.StatusCode
}
