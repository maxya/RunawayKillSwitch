# Tests — RunawayKillSwitch

## Test Strategy

RunawayKillSwitch has two test layers:

| Layer | Tool | What it covers |
|-------|------|---------------|
| **Unit** | `go test` (stdlib) | Pure functions: SSE parsing, prompt hashing, model routing, spend math, config loading |
| **Integration** | `go test` + `testcontainers-go` | Redis-backed `RedisMetricsStore` and `CircuitBreaker` against a real Redis instance |

No mocking of Redis. Integration tests spin up a real `redis:7.2-alpine` container via testcontainers and tear it down after the test run. This matches production behavior and prevents mock/reality divergence.

End-to-end (full HTTP proxy) tests are performed manually using the verification steps in `bootstrap.md §Step 14`. Automated E2E tests against a live proxy are future work.

## Running Tests

```bash
# All unit tests (no Docker required)
cd proxy-engine && go test ./core/... -v -run "^Test"

# All unit tests with race detector
cd proxy-engine && go test -race ./core/... -v

# Integration tests (requires Docker; spins up Redis container automatically)
cd proxy-engine && go test -tags integration ./core/... -v -timeout 60s

# Single test function
cd proxy-engine && go test ./core/... -run TestParseSSEEventTokens -v

# All tests with coverage report
cd proxy-engine && go test -coverprofile=coverage.out ./... && go tool cover -html=coverage.out

# Lint (requires golangci-lint)
golangci-lint run ./...

# Vet only (no external tools)
cd proxy-engine && go vet ./...
```

## Test File Layout

```
proxy-engine/
├── core/
│   ├── config_test.go          # LoadConfig, GetModelPricing
│   ├── streaming_test.go       # ParseSSEEventTokens, ParseNonStreamingTokens
│   ├── metrics_test.go         # RedisMetricsStore (integration, +build integration)
│   └── breaker_test.go         # CircuitBreaker (integration, +build integration)
└── main_test.go                # computePromptHash, extractModel, injectStreamOptions, resolveOpenAITarget
```

Unit test files have no build tag. Integration test files use `//go:build integration`.

## Writing Unit Tests

Use the standard library `testing` package. Table-driven tests for any function with multiple input cases.

```go
func TestParseSSEEventTokens(t *testing.T) {
    tests := []struct {
        name         string
        data         string
        provider     string
        wantInput    int64
        wantOutput   int64
    }{
        {
            name:       "anthropic message_start",
            provider:   "anthropic",
            data:       `{"type":"message_start","message":{"usage":{"input_tokens":42}}}`,
            wantInput:  42,
            wantOutput: 0,
        },
        {
            name:       "anthropic message_delta",
            provider:   "anthropic",
            data:       `{"type":"message_delta","usage":{"output_tokens":17}}`,
            wantInput:  0,
            wantOutput: 17,
        },
        {
            name:       "openai final chunk with usage",
            provider:   "openai",
            data:       `{"usage":{"prompt_tokens":100,"completion_tokens":50}}`,
            wantInput:  100,
            wantOutput: 50,
        },
        {
            name:       "openai partial chunk no usage",
            provider:   "openai",
            data:       `{"choices":[{"delta":{"content":"hello"}}]}`,
            wantInput:  0,
            wantOutput: 0,
        },
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            in, out := ParseSSEEventTokens([]byte(tt.data), tt.provider)
            if in != tt.wantInput || out != tt.wantOutput {
                t.Errorf("got (%d, %d), want (%d, %d)", in, out, tt.wantInput, tt.wantOutput)
            }
        })
    }
}
```

## Writing Integration Tests

Use `//go:build integration` at the top of the file. Use testcontainers-go to start Redis.

```go
//go:build integration

package core_test

import (
    "context"
    "testing"

    "github.com/testcontainers/testcontainers-go"
    "github.com/testcontainers/testcontainers-go/wait"
)

func startRedis(t *testing.T) (redisURL string, cleanup func()) {
    t.Helper()
    ctx := context.Background()
    req := testcontainers.ContainerRequest{
        Image:        "redis:7.2-alpine",
        ExposedPorts: []string{"6379/tcp"},
        WaitingFor:   wait.ForLog("Ready to accept connections"),
    }
    c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
        ContainerRequest: req,
        Started:          true,
    })
    if err != nil {
        t.Fatalf("start redis: %v", err)
    }
    host, _ := c.Host(ctx)
    port, _ := c.MappedPort(ctx, "6379")
    return "redis://" + host + ":" + port.Port() + "/0", func() { c.Terminate(ctx) }
}

func TestCircuitBreakerTripsOnVelocity(t *testing.T) {
    redisURL, cleanup := startRedis(t)
    defer cleanup()

    cfg := &Config{
        Limits: LimitsConfig{MaxSpendPerMinuteUSD: 0.001},
        PricingMatrix: PricingMatrixConfig{
            DefaultInputCostPerM:  3.0,
            DefaultOutputCostPerM: 15.0,
        },
    }
    store, err := NewRedisMetricsStore(redisURL, cfg)
    if err != nil {
        t.Fatal(err)
    }
    breaker := NewCircuitBreaker(store, cfg)

    ctx := context.Background()
    // First call — should not trip
    triggered, _, err := breaker.PostResponseRecord(ctx, "unknown", 1000, 500)
    if err != nil {
        t.Fatal(err)
    }
    // Subsequent call — accumulated spend should exceed $0.001/min
    triggered, reason, err := breaker.PostResponseRecord(ctx, "unknown", 10000, 5000)
    if err != nil {
        t.Fatal(err)
    }
    if !triggered {
        t.Error("expected circuit breaker to trip on velocity limit")
    }
    if reason == "" {
        t.Error("expected non-empty trip reason")
    }
}
```

## What to Test

### Unit test priority (pure functions, no external deps)

| Function | Test cases |
|----------|-----------|
| `ParseSSEEventTokens` | Each Anthropic event type; each OpenAI event type; malformed JSON; unknown provider |
| `ParseNonStreamingTokens` | Anthropic format; OpenAI format; empty body; malformed JSON |
| `computePromptHash` | Same messages → same hash; different messages → different hash; missing messages field; empty body |
| `extractModel` | Present model field; missing field; empty string |
| `injectStreamOptions` | Streaming request → options injected; non-streaming → unchanged; malformed body → unchanged |
| `resolveOpenAITarget` | deepseek prefix; gpt prefix; o1/o3 prefix; unknown model → default; config provider prefix |
| `LoadConfig` | Valid YAML; missing file; malformed YAML; defaults applied when fields are zero |
| `GetModelPricing` | Known model; unknown model → defaults |

### Integration test priority (requires Redis)

| Scenario | Expected outcome |
|----------|-----------------|
| `IsLocked` on fresh Redis | Returns `false, "", nil` |
| `TriggerCircuitBreaker` then `IsLocked` | Returns `true, reason, nil` |
| `ResetCircuitBreaker` clears lock and history | Returns `false, "", nil` after reset |
| `CheckAndPushPromptHash` — N-1 identical | Returns `false` |
| `CheckAndPushPromptHash` — N identical | Returns `true` |
| `CheckAndPushPromptHash` — N identical then different | Returns `false` |
| `RecordSpend` accumulates across calls | `GetSlidingWindowSpend(ctx, 1)` returns sum |
| Velocity limit trip | `PostResponseRecord` returns `triggered=true` when minute spend exceeds limit |
| Hourly limit trip | `PostResponseRecord` returns `triggered=true` when hour spend exceeds limit |

## Test Data Conventions

- Use minimal, deterministic inputs. Real provider JSON payloads should be inline string literals, not loaded from files.
- For spend/limit tests, use very small limits (e.g., `$0.001/min`) and large token counts so the test trips quickly without timing dependency.
- Do not use `time.Sleep` in tests. If a test requires time-bucketing behavior, mock the clock or test at the bucket boundary using a real timestamp.

## Linting

Use `golangci-lint` with at minimum these linters enabled:

```yaml
# .golangci.yml (create when adding CI)
linters:
  enable:
    - errcheck       # all errors must be handled
    - govet          # go vet checks
    - staticcheck    # SA* checks
    - gosimple       # simplification suggestions
    - unused         # unused code detection
    - gofmt          # formatting
    - gosec          # security checks (G*): log injection, SSRF, etc.
```

Run before committing:
```bash
golangci-lint run ./...
go vet ./...
```

## CI Pipeline (Future)

When a CI workflow is added, tests must run in this order:

1. `go vet ./...`
2. `golangci-lint run ./...`
3. `go test -race ./core/...` (unit, no Docker needed)
4. `go test -tags integration -race ./core/... -timeout 120s` (integration, Docker-in-Docker)
5. `docker compose build` (verifies the full build still works)

All steps must pass for the pipeline to succeed.

## Manual Verification Checklist

Before declaring a feature complete, run through these checks against a live stack:

```bash
# Stack is healthy
docker compose ps
docker exec killswitch-db redis-cli ping          # → PONG
curl -s http://localhost:8531/api/status | python3 -m json.tool

# Dashboard loads with green status
open http://localhost:8531

# Proxy passes real traffic (replace with a real key)
curl -s http://localhost:8530/v1/messages \
  -H "x-api-key: $ANTHROPIC_API_KEY" \
  -H "anthropic-version: 2023-06-01" \
  -H "content-type: application/json" \
  -d '{"model":"claude-haiku-4-5","max_tokens":10,"messages":[{"role":"user","content":"Say: ok"}]}'

# Request count incremented
curl -s http://localhost:8531/api/status | python3 -c "import sys,json; d=json.load(sys.stdin); print('requests:', d['request_count'])"

# Manual lock test
docker exec killswitch-db redis-cli SET agent_state:locked 1
docker exec killswitch-db redis-cli SET agent_state:lock_reason "Test lock"
curl -s http://localhost:8530/v1/messages \
  -H "x-api-key: dummy" -H "content-type: application/json" \
  -d '{"model":"claude-haiku-4-5","messages":[{"role":"user","content":"hi"}]}'
# → HTTP 402 with circuit_breaker_active error

# Reset
curl -X POST http://localhost:8531/api/reset
# → {"status":"reset","message":"Circuit breaker reset successfully"}
```
