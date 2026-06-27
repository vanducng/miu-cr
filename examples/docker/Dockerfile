# Multi-stage pure-Go build — CGO_ENABLED=0, static binary, slim runtime image.
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
FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates git openssh-client \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --system --gid 65532 nonroot \
    && useradd --system --uid 65532 --gid 65532 --create-home --home-dir /home/nonroot --shell /usr/sbin/nologin nonroot

COPY --from=build /out/miucr /usr/local/bin/miucr

# Expose the default webhook port; override with --addr at runtime.
EXPOSE 8080

ENV HOME=/home/nonroot
USER 65532:65532

ENTRYPOINT ["/usr/local/bin/miucr"]
