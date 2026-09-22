# syntax=docker/dockerfile:1
#
# Linux image for llm-metrics-exporter. On macOS run the binary natively
# (packaging/launchd): a container there runs in a VM and cannot see the
# host's engines on 127.0.0.1.
#
#   docker build --build-arg REVISION=$(git rev-parse --short HEAD) -t llm-metrics-exporter .

FROM cgr.dev/chainguard/go:latest AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
ARG REVISION=unknown
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X github.com/evanwtf/llm-metrics-exporter/internal/version.Revision=${REVISION}" \
      -o /out/llm-metrics-exporter ./cmd/llm-metrics-exporter \
 && mkdir -p /out/registrations

# Static and shell-less. CI runs this exact stage, because a shell-less image
# can build cleanly and still never boot.
FROM cgr.dev/chainguard/static:latest
COPY --from=build /out/llm-metrics-exporter /usr/bin/llm-metrics-exporter
# The mount point exists and belongs to nonroot before the USER switch, so an
# unmounted volume is still readable.
COPY --from=build --chown=nonroot:nonroot /out/registrations /registrations
VOLUME ["/registrations"]
USER nonroot
EXPOSE 9109
# No shell or curl in the image: the binary checks itself.
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
  CMD ["/usr/bin/llm-metrics-exporter", "health"]
ENTRYPOINT ["/usr/bin/llm-metrics-exporter"]
CMD ["serve", "--registration-dir", "/registrations"]
