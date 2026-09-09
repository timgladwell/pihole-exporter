package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/timgladwell/pihole-exporter/internal/piholetest"
	"github.com/timgladwell/pihole-exporter/pkg/pihole"
)

// TestExporterProcessServesMetrics runs the built binary as a real process
// against a stub Pi-hole. The other tests exercise buildServer() in-process;
// this one covers what only the assembled command can break — flag and env
// wiring, the listener, and the -healthcheck path the image HEALTHCHECK uses.
func TestExporterProcessServesMetrics(t *testing.T) {
	binary := buildExporterBinary(t)
	stub := piholetest.StartStubPiholeServer(t)
	addr := freeAddr(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := exec.CommandContext(ctx, binary)
	cmd.Env = append(cmd.Environ(),
		"LISTEN_ADDR="+addr,
		"PIHOLE_BASE_URL="+stub.URL,
		pihole.DefaultAppPasswordEnv+"=app-password",
	)
	logs := &strings.Builder{}
	cmd.Stdout = logs
	cmd.Stderr = logs
	if err := cmd.Start(); err != nil {
		t.Fatalf("start exporter: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		_ = cmd.Wait()
		t.Logf("exporter output:\n%s", logs)
	})

	base := "http://" + addr
	waitForHealthz(t, base)

	// Nothing scrapes Pi-hole until /metrics is requested, so readiness is
	// still 503 here even though the process is up and healthy.
	if got := getStatus(t, base+"/metrics?probe=true"); got != http.StatusServiceUnavailable {
		t.Fatalf("readiness before first scrape = %d, want %d", got, http.StatusServiceUnavailable)
	}

	body := getBody(t, base+"/metrics")
	if !strings.Contains(body, "pihole_exporter_scrape_success 1") {
		t.Fatalf("metrics body missing successful scrape:\n%s", body)
	}

	if got := getStatus(t, base+"/metrics?probe=true"); got != http.StatusOK {
		t.Fatalf("readiness after successful scrape = %d, want %d", got, http.StatusOK)
	}

	// The image HEALTHCHECK runs exactly this, with no shell in the container.
	if out, err := exec.Command(binary, "-healthcheck", "-listen", addr).CombinedOutput(); err != nil {
		t.Fatalf("-healthcheck error = %v, output: %s", err, out)
	}
}

func buildExporterBinary(t *testing.T) string {
	t.Helper()

	binary := filepath.Join(t.TempDir(), "pihole-exporter")
	out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go build error = %v, output: %s", err, out)
	}
	return binary
}

// freeAddr returns a loopback address nothing is listening on.
// ponytail: racy by construction — another process could claim the port
// between the close and the exporter's bind. Retry if that ever flakes.
func freeAddr(t *testing.T) string {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release port: %v", err)
	}
	return addr
}

func waitForHealthz(t *testing.T, base string) {
	t.Helper()

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(base + "/healthz")
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("exporter did not serve /healthz within 20s")
}

func getBody(t *testing.T, url string) string {
	t.Helper()

	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("Get(%s) error = %v", url, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Get(%s) status = %d, body: %s", url, resp.StatusCode, body)
	}
	return string(body)
}
