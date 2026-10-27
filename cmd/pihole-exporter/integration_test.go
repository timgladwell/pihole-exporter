package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
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

// TestExporterProcessReleasesSessionOnSIGTERM is the signal path end to end:
// SIGTERM must make the real process exit 0 after deleting its Pi-hole session.
func TestExporterProcessReleasesSessionOnSIGTERM(t *testing.T) {
	binary := buildExporterBinary(t)

	var mu sync.Mutex
	var deleted []string
	stub := piholetest.Handler(t.Errorf)
	stubServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && r.URL.Path == "/api/auth" {
			mu.Lock()
			deleted = append(deleted, r.Header.Get("X-FTL-SID"))
			mu.Unlock()
		}
		stub.ServeHTTP(w, r)
	}))
	defer stubServer.Close()
	addr := freeAddr(t)

	// Not CommandContext: cancelling its context sends SIGKILL.
	cmd := exec.Command(binary)
	cmd.Env = append(cmd.Environ(),
		"LISTEN_ADDR="+addr,
		"PIHOLE_BASE_URL="+stubServer.URL,
		pihole.DefaultAppPasswordEnv+"=app-password",
	)
	logs := &strings.Builder{}
	cmd.Stdout = logs
	cmd.Stderr = logs
	if err := cmd.Start(); err != nil {
		t.Fatalf("start exporter: %v", err)
	}
	waited := make(chan error, 1)
	go func() {
		waited <- cmd.Wait()
		close(waited) // later receives in Cleanup return at once
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill() // no-op if it already exited
		<-waited
		t.Logf("exporter output:\n%s", logs)
	})

	base := "http://" + addr
	waitForHealthz(t, base)
	getBody(t, base+"/metrics") // scrape once so there is a session to release

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal exporter: %v", err)
	}
	select {
	case err := <-waited:
		if err != nil {
			t.Fatalf("exporter exit error = %v, want clean exit", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("exporter did not exit within 10s of SIGTERM")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(deleted) != 1 || deleted[0] != "sid-1" {
		t.Fatalf("deleted sessions = %v, want [sid-1]", deleted)
	}
}

// TestBinaryExcludesTestCode guards the production build: Go links only what
// main imports, so the Pi-hole mocks stay out of the binary until a non-test
// file imports them by mistake. This makes that mistake fail a test.
func TestBinaryExcludesTestCode(t *testing.T) {
	t.Parallel()

	out, err := exec.Command("go", "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list error = %v, output: %s", err, out)
	}

	for _, pkg := range strings.Fields(string(out)) {
		switch {
		case pkg == "testing",
			pkg == "net/http/httptest",
			strings.HasPrefix(pkg, "github.com/jarcoal/httpmock"),
			strings.HasPrefix(pkg, "github.com/timgladwell/pihole-exporter/internal/piholetest"):
			t.Errorf("production binary depends on test package %s", pkg)
		}
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
