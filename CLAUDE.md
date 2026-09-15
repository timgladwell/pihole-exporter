# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Purpose

The exporter is a **lightweight** and **resilient** way to get PiHole observability information into a user's observability stack.

The module path is `github.com/timgladwell/pihole-exporter`.

## Commands

```sh
# Run all tests
go test ./...

# Run all tests with coverage, attributing cross-package coverage correctly
go test -coverpkg=./... -coverprofile=coverage.out ./... && go tool cover -func=coverage.out

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

**Tool requirements.** Go, plus `podman` or `docker` for the image layer, plus a
coverage tool (#42). All of it is required locally — the same checks CI runs, run
before pushing. A check that only ever runs in CI is a check nobody runs while
the code is still cheap to change.

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

Tests use only the standard `testing` package — no test framework. Each package
has its own `_test.go` files.

### The layers

**Unit tests** — Go's unit is the package, not a class, so these are often called
package tests; `pkg/pihole` and `pkg/exporter` each have their own. Every
exported function and method is tested for both its success and its failure
conditions. One test asserts one behaviour: the value returned, *and* the change
to system state it should have made — or, for an error case, that no state
changed. A unit test needs a mock only where the unit itself depends on something
external; where it does not, exercise it directly.

Tests assert behaviour, not implementation. Go lets a test reach unexported
identifiers in the package it tests, and that is a footgun: reaching in to assert
private state couples the test to how the code is written today, so a refactor
that changes nothing observable breaks it. State that other code can reach — a
database, a file, an API — is public for testing purposes even when the Go
identifier is not. Asserting private state is a large exception and needs a
reason.

**Integration tests** — several units together, inside the test harness. A mock
Pi-hole holding known statistics, a real `AuthClient`, a real `Collector`, a real
scrape request at the front door: assert that a session was authenticated, that
the scrape response carries the expected values, and that the *next* scrape
behaves as intended. This is the layer that goes deep and wide across use cases,
and the one that catches regressions.

**Deployable tests** — end-to-end against the artefacts we ship: the built binary
(`cmd/pihole-exporter/integration_test.go`) and the container image (the CI
`image` job, against `internal/piholetest/stub`). These still exercise end-to-end
behaviour, but the focus shifts to confirming the build and packaging worked —
flag and env wiring, the listener, the entrypoint, the non-root user, the
`HEALTHCHECK`, and that the binary in the image runs at all.

In a system this small there are not many units to integrate before you are
looking at the whole system, so the harness layers necessarily overlap. The
boundary that matters is: **inside the test harness** we go deeper and wider to
cover each use case and catch regressions; **against deployables** we confirm the
thing we ship was assembled correctly.

**Local tooling.** `go test ./...` — unit, integration and the built-binary
tests — needs Go and nothing else. The deployable image layer needs `podman` or
`docker`, and a container runtime is a **required local dependency**, not a
CI-only one: a test that can only run in CI is one nobody runs before pushing,
which is how the deployable layer rots. See `## Commands` for the full list.

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

Assert sequence, count **and** payload. Sequence and count catch the behavioural
bugs — authenticate, *then* request stats; zero authentication calls when a valid
session is cached; exactly one delete per session, carrying the SID being
deleted. Payload assertions catch the ones that matter more: this exporter exists
to move data from Pi-hole to Prometheus accurately, and it is useless if a value
is corrupted, mislabelled or dropped in transit. Assert that the number Pi-hole
reported is the number that comes out of `/metrics`, under the right name and
labels. `internal/piholetest.Handler` cannot express any of this yet (#31).

- Request assertions cover method, path, headers (`X-FTL-SID`, `X-FTL-CSRF`,
  `User-Agent`, `Accept`) and the decoded body.
- Fixtures are generated from real Pi-hole responses rather than written by hand,
  and our assumptions about the API are checked against the upstream spec so the
  fixtures cannot drift away from what Pi-hole actually returns (#39). A fixture
  that resembles the real API is what makes an upstream change show up as a test
  failure instead of a metric that quietly stops.
- The mock Pi-hole is a shared utility for the whole harness — one implementation
  in `internal/piholetest`, used by every layer — not a server hand-rolled per
  test. That is what keeps the layers agreeing on what a Pi-hole is.

### Nothing is manual, and "too hard to test" is a design problem

- If something cannot be tested, refactor the code — do not lower the standard.
- What makes code hard to test is being wired into the language runtime and the
  operating system: signals, `os.Exit`, `os.Args`, listeners, clocks. A function
  that calls `os.Exit` cannot be tested, because it takes the test binary down
  with it. Isolate that surface from as much as possible, so that what remains
  hard to test is a handful of lines with no logic in them.
- Keep that edge in `main`, and put everything else below it in functions that
  take arguments and return a result. Go's result type is the `(value, error)`
  pair — see **Errors are data** below for what belongs in the error half.
- The only thing that justifies a manual check is infrastructure CI cannot have:
  a real Pi-hole (`PIHOLE_LIVE_TEST=1`, skipped otherwise) or a published
  manifest list (#29). Each of those gets an issue naming how it is exercised.
- Never synchronise a test with `time.Sleep`. Inject the clock (`AuthClient.now`)
  or poll a condition with a deadline. Sleep-based tests are the ones that go
  flaky under `-race` in CI and get deleted a year later.

### Errors are data, not sentences

Go's version of a `Result` is the `(value, error)` pair: both halves come back
together, success and failure are both ordinary results, and the `error` half is
where the context lives. Use the language's own idiom rather than a bespoke
`Result[T]` — the stdlib, `go vet` and every caller already understand `error`.

**Use typed errors wherever a caller might branch on the outcome**, which is
most of them. A struct implementing `error` carries fields; a formatted string
carries nothing a program can read:

```go
type RequestError struct {
    Action     string        // "query_pihole"
    Path       string        // "/stats/summary"
    StatusCode int           // 404
    Duration   time.Duration // how long it took
    Retryable  bool          // transient, or permanent?
    Err        error         // wrapped cause
}
```

- Keep values **raw**, in their natural type — an `int` status code, a
  `time.Duration`, a `bool`. Structured logs, metrics, dashboards and alerts all
  need the value, not a sentence containing it. A number substituted into a
  string has to be parsed back out by whoever needs it, and cannot be a metric
  label at all.
- A human-readable message is one more field, rendered by `Error()`, not the
  place the information lives.
- `errors.Is` and `errors.As` interrogate the chain; `%w` preserves it. Tests
  assert against the typed fields or a sentinel, never by matching message text —
  string-match assertions break on rewording and tell you nothing when they fail.
- Sentinel errors (`ErrUnauthenticated`, `ErrMissingPassword`) stay useful for
  cases with no payload. Where a caller needs to know *which* request failed and
  *whether retrying could help*, that is a typed error.

The failure classification that #38 needs — transient or permanent — is exactly a
field on the error. Do not re-derive it by inspecting strings at the call site;
decide it where the failure happens, and carry it. The current code returns only
formatted strings, which is why #40 exists.

### Resiliency paths are part of the expected behaviour

External dependencies are inherently untrusted, and Pi-hole being unhealthy must
not make the exporter unhealthy. The exporter stays up, keeps serving, and keeps
saying what it knows.

**Degrade in stages, and recover on your own.** A dependency that stops answering
gets retried a few times quickly, then with a widening gap, and eventually drops
to a heartbeat — a check every few minutes to see whether Pi-hole is back. When
it answers again, normal cadence resumes without a restart and without
intervention (#38).

**Tell downstream what it is looking at.** "We could not reach Pi-hole" and "the
value Pi-hole reported is 0" are completely different facts, and a consumer that
cannot tell them apart will draw graphs of fiction. A failed scrape must never
present a fabricated zero as data: it reports the failure and withholds the
values (#37). Systems that depend on us deserve the same context we want from
Pi-hole.

**Keep the process data separate from the collected data.** Metrics describing
how the scrape went — succeeded or failed, why, how long, which attempt — live
apart from the metrics scraped *from* Pi-hole, and are named so nothing can
confuse the two (`pihole_exporter_*` versus `pihole_*`). The separation is what
lets an alert or a dashboard identify a failure *and its kind* directly, instead
of inferring it from a gap in a graph or a suspicious zero.

**Surface the coping, not just the failure.** Degrading gracefully is only half
the job: if the exporter backs off and waits for Pi-hole, it must emit that it is
doing so, so an operator can alert on "Pi-hole has been down for five minutes" or
"no fresh data for five minutes" rather than discovering it later. Resilient
behaviour that is invisible looks identical to a system quietly doing nothing.
Consecutive failures, current backoff state, and the age of the last successful
scrape are all observable facts, and they get emitted (#38).

What this means for tests:

- Pi-hole unreachable, refusing auth, or slower than the scrape timeout: the
  failure is reported, no invented values are emitted, and the *next* scrape
  recovers without a restart.
- No Pi-hole at all at startup: the exporter still starts and still serves
  `/metrics`, reporting the failure. A dependency being down is not a reason to
  refuse to run — it is a reason to explain the context of the data we produce.
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

Preventing failure is usually not possible; behaving the same way every time it
happens is. Which response is right depends on whether the failure can pass:

- **Transient** — a timed-out request, a restarting Pi-hole, a receiver that has
  gone away. Retry quickly a few times, back off, fall into a wait-and-see
  cadence, and recover automatically when the dependency returns. Never give up
  permanently on a problem that can resolve itself.
- **Permanent** — configuration that cannot be valid, a response that can never
  be parsed, an operation that can never succeed. Say so once, clearly, and stop
  cleanly rather than retrying something that will fail identically forever.

Either way: never a silent partial state, and every failure path leaves
observable evidence — a metric, or a log line naming what failed and why. A test
that exercises a failure asserts that evidence too. That is what makes an
incident diagnosable instead of a guess, and it is the difference between a
product people trust and one they second-guess.

### Go specifics worth knowing here

- Go has no `Result` type; `(value, error)` is it. Use `errors.Is`/`errors.As`
  and wrapped errors to carry context, and assert against sentinel errors
  (`ErrUnauthenticated`, `ErrMissingPassword`) rather than message text.
- Go tests live in the package they test and can reach unexported identifiers.
  Prefer asserting through the exported surface; reach inside only for state a
  caller genuinely cannot observe.
- `t.Setenv` panics if the test also calls `t.Parallel()`. Tests that set env
  vars or touch `flag.CommandLine` run serially; everything else uses
  `t.Parallel()`.
- `t.Cleanup` over `defer` in helpers, so the caller cannot forget it.
- `go test -race` is what catches the concurrency bugs this codebase can
  actually have: the session mutex, and shutdown racing an in-flight scrape.
  CI runs it; run it before pushing.

### Style

- Use `t.Helper()` on all assertion and helper functions
- Error check pattern: `if err != nil { t.Fatalf("Thing() error = %v", err) }`
- Assertion pattern: `if got != want { t.Fatalf("Field = %v, want %v", got, want) }`
- One test function per behaviour, not per function under test. A function with
  several responsibilities gets several tests — `TestShutdownReleasesPiholeSession`,
  `TestShutdownStopsTheHTTPServer`, `TestShutdownIsSafeWithoutASession` — because
  a failing test name should identify the regression on its own, and a
  scenario-specific test is far easier to keep current than one asserting several
  things at once.
- No table-driven tests in this codebase

**Coverage expectations**: *all* behaviour in `pkg/pihole`, `pkg/exporter`, and
`cmd/pihole-exporter` is covered, not only newly added behaviour. Where current
tests do not cover current behaviour, that gap is a thing to close, not a
precedent to follow.

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
