# Changelog

All notable changes to this project will be documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

<!-- Add a new entry for each release. The release workflow checks that an entry matching the tag exists. -->

## [Unreleased]

### Added

- `version` label on `pihole_exporter_build_info`, injected from the release tag at build time
- `linux/amd64` images alongside `linux/arm64`, published as a manifest list
- Integration test running the built binary as a process against a stub Pi-hole, and a CI `image` job that builds and runs the container image against the same stub
- `/alive` liveness endpoint, and a readiness probe on `/metrics?probe=true` that reports the last scrape's outcome without contacting Pi-hole
- `-healthcheck` flag that probes `/healthz` and exits, used by the image `HEALTHCHECK`
- OCI image labels linking the image back to this repository
- Pi-hole sessions are now deleted through `DELETE /api/auth` when they are refreshed and on graceful shutdown, releasing the API seat instead of leaving it occupied until its TTL expires
- MIT `LICENSE`, previously missing
- README note recording that this repository is a fork of `alantoch/pihole-exporter`

### Changed

- Base image is now `gcr.io/distroless/static:nonroot`: no shell, no package manager, runs as UID 65532
- `:latest` now moves only for stable releases; pre-releases publish their version tag only
- The release workflow runs gofmt, build, vet and race tests before publishing

## [v0.1.0-alpha] - 2026-05-23

### Added

- Pi-hole v6 API client with session-based authentication and TOTP support
- Prometheus metrics collector exposing Pi-hole statistics (queries, blocking, cache, clients, upstream resolvers)
- `/healthz` endpoint reporting exporter scrape health
- OpenTelemetry metrics exporter support via `OTEL_METRICS_EXPORTER` environment variable
- Docker image published to GitHub Container Registry (`ghcr.io`) targeting `linux/arm64` (Raspberry Pi 4 64-bit)
- Release-triggered CI workflow for building and pushing the container image via Podman
- Grafana dashboard JSON for visualising Pi-hole metrics
