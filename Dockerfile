# ── Stage 1: Build ───────────────────────────────────────────────────────────
FROM golang:1.22-alpine AS builder

WORKDIR /src

# Cache dependencies first.
COPY go.mod go.sum ./
RUN go mod download

# Copy source and build a static binary.
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /ue-crash-reporter ./cmd/server

# ── Stage 2: Runtime ──────────────────────────────────────────────────────────
FROM alpine:3.19

# ca-certs for any outbound TLS (alerts/webhooks), tini for signal forwarding.
RUN apk add --no-cache ca-certificates tini

WORKDIR /app

COPY --from=builder /ue-crash-reporter .

# Data directory for SQLite DB and uploaded crash files.
VOLUME ["/data"]

ENV ADDR=":8080" \
    DATA_DIR="/data" \
    DB_PATH="/data/crashes.db"

EXPOSE 8080

ENTRYPOINT ["/sbin/tini", "--"]
CMD ["/app/ue-crash-reporter"]
