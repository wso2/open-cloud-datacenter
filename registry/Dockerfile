FROM golang:1.26-alpine AS builder

WORKDIR /app

RUN apk add --no-cache git ca-certificates

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-w -s" -o /registry-provisioner ./cmd/main.go

# --- Runtime image ---
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /registry-provisioner /registry-provisioner
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# 8443 = HTTPS metrics, 8081 = health probes
EXPOSE 8443 8081

ENTRYPOINT ["/registry-provisioner"]
