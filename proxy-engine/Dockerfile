FROM golang:1.22-alpine AS builder

WORKDIR /app

# git required for go module downloads from VCS
RUN apk add --no-cache git ca-certificates

# Copy go.mod first. go.sum may not exist yet — go mod tidy generates it.
COPY go.mod ./
COPY . .

# go mod tidy downloads all dependencies and generates go.sum if missing
RUN go mod tidy

# Build a statically linked binary with debug symbols stripped
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o killswitch-proxy .

# --- Final image ---
FROM alpine:3.19

# ca-certificates required for TLS connections to api.anthropic.com etc.
RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app
COPY --from=builder /app/killswitch-proxy .

EXPOSE 8530 8531

CMD ["./killswitch-proxy"]
