FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS builder

WORKDIR /app

RUN apk add --no-cache git ca-certificates

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Supplied by BuildKit and describing the image being produced, not the machine
# producing it. Hardcoding an architecture here builds a binary the runtime image
# may not be able to execute at all.
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -ldflags="-w -s" -o /registry-provisioner ./cmd/main.go

# --- Runtime image ---
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /registry-provisioner /registry-provisioner
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# 8443 = HTTPS metrics, 8081 = health probes
EXPOSE 8443 8081

ENTRYPOINT ["/registry-provisioner"]
