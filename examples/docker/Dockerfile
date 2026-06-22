# Multi-stage pure-Go build — CGO_ENABLED=0, static binary, distroless final image.
# Result: ~20 MB image with no shell, no package manager, and minimal attack surface.
#
# Build:
#   docker build -t miucr-server -f examples/docker/Dockerfile .
#
# Run (webhook mode):
#   docker run --rm \
#     -p 8080:8080 \
#     -e WEBHOOK_SECRET="$(openssl rand -hex 32)" \
#     -e GITHUB_TOKEN="ghp_..." \
#     -e ANTHROPIC_API_KEY="sk-ant-..." \
#     miucr-server serve \
#       --repos owner/repo \
#       --addr :8080 \
#       --gate high
#
# Run (poll-only, no webhook secret required):
#   docker run --rm \
#     -e GITHUB_TOKEN="ghp_..." \
#     -e ANTHROPIC_API_KEY="sk-ant-..." \
#     miucr-server serve \
#       --repos owner/repo \
#       --poll \
#       --poll-interval 60s \
#       --gate high

# ── Stage 1: build ───────────────────────────────────────────────────────────
FROM golang:1.25-bookworm AS build
ARG TARGETARCH

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} \
    go build \
      -trimpath \
      -ldflags="-s -w" \
      -o /out/miucr \
      ./cmd/miucr

# ── Stage 2: run ─────────────────────────────────────────────────────────────
# gcr.io/distroless/static-debian12 includes CA certs and timezone data but
# no shell, no apt, no package manager. The binary must be fully static (CGO=0).
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/miucr /usr/local/bin/miucr

# Expose the default webhook port; override with --addr at runtime.
EXPOSE 8080

# Run as the non-root user provided by the distroless image (uid 65532).
USER nonroot:nonroot

ENTRYPOINT ["/usr/local/bin/miucr"]
