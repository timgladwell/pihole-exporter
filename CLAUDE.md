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

### The three layers

- **Package tests** (`pkg/pihole`, `pkg/exporter`) mock the Pi-hole HTTP API with
  `httptest.NewServer` and verify behaviour at the HTTP boundary. Do not mock
  `AuthClient` or internal interfaces — wire up a real `AuthClient` pointed at a
  test server. The collector tests do the same: a full fake Pi-hole endpoint and
  a real `Collector`.
- **Integration test** (`cmd/pihole-exporter/integration_test.go`) runs the built
  binary as a process against the stub Pi-hole. It covers what only the assembled
  command can break: flag and env wiring, the listener, the `-healthcheck` path.
- **System test** (the CI `image` job) runs the container image against
  `internal/piholetest/stub`. It covers what only the image can break: entrypoint,
  non-root user, the `HEALTHCHECK`, and that the binary in the image runs at all.

New behaviour lands with a happy-path test at the lowest layer that can see it,
plus the sad paths that actually happen in operation — Pi-hole down, auth
rejected, a body that does not parse, a scrape past the timeout. A feature whose
only test is "it works when everything works" is half-tested.

### Mocks validate; stubs supply a value

The two are not the same thing and the distinction decides what a test proves.

- A **mock** stands in for a system outside the test's control that we integrate
  with — here, Pi-hole. A mock validates: it asserts the shape and content of
  every request it receives, how many arrived, and in what order. A request that
  does not match an expectation is a failure, and so is an expected request that
  never arrived. Think `webmock`, not "a server that returns JSON".
- A **stub** supplies a trivial value the test does not care about — a build
  version, a fixed clock. It asserts nothing, and that is correct.

Mock only across a boundary we do not control. Do not mock `AuthClient` or
internal interfaces: wire up the real thing pointed at a mock Pi-hole, so what is
asserted is the traffic, not that a Go method was called.

The behaviour worth asserting is usually sequence and count, not payloads:
authenticate, *then* request stats; zero authentication calls when a valid
session is cached; exactly one delete per session, carrying the SID being
deleted. `internal/piholetest.Handler` does not do this yet — it answers by path
and validates nothing (#31).

- Request assertions cover method, path, headers (`X-FTL-SID`, `X-FTL-CSRF`,
  `User-Agent`, `Accept`) and the decoded body.
- Response bodies should look like a real Pi-hole's, captured from one where
  practical (`PIHOLE_LIVE_TEST=1`) and kept in `internal/piholetest` or
  `testdata/`. A fixture that resembles the real API is what makes an upstream
  change show up as a test failure instead of a metric that quietly stops.
- Extend the shared mock rather than hand-rolling a new server per test, so every
  layer agrees on what a Pi-hole is and the assertions do not drift apart.

### Nothing is manual, and "too hard to test" is a design problem

- If something cannot be tested, refactor the code — do not lower the standard.
  The usual culprit is operating-system integration: signals, `os.Exit`,
  `os.Args`, listeners, clocks. A function that calls `os.Exit` cannot be tested,
  because it takes the test binary down with it.
- Keep that surface in a thin edge with no logic in it (`main`), and put
  everything else below it in functions that take arguments and return errors.
  Testing OS integration directly is difficult and fragile; testing everything
  else is not, once it is no longer entangled with it. See #28.
- The only thing that justifies a manual check is infrastructure CI cannot have:
  a real Pi-hole (`PIHOLE_LIVE_TEST=1`, skipped otherwise) or a published
  manifest list (#29). Each of those gets an issue naming how it is exercised.
- Never synchronise a test with `time.Sleep`. Inject the clock (`AuthClient.now`)
  or poll a condition with a deadline. Sleep-based tests are the ones that go
  flaky under `-race` in CI and get deleted a year later.

### Resiliency paths are part of the expected behaviour

The exporter sits between two systems it does not control, so how it behaves
when they misbehave is a feature, and gets tested like one:

- Pi-hole unreachable, refusing auth, or slower than the scrape timeout: the
  scrape fails, `pihole_exporter_scrape_success` goes to 0, `_scrape_error`
  appears, and the *next* scrape recovers without a restart.
- No Pi-hole at all at startup: the exporter still starts and still serves
  `/metrics`, reporting the failure through those gauges. A dependency being
  down is not a reason to refuse to run.
- The OTLP receiver going away: push failures are logged, the exporter keeps
  collecting, and nothing accumulates without a bound.
- Nobody scraping for a long time: memory does not grow unbounded. The
  cardinality risk is the labelled metrics (`top_clients`, `top_domains`,
  `types`, `status`, `replies`), where the label set follows network traffic
  rather than anything the exporter controls.

Assumptions about the environment get questioned and then asserted, so a future
change cannot quietly break them: there is not always exactly one Pi-hole, and a
rolling restart means two instances answer the same address for a few seconds
(#33). Load testing belongs in `go test` too — cardinality churn over thousands
of scrapes, idle allocation, concurrent scrapes under `-race` — not in a separate
harness we never run (#35). Guard the slow cases with `testing.Short()`.

### Consistent behaviour beats prevented failure

Superior software behaves the same way every time, including when it fails.
Preventing every failure is not the goal — failing the same way, saying so, and
recovering on the next cycle usually is. Practically:

- Prefer a predictable failure and a clean restart over heroic in-place
  recovery. Never a silent partial state.
- Every failure path is observable: a metric, or a log line naming what failed
  and why. If a test exercises a failure, it asserts the observable evidence too
  — that is what makes an incident diagnosable rather than a guess.
- Degrade in one direction only: a failed scrape must not corrupt the last good
  state or leave the session, the meter provider, or the HTTP server in a
  half-built condition.

### Go specifics worth knowing here

- `t.Setenv` panics if the test also calls `t.Parallel()`. Tests that set env
  vars or touch `flag.CommandLine` run serially; everything else uses
  `t.Parallel()`.
- `t.Cleanup` over `defer` in helpers, so the caller cannot forget it.
- Assert sentinel errors with `errors.Is` (`ErrUnauthenticated`,
  `ErrMissingPassword`) rather than matching message text.
- `go test -race` is what catches the concurrency bugs this codebase can
  actually have: the session mutex, and shutdown racing an in-flight scrape.
  CI runs it; run it before pushing.

### Style

- Use `t.Helper()` on all assertion and helper functions
- Error check pattern: `if err != nil { t.Fatalf("Thing() error = %v", err) }`
- Assertion pattern: `if got != want { t.Fatalf("Field = %v, want %v", got, want) }`
- One test function per scenario; no table-driven tests in this codebase
- Name the test after the behaviour, not the function:
  `TestShutdownReleasesPiholeSession`, not `TestShutdown`

**Coverage expectations**: all new behaviour in `pkg/pihole`, `pkg/exporter`, and
`cmd/pihole-exporter` must have tests.

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
