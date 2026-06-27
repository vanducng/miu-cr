# Multi-stage pure-Go build — CGO_ENABLED=0, static binary, minimal runtime image.
# The runtime image includes git because PR review needs local git operations.
#
# Build:
#   docker build -t miucr-server -f examples/docker/Dockerfile .
#
# Run (webhook mode):
#   docker run --rm \
#     -p 8080:8080 \
#     -e WEBHOOK_SECRET="$(openssl rand -hex 32)" \
#     -e GITHUB_TOKEN="your-github-token" \
#     -e ANTHROPIC_API_KEY="your-provider-key" \
#     miucr-server serve \
#       --repos owner/repo \
#       --addr :8080 \
#       --gate high
#
# Run (poll-only, no webhook secret required):
#   docker run --rm \
#     -e GITHUB_TOKEN="your-github-token" \
#     -e ANTHROPIC_API_KEY="your-provider-key" \
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
FROM alpine:3.22

RUN apk add --no-cache ca-certificates git openssh-client \
    && addgroup -S -g 65532 nonroot \
    && adduser -S -D -H -u 65532 -G nonroot -h /home/nonroot -s /sbin/nologin nonroot \
    && mkdir -p /home/nonroot \
    && chown 65532:65532 /home/nonroot

COPY --from=build /out/miucr /usr/local/bin/miucr

# Expose the default webhook port; override with --addr at runtime.
EXPOSE 8080

ENV HOME=/home/nonroot
USER 65532:65532

ENTRYPOINT ["/usr/local/bin/miucr"]
