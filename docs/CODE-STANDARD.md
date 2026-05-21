# Code Standards — RunawayKillSwitch

This document extends [Effective Go](https://go.dev/doc/effective_go) with project-specific rules.
Where Effective Go and this document conflict, this document wins.

---

## Language & Toolchain

- **Go 1.22+** — use range-over-integer (`for i := range n`), `any` instead of `interface{}`.
- All compilation happens inside Docker; no local Go toolchain is required for end users.
- Module path: `github.com/runaway-killswitch/proxy-engine`

---

## Formatting

**`gofmt` is non-negotiable.** Every committed file must be `gofmt`-clean. No tabs-vs-spaces debates; gofmt decides. Run it automatically via your editor or a pre-commit hook.

**Brace placement.** Go's semicolon-insertion rules require opening braces on the same line as the control structure. This is not style preference — it is a language rule.

```go
// Correct
if err != nil {
    return err
}

// Compile error — semicolon inserted before {
if err != nil
{
    return err
}
```

**Line length.** No hard limit. Effective Go has none either. Prefer lines that fit in a terminal without horizontal scrolling, but never break a clean expression just to meet an arbitrary column count.

---

## Package Structure

| Package | Responsibility |
|---------|---------------|
| `main` (`proxy-engine/main.go`) | HTTP server setup, handler wiring, request-level orchestration, helper functions |
| `core` (`proxy-engine/core/`) | All business logic — config loading, Redis I/O, circuit breaker decisions, SSE parsing |

**Package names** (Effective Go): lowercase, single word, no underscores, no mixedCaps. The name is the base name of its source directory. Callers write `core.CircuitBreaker`, so the package name `core` is what they type — keep it short and unambiguous.

Keep `main.go` thin. Business logic that can be unit-tested without an HTTP server belongs in `core/`.

---

## Doc Comments

Every exported type, function, method, and package-level variable must have a doc comment (Effective Go: "Godoc — the program — processes Go source files to extract documentation about the package contents."). Doc comments are sentences that begin with the name of the thing being described.

```go
// CircuitBreaker evaluates each request against configured spend and loop limits
// and sets the Redis lock when a threshold is exceeded.
type CircuitBreaker struct { ... }

// PreRequestCheck runs before forwarding a request. It returns blocked=true if
// the request must be rejected, along with the human-readable reason.
func (cb *CircuitBreaker) PreRequestCheck(ctx context.Context, promptHash string) (bool, string, error) { ... }

// LoadConfig reads the YAML file at path and returns a validated Config.
// Zero-value fields are replaced with safe defaults.
func LoadConfig(path string) (*Config, error) { ... }
```

Unexported helpers do not need doc comments unless the WHY is non-obvious. When in doubt, write the comment.

**Error and panic docs.** If a function can return a sentinel error or will panic under specific conditions, document it:
```go
// NewRedisMetricsStore connects to Redis at redisURL and verifies connectivity
// with a ping. Returns an error if the URL is invalid or the ping times out.
func NewRedisMetricsStore(redisURL string, config *Config) (*RedisMetricsStore, error) { ... }
```

---

## Naming

### General rule (Effective Go)
Use `MixedCaps` or `mixedCaps`. Never use underscores in Go identifiers — that is Python style. The only underscore allowed is the blank identifier `_`.

### Exported vs unexported
Capitalization controls visibility. Export only what external packages need. In this project, `core` exports types and constructors used by `main`; internal Redis key constants stay unexported.

### Getters and setters (Effective Go)
Do not use `Get` prefix for getters. If the field is `locked`, the method is `Locked()`, not `GetLocked()`. Setters take `Set` prefix: `SetLocked()`.

```go
// Good
func (s *RedisMetricsStore) IsLocked(ctx context.Context) (bool, string, error)

// Wrong — Effective Go explicitly says no GetX for getters
func (s *RedisMetricsStore) GetIsLocked(ctx context.Context) (bool, string, error)
```

### Interface names (Effective Go)
One-method interfaces take the method name plus `-er`: `Reader`, `Writer`, `Flusher`, `Stringer`. If you introduce an interface for the metrics store, name it `MetricsStorer` or `SpendRecorder` — not `IMetricsStore` (Hungarian notation, not Go idiom).

### Naming table

| Item | Convention | Example |
|------|-----------|---------|
| Packages | lowercase, single word | `core` |
| Exported types | PascalCase | `CircuitBreaker`, `RedisMetricsStore` |
| Exported functions/methods | PascalCase | `NewCircuitBreaker`, `LoadConfig` |
| Unexported functions/methods | camelCase | `computePromptHash`, `streamAndCapture` |
| Unexported constants | camelCase | `lockKey`, `spendMinutePrefix` |
| Config struct fields | PascalCase + yaml tag | `MaxSpendPerMinuteUSD \`yaml:"max_spend_per_minute_usd"\`` |
| Acronyms | consistent case | `URL`, `SSE`, `HTTP` (exported); `url`, `sse` (unexported) |

### Acronyms
Keep acronyms consistently cased: `buildUpstreamURL` (not `buildUpstreamUrl`), `parseSSEEventTokens` (not `parseSseEventTokens`), `httpClient` (unexported field, so lowercase-first). This matches the Go standard library (`net/http`, `net/url`).

---

## Control Structures

### If with initialization (Effective Go)
Use the `if` initialization form to scope variables tightly. This is idiomatic Go — not a trick.

```go
// Good — err is scoped to the if block
if err := file.Chmod(0664); err != nil {
    return err
}

// Also good — opts scoped to the if
if opts, err := redis.ParseURL(redisURL); err != nil {
    return nil, fmt.Errorf("invalid redis URL: %w", err)
} else {
    client = redis.NewClient(opts)
}
```

### Omit else after return (Effective Go)
When an `if` body ends with `return`, `break`, `continue`, or `goto`, omit the `else`. The code after the `if` is already the else branch.

```go
// Good
if val == redis.Nil {
    return false, "", nil
}
if err != nil {
    return false, "", err
}
reason, _ = s.client.Get(ctx, lockReasonKey).Result()
return val == "1", reason, nil

// Unnecessary nesting
if val == redis.Nil {
    return false, "", nil
} else if err != nil {        // ← redundant else
    return false, "", err
} else {
    reason, _ = ...
    return val == "1", reason, nil
}
```

### Range (Effective Go)
Use `range` for iteration over slices, maps, strings, and channels. Discard unused iteration variables with `_`.

```go
// Iterate headers — discard index
for k, vs := range r.Header {
    for _, v := range vs {
        outReq.Header.Add(k, v)
    }
}

// Only need keys
for key := range req {
    // ...
}

// Only need index (range-over-integer, Go 1.22)
for i := range windowMinutes {
    keys[i] = spendMinutePrefix + now.Add(time.Duration(-i)*time.Minute).Format(...)
}
```

For strings, `range` decodes UTF-8 rune-by-rune. Use a byte-index loop (`for i := 0; i < len(s); i++`) only when you need raw bytes.

### Type switch (Effective Go)
Use type switches to discover the dynamic type of an interface value. The idiomatic form assigns to a new variable in the switch:

```go
switch v := value.(type) {
case string:
    // v is string here
case int:
    // v is int here
default:
    // v is the original interface type
}
```

For a single-type check, use a type assertion with the comma-ok idiom instead of a switch:
```go
if str, ok := value.(string); ok {
    // use str
}
```

---

## Functions

### Multiple return values (Effective Go)
Go functions can return multiple values. Use this to return both a result and an error — never encode errors in sentinel values like `-1` or `""`.

```go
// Good — result + error
func (s *RedisMetricsStore) GetSlidingWindowSpend(ctx context.Context, windowMinutes int) (float64, error)

// Good — three return values when all three are genuinely needed
func (cb *CircuitBreaker) PreRequestCheck(ctx context.Context, promptHash string) (blocked bool, reason string, err error)
```

### Named result parameters (Effective Go)
Use named results when they materially improve readability at the call site, or when a bare `return` in a longer function is cleaner than repeating variable names. Do not use them just to save typing in short functions.

```go
// Good — names document what each return value means
func (s *ProxyServer) forwardRequest(...) (inputTokens, outputTokens int64) {
    // ... long function body
    return  // bare return reads cleanly
}

// Unnecessary — caller can read the types
func add(a, b int) (result int) {  // ← named result adds no value here
    result = a + b
    return
}
```

### Defer (Effective Go)
Use `defer` to release resources at the point of acquisition. This co-locates acquisition and release, making resource leaks obvious on code review.

```go
resp, err := httpClient.Do(outReq)
if err != nil {
    http.Error(w, ..., http.StatusBadGateway)
    return
}
defer resp.Body.Close()  // ← guaranteed to run even if handler returns early
```

Deferred calls execute in LIFO order. When deferring in a loop (e.g., closing multiple files), use a closure or a named function to avoid the common "defer inside loop" mistake:

```go
// Wrong — all defers run after the loop, holding all handles open
for _, f := range files {
    defer f.Close()
}

// Right — close after each iteration
for _, f := range files {
    func() {
        defer f.Close()
        process(f)
    }()
}
```

---

## Data

### Zero values (Effective Go)
Design structs so their zero value is useful. A `sync.Mutex` is an unlocked mutex at zero value — callers don't need to call `Init()`. A `bytes.Buffer` is ready to use. Follow the same principle: `Config` fields with zero values should trigger safe defaults (implemented in `LoadConfig`).

### `make` vs `new` (Effective Go)
- `new(T)` allocates zeroed storage and returns `*T`. Use for structs that need a pointer.
- `make(T, args)` creates slices, maps, and channels — it returns an initialized (not just zeroed) value. You cannot use `new` for these.

```go
// Slice — must use make
keys := make([]string, windowMinutes)

// Map — must use make
models := make(map[string]ModelPricing)

// Struct pointer — new or composite literal
cfg := new(Config)    // zeroed
cfg := &Config{}      // zeroed, equivalent
cfg := &Config{       // initialized
    Limits: LimitsConfig{MaxSpendPerMinuteUSD: 1.50},
}
```

### Composite literals (Effective Go)
Initialize structs with field names for clarity and forward-compatibility. Positional literals break when fields are added.

```go
// Good — survives field additions
return &CircuitBreaker{
    store:  store,
    config: config,
}

// Fragile — breaks on any field addition
return &CircuitBreaker{store, config}
```

For slices and maps, prefer composite literals over repeated `append`/`assign` calls when the full contents are known:

```go
payload := map[string]any{
    "content": fmt.Sprintf("🚨 RunawayKillSwitch: %s", reason),
}
```

### Maps — comma-ok idiom (Effective Go)
Always use the two-value form when testing for key presence. The single-value form returns the zero value for missing keys, which is indistinguishable from a stored zero value.

```go
// Good — distinguishes "missing" from "zero"
if pricing, ok := c.PricingMatrix.Models[model]; ok {
    return pricing
}
return ModelPricing{...defaults...}

// Unreliable — zero ModelPricing looks the same as a missing key
pricing := c.PricingMatrix.Models[model]
```

### Append (Effective Go)
Use `append` for growing slices. To append one slice to another, use the `...` spread operator:

```go
keys = append(keys, moreKeys...)
```

---

## Methods

### Pointer vs value receivers (Effective Go)
The rule: if any method on a type needs a pointer receiver, all methods should use pointer receivers for consistency.

Use a **pointer receiver** when:
- The method modifies the receiver (e.g., `TriggerCircuitBreaker` sets Redis keys and conceptually mutates state).
- The receiver is a large struct (copying is expensive).
- Consistency with other methods on the type demands it.

Use a **value receiver** when:
- The method does not modify the receiver.
- The receiver is a small, naturally copied type (e.g., a simple config value struct).

In this codebase, `RedisMetricsStore`, `CircuitBreaker`, and `ProxyServer` always use pointer receivers because they hold mutable state (Redis client, config) and are allocated once.

```go
// All methods on RedisMetricsStore use pointer receivers
func (s *RedisMetricsStore) IsLocked(ctx context.Context) (bool, string, error) { ... }
func (s *RedisMetricsStore) TriggerCircuitBreaker(ctx context.Context, reason string) error { ... }
func (s *RedisMetricsStore) RecordSpend(...) (float64, error) { ... }
```

---

## Interfaces

### Keep interfaces small (Effective Go)
"The bigger the interface, the weaker the abstraction." One or two methods is the Go ideal. If you need to mock `RedisMetricsStore` in tests, extract only the methods the test caller needs:

```go
// Good — narrow interface for testing PreRequestCheck
type promptChecker interface {
    IsLocked(ctx context.Context) (bool, string, error)
    CheckAndPushPromptHash(ctx context.Context, hash string, max int) (bool, error)
    TriggerCircuitBreaker(ctx context.Context, reason string) error
}
```

### Export interfaces, not concrete types (Effective Go)
When a package boundary is crossed, accept interfaces and return concrete types. This keeps callers decoupled from implementation details without forcing them to import the concrete type.

### Interface satisfaction check (Effective Go)
To verify at compile time that a type implements an interface, add a blank-identifier assignment:

```go
var _ io.Flusher = (*http.ResponseWriter)(nil)   // stdlib example
var _ MetricsStorer = (*RedisMetricsStore)(nil)   // verify at compile time
```

This line compiles only if `*RedisMetricsStore` satisfies `MetricsStorer`. It produces no runtime overhead.

---

## The Blank Identifier (Effective Go)

Use `_` to discard values you explicitly do not need. This is documentation — it tells the reader "this value is intentionally ignored."

```go
// Discard one of multiple return values
reason, _ = s.client.Get(ctx, lockReasonKey).Result()

// Discard loop index
for _, v := range vals {
    totalMicro += n
}

// Import for side effects only
import _ "net/http/pprof"

// Compile-time interface check (no runtime cost)
var _ io.Writer = (*bytes.Buffer)(nil)
```

Do not use `_` to silence errors. A discarded error is a hidden bug. The only acceptable cases are:
- The function is documented to never fail for the given input.
- The return value is truly irrelevant *and* you have explicitly reasoned about it.

---

## Error Handling

### Error strings (Effective Go)
Error strings must be lowercase and not end with punctuation. They are often embedded inside larger messages via `fmt.Errorf("outer: %w", err)`, and a capital letter or trailing period looks wrong mid-sentence.

```go
// Good
return fmt.Errorf("invalid redis URL: %w", err)
return fmt.Errorf("lock check: %w", err)

// Bad — capital letter and period break embedding
return fmt.Errorf("Invalid Redis URL: %w", err)
return fmt.Errorf("Lock check failed. %w", err)
```

### Wrapping errors at boundaries
Wrap errors with `fmt.Errorf("context: %w", err)` every time a call crosses a package or abstraction boundary. This builds a chain that slog and callers can unwrap.

```go
if err := client.Ping(ctx).Err(); err != nil {
    return nil, fmt.Errorf("redis ping failed: %w", err)
}
```

### Fail open on infrastructure errors
Distinguish **infrastructure errors** (Redis timeout, parse failure) from **business decisions** (breaker tripped, loop detected). Infrastructure errors log a warning and allow the request through — a flaky Redis connection should not take down proxy functionality. Business decisions block the request.

```go
blocked, reason, err := s.breaker.PreRequestCheck(ctx, promptHash)
if err != nil {
    slog.Error("pre-request check failed, failing open", "error", err)
    // fall through — do not block the request
}
if blocked {
    writeBlockedResponse(w, reason)
    return
}
```

### Panic (Effective Go)
`panic` is for situations that should never happen — a programming error, not a runtime condition. In this proxy, valid uses of `panic` are essentially none in the hot path. Use it only at startup when the program cannot meaningfully continue (missing config, corrupted binary). Even then, `log.Fatalf` is clearer and more conventional at the `main` level.

```go
// Fine — startup, unrecoverable
log.Fatalf("Failed to load config from %s: %v", configPath, err)

// Wrong — HTTP handler receiving a bad request is not a panic situation
func (s *ProxyServer) handleAnthropic(w http.ResponseWriter, r *http.Request) {
    panic("bad request")  // ← never do this; return an HTTP error
}
```

### Recover (Effective Go)
`recover` is appropriate in top-level goroutine wrappers to prevent a panicking background goroutine from crashing the whole process. Wrap background goroutines defensively:

```go
go func() {
    defer func() {
        if r := recover(); r != nil {
            slog.Error("webhook goroutine panicked", "recover", r)
        }
    }()
    s.fireWebhook(reason)
}()
```

---

## Logging

Use `log/slog` (standard library, Go 1.21+) for all structured operational logs. Never use `fmt.Println`, `fmt.Printf`, or the `log` package for operational messages (`log.Fatalf` at startup is the only exception).

```go
import "log/slog"

// Startup only — log.Fatalf is fine
log.Fatalf("Failed to load config: %v", err)

// Operational — slog with structured key-value fields
slog.Info("request intercepted", "model", model, "hash", promptHash[:8])
slog.Warn("redis pipeline partial failure", "error", err)
slog.Error("upstream request failed", "url", targetURL, "status", resp.StatusCode)
slog.Info("circuit breaker triggered", "reason", triggerReason)
```

Log format: default text handler (human-readable in `docker compose logs`). Do not switch to a JSON handler unless a log aggregation pipeline (e.g., Loki, Datadog) is introduced.

**Never log API key values.** Header values from `Authorization`, `x-api-key`, and similar must never appear in any log output.

---

## Context Propagation

Every Redis call and every outbound HTTP call must receive the request's context (`r.Context()`). Never use `context.Background()` in the hot path — this discards the client's cancellation signal and deadline.

```go
// Good — cancellation propagates from the client request
blocked, reason, err := s.breaker.PreRequestCheck(r.Context(), promptHash)

// Bad — client disconnect or timeout not honored
blocked, reason, err := s.breaker.PreRequestCheck(context.Background(), promptHash)
```

Use `context.WithTimeout` only when the caller's context does not already carry an appropriate deadline — for example, the Redis ping at startup, which has no inbound request context:

```go
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()
if err := client.Ping(ctx).Err(); err != nil { ... }
```

---

## Concurrency

### Core principle (Effective Go)
"Do not communicate by sharing memory; instead, share memory by communicating."

In RunawayKillSwitch, all shared mutable state lives in Redis — not in in-process variables. There are no mutexes, no `sync.Map`, no global variables. This is the correct design: Redis is the single source of truth for lock state and spend counters, and atomicity is provided by Redis pipelines and `INCRBY`.

### Goroutines
The proxy uses goroutines in exactly two places:

1. **Webhook dispatch** — `go s.fireWebhook(reason)` fires without blocking the response path. Wrap it with a recover (see Error Handling §Recover).
2. **Admin server** — `go http.ListenAndServe(":8531", adminMux)` runs the admin UI concurrently with the proxy.

Do not introduce shared in-process state (no global maps, no `sync.Mutex`-protected caches) without a compelling reason and a documented justification.

### Buffered channels as semaphores (Effective Go)
If future work needs to limit concurrent upstream connections, use a buffered channel as a semaphore:

```go
var sem = make(chan struct{}, maxConcurrentUpstream)

func (s *ProxyServer) forwardRequest(...) {
    sem <- struct{}{}        // acquire
    defer func() { <-sem }() // release
    // ... forward
}
```

This is more idiomatic than `sync.Mutex`-based connection counting.

---

## HTTP Handlers

- Check `r.Method` first. Return `http.StatusMethodNotAllowed` for wrong methods.
- Read the full body with `io.ReadAll`, then close `r.Body`. Do not stream-parse the request body.
- Copy upstream headers by iterating `r.Header` — skip `Host` and `Content-Length` (Go's `http.Client` rebuilds these).
- Set `outReq.ContentLength` explicitly after body modification (e.g., after `injectStreamOptions`).
- Never set `Transfer-Encoding: chunked` manually — Go handles this automatically.

---

## Streaming (SSE)

- Use `bufio.NewReaderSize(body, 4096)` and read line-by-line with `ReadBytes('\n')`.
- Write each line to `w` and flush before processing — client must receive data in real time.
- Accumulate `inputTokens` and `outputTokens` with max-of-latest semantics: `if in > 0 { inputTokens = in }`. This handles providers that send partial counts in multiple events.
- Only parse `data:` lines; ignore `event:`, `id:`, comment lines (`:`) for token extraction.

---

## Redis Operations

- All spend counters use `int64` microdollars (cost × 1 000 000). Never store floats in Redis counters — `INCRBY` requires integers; floating-point `INCRBYFLOAT` has rounding accumulation issues over millions of operations.
- Batch related operations into a single pipeline (`s.client.Pipeline()`). Exec the pipeline and check the returned error, not individual command errors.
- Key naming: `namespace:entity:qualifier` — e.g., `spend:minute:202605211430`, `agent_state:locked`.
- Set TTL on all time-bucketed keys at write time (minute buckets: 10min, hour buckets: 2hr). Keys without TTL must be explicitly documented with a reason.

---

## Code Comments

Default to **no comments**. Only add a comment when:
- The WHY is non-obvious (a hidden constraint, a provider-specific quirk, a workaround for known upstream behavior)
- A subtle invariant would surprise a future reader

```go
// Good — explains a non-obvious provider behavior
// Inject stream_options so OpenAI returns token counts in the final SSE chunk.
// Without this, streaming responses carry no usage data.
req["stream_options"] = json.RawMessage(`{"include_usage":true}`)

// Bad — narrates what the code obviously does
// Read all bytes from the body
bodyBytes, err := io.ReadAll(r.Body)
```

Do not write multi-line `/* */` comment blocks inside functions. One short `//` line maximum for inline explanation. Package-level doc comments (above `package` declarations) may use block comments.

---

## Import Organization

Three groups, separated by blank lines, sorted alphabetically within each group. `goimports` or `gofmt` with your editor handles this automatically.

```go
import (
    // 1. Standard library
    "bufio"
    "context"
    "encoding/json"
    "fmt"
    "io"

    // 2. Third-party
    "github.com/redis/go-redis/v9"
    "gopkg.in/yaml.v3"

    // 3. Internal packages
    "github.com/runaway-killswitch/proxy-engine/core"
)
```

---

## Git Workflow

Conventional Commits: `type(scope): message`

Types: `feat`, `fix`, `refactor`, `test`, `docs`, `chore`, `perf`

Examples:
```
feat(proxy): add OpenRouter routing support
fix(streaming): handle partial SSE chunks across buffer boundaries
perf(redis): batch prompt hash check and push into single pipeline
docs(architecture): document Redis key TTL policy
chore(deps): upgrade go-redis to v9.6.0
```

Branch naming: `feature/description`, `fix/description`, `chore/description`

---

## Dependency Policy

Keep dependencies minimal. Current allowed third-party dependencies:

| Module | Purpose | Justification |
|--------|---------|---------------|
| `github.com/redis/go-redis/v9` | Redis client | No mature stdlib alternative |
| `gopkg.in/yaml.v3` | Config parsing | YAML is not in the stdlib |

Do not add new third-party dependencies without documenting the justification here. Prefer stdlib solutions (`log/slog` over zerolog, `encoding/json` over sonic unless benchmarks show a measurable bottleneck on the hot path).

---

## Security

- **No API key logging** — never log or expose values from `Authorization`, `x-api-key`, or similar headers.
- **No SSRF** — upstream URLs are constructed from config values, not from user-supplied request headers. Never forward a user-controlled `X-Forwarded-Host` or similar as the upstream target.
- **No body size DoS** — use `http.MaxBytesReader` for request bodies to prevent unbounded memory allocation from oversized payloads.
- **Input validation** — validate config values at load time (`LoadConfig`); fail fast with a clear error if required fields are missing or out of range.
- **CORS** — admin API sets `Access-Control-Allow-Origin: *` intentionally (local tool, no sensitive data exposed). Do not expand this pattern to the proxy port (8530).

---

## Docker

- Use multi-stage builds: `golang:1.22-alpine` (builder) → `alpine:3.19` (runtime).
- Build flags: `CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w"` for a statically linked, stripped binary.
- The runtime image must include `ca-certificates` (for TLS to api.anthropic.com etc.) and `tzdata`.
- Do not `COPY` source code into the runtime image — only the compiled binary.
- Do not run the container as root in production configurations. (Current bootstrap uses default; add `USER nonroot:nonroot` when hardening.)
