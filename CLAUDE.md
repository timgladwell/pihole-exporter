# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Purpose

The exporter is a **lightweight** and **resilient** way to get PiHole observability information into a user's observability stack.

The module path is `github.com/timgladwell/pihole-exporter`.

## Commands

```sh
# Run all tests
go test ./...

# Run a single test
go test ./pkg/pihole -run TestSessionAuthenticatesAndCachesToken

# Run what CI runs, before pushing
gofmt -l . && go build ./... && go vet ./... && go test -race ./...

# Run live auth tests against a real Pi-hole (requires env vars)
PIHOLE_BASE_URL="http://192.168.0.2" PIHOLE_APP_PASSWORD="..." PIHOLE_LIVE_TEST=1 go test ./pkg/pihole -run TestLiveAuth

# Regenerate metric definitions from the upstream Pi-hole OpenAPI spec
go generate ./...

# Run locally
PIHOLE_BASE_URL="http://192.168.0.2" PIHOLE_APP_PASSWORD="..." go run ./cmd/pihole-exporter

# Build Docker image
docker build -t pihole-exporter .
```

## Architecture

The exporter has three layers:

**`pkg/pihole`** — Pi-hole API client
- `auth.go`: `AuthClient` authenticates via `/api/auth`, stores and auto-refreshes the session (SID + CSRF headers). Sessions are protected by a mutex. The password is consumed from the environment at startup and then removed from it.
- `client.go`: `GetJSONMap` makes authenticated GET requests against Pi-hole API endpoints.
- `metrics.go`: `CollectMetrics` iterates `compiledMetricSpecs`, groups them by endpoint (to deduplicate HTTP requests), then extracts numeric values by JSON path. Metrics with a `Label` field expand objects into one metric per key.
- `metrics_gen.go`: **generated file** — do not edit. Produced by `go generate ./...` from the Pi-hole OpenAPI spec. Contains `CompiledPiHoleAPIVersion` and `compiledMetricSpecs`.

**`pkg/exporter`** — bridges the pihole client to metrics backends
- `collector.go`: Implements `prometheus.Collector`. On each scrape it calls `CollectMetrics`, emits the Pi-hole metrics, plus exporter-level gauges (`pihole_exporter_build_info`, `_scrape_success`, `_scrape_duration_seconds`) and, on failure, `_scrape_error` / `_metric_error`. `Desc` objects are lazily created and cached.
- `otel.go`: Wraps `CollectMetrics` for OpenTelemetry push exporters (OTLP gRPC, OTLP HTTP, stdout).

**`cmd/pihole-exporter`** — entrypoint
- Parses config from flags/env vars, wires up `AuthClient`, selects either the Prometheus or OTel code path, and starts an HTTP server on `:9617`.

**`tools/pihole-metricgen`** — code generator
- Fetches the Pi-hole OpenAPI `main.yaml` (URL or local path), walks `/stats/*` endpoints tagged `Metrics`, and emits `metrics_gen.go`. Only `integer`, `number`, and `boolean` schema types become metrics; objects named `types`, `status`, or `replies` become labeled metrics.

## Testing standards

Tests use only the standard `testing` package — no test framework. Each package has its own `_test.go` files.

**How to test**: Tests mock the Pi-hole HTTP API using `httptest.NewServer` and verify behavior at the HTTP boundary. Do not mock `AuthClient` or internal interfaces — wire up a real `AuthClient` pointed at a test server. This mirrors how the collector tests work: they stand up a full fake Pi-hole endpoint and gather from a real `Collector`.

**Test style**:
- Use `t.Parallel()` where safe (avoid it when mutating `os.Args` or `flag.CommandLine`)
- Use `t.Setenv()` for env vars (auto-restores on cleanup)
- Use `t.Helper()` on all assertion/helper functions
- Error check pattern: `if err != nil { t.Fatalf("Thing() error = %v", err) }`
- Assertion pattern: `if got != want { t.Fatalf("Field = %v, want %v", got, want) }`
- One test function per scenario; no table-driven tests in this codebase

**Coverage expectations**: All new behaviour in `pkg/pihole`, `pkg/exporter`, and `cmd/pihole-exporter` must have tests. Live tests against a real Pi-hole are gated by `PIHOLE_LIVE_TEST=1` and skipped otherwise.

## CI

`.github/workflows/ci.yml` runs on every pull request and on pushes to `main`: `gofmt -l` (must be empty), `go build ./...`, `go vet ./...`, `go test -race ./...`.

`go generate ./...` is deliberately **not** a CI check. It fetches the Pi-hole OpenAPI spec from the `master` branch of `pi-hole/FTL`, so a no-diff check would fail whenever upstream changes, unrelated to the PR under test.

## Repo settings

Settings that live in GitHub, not in the repo, and are easy to break from here.

**Ruleset "No push to main"** (id 16776399), scoped to `~DEFAULT_BRANCH`:
`non_fast_forward`, `pull_request` (0 approvals, merge commits only),
`required_signatures`, and `required_status_checks` requiring the context
`test`, pinned to integration_id 15368 (GitHub Actions).

- The required context `test` is the **job id** in `.github/workflows/ci.yml`.
  Renaming that job leaves pull requests pending forever rather than failing —
  the ruleset waits for a check nothing reports.
- `required_signatures` gates `main` only. Unsigned commits on feature branches
  are expected and correct; they get re-signed when the stack is rebased before
  merge.

**Use HTTPS remotes, not SSH.** The signing key is a FIDO2 `sk-` key, so an SSH
remote forces a YubiKey touch on every fetch and push.

## Release workflow

Releases are triggered by publishing a GitHub Release. The workflow (`.github/workflows/release.yml`):
1. Verifies `CHANGELOG.md` has an entry matching the release tag (format `## [v0.x.x]`).
2. Re-runs the CI checks (gofmt, build, vet, race tests) — a release is published
   from a tag and does not re-run the pull request's status checks.
3. Builds static `linux/amd64` and `linux/arm64` binaries with CGO disabled, with
   the tag injected via `-ldflags -X .../pkg/exporter.Version`.
4. Pushes a manifest list to `ghcr.io/<owner>/pihole-exporter:<version>` using Podman.
   `:latest` moves only when `github.event.release.prerelease` is false.

A CHANGELOG entry is required before publishing a release.
