package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/timgladwell/pihole-exporter/pkg/exporter"
	"github.com/timgladwell/pihole-exporter/pkg/pihole"
)

const (
	defaultListenAddr      = ":9617"
	defaultTimeout         = 10 * time.Second
	defaultMetricsExporter = "prometheus"
	shutdownTimeout        = 5 * time.Second
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], nil)
	stop()

	var usage *usageError
	switch {
	case errors.Is(err, flag.ErrHelp):
		os.Exit(0)
	case errors.As(err, &usage):
		os.Exit(2) // usage text already printed by parseConfig
	case err != nil:
		log.Fatal(err)
	}
}

// run is everything main does short of exiting. piholeHTTP is the client the
// Pi-hole AuthClient uses; nil means the default. It exists so in-process
// tests can hand the AuthClient the piholetest mock.
//
// SIGTERM is how Kubernetes and Docker stop the process; cancelling ctx shuts
// down cleanly, which is what gets the Pi-hole session released rather than
// left to occupy an API seat until its TTL expires.
func run(ctx context.Context, args []string, piholeHTTP *http.Client) error {
	cfg, err := parseConfig(args)
	if err != nil {
		return err
	}
	if cfg.healthCheck {
		if err := probeHealth(cfg.listenAddr); err != nil {
			return fmt.Errorf("healthcheck: %w", err)
		}
		return nil
	}

	server, shutdown, err := buildServer(cfg, piholeHTTP)
	if err != nil {
		return err
	}
	defer shutdown()

	served := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case <-ctx.Done():
			stopCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
			defer cancel()
			if err := server.Shutdown(stopCtx); err != nil {
				log.Printf("shutdown HTTP server: %v", err)
			}
		case <-served:
		}
	}()

	err = server.ListenAndServe()
	close(served)
	<-stopped // let Shutdown finish draining before the session is released
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}

func buildServer(cfg config, piholeHTTP *http.Client) (*http.Server, func(), error) {
	client, err := pihole.NewAuthClient(cfg.piHoleURL, cfg.password, pihole.WithUserAgent(userAgent()), pihole.WithHTTPClient(piholeHTTP))
	if err != nil {
		return nil, nil, fmt.Errorf("create Pi-hole client: %w", err)
	}

	mux := http.NewServeMux()
	alive := func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	}
	// /alive is the Kubernetes-facing name for the same liveness answer as
	// /healthz: the process is serving HTTP. Neither talks to Pi-hole.
	mux.HandleFunc("/healthz", alive)
	mux.HandleFunc("/alive", alive)

	// Releasing the Pi-hole session is part of every shutdown path, not just
	// the OpenTelemetry one.
	shutdown := func() {
		ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := client.Logout(ctx); err != nil {
			log.Printf("release Pi-hole session: %v", err)
		}
	}

	switch cfg.metricsExporter {
	case metricsExporterPrometheus:
		registry := prometheus.NewRegistry()
		collector := exporter.NewCollector(client, cfg.timeout)
		registry.MustRegister(collector)
		mux.Handle("/metrics", metricsHandler(promhttp.HandlerFor(registry, promhttp.HandlerOpts{}), collector))
	case metricsExporterOTLP, metricsExporterOTLPGRPC, metricsExporterOTLPHTTP, metricsExporterStdout:
		provider, err := exporter.NewOpenTelemetryMeterProvider(context.Background(), client, cfg.timeout, string(cfg.metricsExporter))
		if err != nil {
			return nil, nil, fmt.Errorf("create %s metrics exporter: %w", cfg.metricsExporter, err)
		}
		releaseSession := shutdown
		shutdown = func() {
			ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
			defer cancel()
			if err := provider.Shutdown(ctx); err != nil {
				log.Printf("shutdown metrics exporter: %v", err)
			}
			releaseSession()
		}
	default:
		return nil, nil, fmt.Errorf("unsupported metrics exporter %q", cfg.metricsExporter)
	}

	log.Printf("pihole-exporter %s listening on %s, metrics exporter %s, compiled for Pi-hole API %s", exporter.Version, cfg.listenAddr, cfg.metricsExporter, pihole.CompiledPiHoleAPIVersion)

	return &http.Server{
		Addr:              cfg.listenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}, shutdown, nil
}

type config struct {
	listenAddr      string
	piHoleURL       string
	password        string
	timeout         time.Duration
	metricsExporter metricsExporter
	healthCheck     bool
}

// usageError is a configuration mistake the operator must fix; main exits 2.
type usageError struct {
	Flag string // the flag at fault: "pihole-url", "password", "metrics-exporter"; "" for a parse error
	Err  error
}

func (e *usageError) Error() string { return e.Err.Error() }
func (e *usageError) Unwrap() error { return e.Err }

func parseConfig(args []string) (config, error) {
	fs := flag.NewFlagSet("pihole-exporter", flag.ContinueOnError)
	listenAddr := fs.String("listen", envOrDefault("LISTEN_ADDR", defaultListenAddr), "HTTP listen address")
	piHoleURL := fs.String("pihole-url", envOrDefault("PIHOLE_BASE_URL", ""), "Pi-hole base URL e.g. http://pi.hole:8080")
	password := fs.String("password", envOrDefault(pihole.DefaultAppPasswordEnv, ""), "Pi-hole app password generated in Pi-hole settings")
	timeout := fs.Duration("timeout", durationEnvOrDefault("SCRAPE_TIMEOUT", defaultTimeout), "Pi-hole scrape timeout")
	metricsExporterValue := fs.String("metrics-exporter", envOrDefault("OTEL_METRICS_EXPORTER", defaultMetricsExporter), "metrics exporter: prometheus, otlp, otlpgrpc, otlphttp, or stdout")
	healthCheck := fs.Bool("healthcheck", false, "probe /healthz on the listen address and exit; the container HEALTHCHECK uses this because the image has no shell")
	if err := fs.Parse(args); err != nil {
		// The flag package has already printed the problem and usage.
		return config{}, &usageError{Err: err}
	}

	if *healthCheck {
		return config{listenAddr: *listenAddr, healthCheck: true}, nil
	}

	usage := func(flagName string, err error) (config, error) {
		_, _ = fmt.Fprintf(fs.Output(), "%v\n\n", err)
		fs.Usage()
		return config{}, &usageError{Flag: flagName, Err: err}
	}

	if *piHoleURL == "" {
		return usage("pihole-url", errors.New("PIHOLE_BASE_URL or -pihole-url is required"))
	}
	if *password == "" {
		return usage("password", fmt.Errorf("%s or -password is required", pihole.DefaultAppPasswordEnv))
	}
	if err := os.Unsetenv(pihole.DefaultAppPasswordEnv); err != nil {
		log.Printf("failed to unset %s: %v", pihole.DefaultAppPasswordEnv, err)
	}

	metricsExporter, err := parseMetricsExporter(*metricsExporterValue)
	if err != nil {
		return usage("metrics-exporter", err)
	}

	return config{
		listenAddr:      *listenAddr,
		piHoleURL:       *piHoleURL,
		password:        *password,
		timeout:         *timeout,
		metricsExporter: metricsExporter,
	}, nil
}

type metricsExporter string

const (
	metricsExporterPrometheus metricsExporter = "prometheus"
	metricsExporterOTLP       metricsExporter = "otlp"
	metricsExporterOTLPGRPC   metricsExporter = "otlpgrpc"
	metricsExporterOTLPHTTP   metricsExporter = "otlphttp"
	metricsExporterStdout     metricsExporter = "stdout"
)

func parseMetricsExporter(value string) (metricsExporter, error) {
	normalized := strings.ToLower(strings.TrimSpace(value))

	switch metricsExporter(normalized) {
	case "", metricsExporterPrometheus:
		return metricsExporterPrometheus, nil
	case metricsExporterOTLP, metricsExporterOTLPGRPC, metricsExporterOTLPHTTP, metricsExporterStdout:
		return metricsExporter(normalized), nil
	default:
		return "", fmt.Errorf("unsupported metrics exporter %q; supported values are prometheus, otlp, otlpgrpc, otlphttp, stdout", value)
	}
}

// metricsHandler serves the readiness probe on /metrics?probe=true and the
// metrics themselves otherwise. The probe reports the last scrape's outcome
// rather than scraping, so probing costs Pi-hole nothing.
func metricsHandler(metrics http.Handler, collector *exporter.Collector) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("probe") != "true" {
			metrics.ServeHTTP(w, r)
			return
		}
		if !collector.Ready() {
			http.Error(w, "last Pi-hole scrape failed or has not run yet", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready\n"))
	})
}

// userAgent names the exporter and its release on outbound requests, so a
// Pi-hole admin reading their logs can tell what is scraping them.
func userAgent() string {
	return "pihole-exporter/" + exporter.Version
}

// probeHealth GETs /healthz on the running exporter. It exists so the
// container HEALTHCHECK has something to run: the image ships no shell or curl.
func probeHealth(listenAddr string) error {
	client := &http.Client{Timeout: 5 * time.Second}

	req, err := http.NewRequest(http.MethodGet, healthCheckURL(listenAddr), nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent())

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %s", resp.Status)
	}
	return nil
}

// healthCheckURL rewrites a listen address into a loopback /healthz URL, so a
// wildcard bind such as ":9617" is probed on 127.0.0.1 rather than an empty host.
func healthCheckURL(listenAddr string) string {
	host, port, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return "http://" + listenAddr + "/healthz"
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/healthz"
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func durationEnvOrDefault(key string, fallback time.Duration) time.Duration {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}

	duration, err := time.ParseDuration(value)
	if err == nil {
		return duration
	}

	seconds, err := strconv.Atoi(value)
	if err == nil {
		return time.Duration(seconds) * time.Second
	}

	log.Printf("invalid %s=%q, using %s", key, value, fallback)
	return fallback
}
