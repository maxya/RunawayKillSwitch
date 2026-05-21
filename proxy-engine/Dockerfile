FROM golang:1.22-alpine AS builder

WORKDIR /app

# git required for go module downloads from VCS
RUN apk add --no-cache git ca-certificates

COPY go.mod go.sum ./
RUN go mod download -x

COPY . .

# Build a statically linked binary with debug symbols stripped
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o killswitch-proxy .

# --- Final image ---
FROM alpine:3.19

# ca-certificates required for TLS connections to api.anthropic.com etc.
RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app
COPY --from=builder /app/killswitch-proxy .

EXPOSE 8530 8531

HEALTHCHECK --interval=15s --timeout=5s --start-period=10s --retries=3 \
    CMD wget -qO- http://localhost:8531/api/status > /dev/null || exit 1

CMD ["./killswitch-proxy"]
