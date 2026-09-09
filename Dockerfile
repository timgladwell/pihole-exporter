# distroless/static ships CA certificates and a nonroot user in ~2MB, so a
# CGO_ENABLED=0 binary needs nothing else. Build the binary first:
#   CGO_ENABLED=0 GOOS=linux go build -o pihole-exporter ./cmd/pihole-exporter
FROM gcr.io/distroless/static:nonroot

ARG VERSION=dev
ARG REVISION=unknown
LABEL org.opencontainers.image.source="https://github.com/timgladwell/pihole-exporter" \
      org.opencontainers.image.description="Pi-hole statistics exporter for Prometheus and OpenTelemetry" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}"

COPY pihole-exporter /usr/local/bin/pihole-exporter

USER 65532:65532
EXPOSE 9617

# No shell or curl in the image, so the binary probes itself.
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
  CMD ["/usr/local/bin/pihole-exporter", "-healthcheck"]

ENTRYPOINT ["/usr/local/bin/pihole-exporter"]
