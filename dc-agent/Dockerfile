# ── Stage 1: Builder ──────────────────────────────────────────────────────────
# We use the full Go image to compile. The output is a single static binary.
FROM golang:1.26-alpine AS builder

# VERSION is reported to the control plane in the hello frame.
ARG VERSION=docker

WORKDIR /src

# Copy dependency files first. Docker caches this layer separately —
# if only code changes (not go.mod/go.sum), the `go mod download` step
# is skipped on the next build, saving significant time.
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest of the source and build.
# CGO_ENABLED=0: produce a fully static binary (no libc dependency).
# This is required for distroless/scratch base images.
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w -X main.version=${VERSION}" -o /dc-agent .

# ── Stage 2: Runtime ──────────────────────────────────────────────────────────
# distroless/static contains only CA certificates and timezone data — nothing else.
# No shell, no package manager, no attack surface. The binary runs as uid 65532
# (nonroot). The CA bundle matters here: the agent dials the control plane over
# WSS and must verify its TLS certificate.
FROM gcr.io/distroless/static:nonroot

# Copy only the compiled binary from the builder stage.
COPY --from=builder /dc-agent /dc-agent

# No EXPOSE: dc-agent listens on nothing. It only dials OUT to the control
# plane (DCAGENT_ENDPOINT, port 443) — that is the whole point of the design.

ENTRYPOINT ["/dc-agent"]
