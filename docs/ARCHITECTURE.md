# Architecture — RunawayKillSwitch

## Overview

RunawayKillSwitch is a network-transparent reverse proxy that sits between an autonomous AI agent and its cloud LLM provider. It intercepts every request and response, tracks token spend velocity in Redis, and trips a circuit breaker the moment a runaway loop is detected. Zero changes are required in the agent's application code — the agent simply points its `BASE_URL` environment variable at the proxy.

## Stack

| Component | Image / Language | Role |
|-----------|-----------------|------|
| `proxy-engine` | Go 1.22, Alpine | Reverse proxy, circuit breaker logic, admin UI |
| `state-db` | Redis 7.2 Alpine | Spend counters, lock state, prompt hash history |

No external services, no cloud dependencies, no persistent volumes beyond in-memory Redis state.

## Directory Layout

```
runaway-killswitch/
├── docker-compose.yml              # Service definitions
├── config/
│   └── killswitch.yaml             # Live config (bind-mounted, no rebuild on change)
└── proxy-engine/
    ├── Dockerfile                  # Multi-stage: golang:1.22-alpine → alpine:3.19
    ├── go.mod
    ├── main.go                     # HTTP servers, handler wiring, helper functions
    └── core/
        ├── config.go               # Config structs + YAML loader
        ├── metrics.go              # RedisMetricsStore — all Redis I/O
        ├── breaker.go              # CircuitBreaker — pre/post request checks
        └── streaming.go            # SSE and non-streaming token extraction
    └── ui/
        └── embedded_dashboard.html # Go embed — served at port 8531
```

## Network Topology

```
┌─────────────────────────────────────────────────────────────┐
│  Developer Machine                                          │
│                                                             │
│  Agent process                                              │
│  ANTHROPIC_BASE_URL=http://localhost:8530                   │
│         │                                                   │
│         ▼                                                   │
│  ┌──────────────────────────────────────────────────────┐   │
│  │  DOCKER COMPOSE NETWORK (killswitch-net)             │   │
│  │                                                      │   │
│  │  proxy-engine :8530 (AI proxy)                       │   │
│  │  proxy-engine :8531 (admin UI)  ◄── browser          │   │
│  │         │                                            │   │
│  │         └──redis──► state-db :6379                   │   │
│  └──────────────────────────────────────────────────────┘   │
│         │                                                   │
│         ▼ (only when lock == false)                         │
│  api.anthropic.com / api.openai.com / api.deepseek.com      │
└─────────────────────────────────────────────────────────────┘
```

## Five-Stage Interception Lifecycle

Every inbound AI request passes through five stages in order. A failure at any stage short-circuits the remaining stages.

### Stage 1 — Lock Check

- Redis key `agent_state:locked` is read.
- If `"1"`: return HTTP 402 immediately with a JSON error body containing the lock reason. The upstream provider is never contacted.
- Cost: one Redis `GET` (sub-millisecond).

### Stage 2 — Prompt Hash

- The `messages` array is extracted from the JSON body and SHA-256 hashed. If `messages` is absent, the full body is hashed.
- Hash is pushed to `agent_prompts:history` (Redis list) via `LPUSH` + `LTRIM`.
- The most recent `N` hashes are fetched. If all `N` are identical, a recursive loop is declared, the lock is set, and the request is blocked.
- `N` = `limits.max_consecutive_identical_prompts` in config (default 4).

### Stage 3 — Upstream Forward

- Request headers are copied verbatim (preserves `x-api-key`, `anthropic-version`, `Authorization`, etc.). `Host` and `Content-Length` are rebuilt by Go's `http.Client`.
- For OpenAI-compatible requests with `"stream": true`, `"stream_options": {"include_usage": true}` is injected into the body so the final SSE chunk carries token counts.
- Anthropic requests are routed to `https://api.anthropic.com`.
- OpenAI-compatible requests are routed based on the `model` field prefix (see Routing section below).

### Stage 4 — Token Capture

**Streaming responses (`Content-Type: text/event-stream`):**
- A `bufio.Reader` reads the body line-by-line. Each line is written to the response writer and flushed immediately — preserving real-time streaming to the client.
- `data:` lines are parsed to extract token counts without blocking the forward path.
- Anthropic: `message_start` → `input_tokens`; `message_delta` → `output_tokens`.
- OpenAI-compatible: final chunk with `usage` object → `prompt_tokens` + `completion_tokens`.

**Non-streaming responses:**
- Full body is buffered, token counts extracted, body written to client.

### Stage 5 — Velocity Check

- Token counts × model pricing = USD cost delta.
- Cost stored atomically in Redis minute-bucket keys (`spend:minute:YYYYMMDDHHMM`) and hour-bucket keys as integer microdollars (`int64`, cost × 1 000 000) via `INCRBY`.
- **Sliding window approximation:** Spend is stored in per-calendar-minute Redis buckets. The per-minute velocity check reads the current and previous minute bucket (2 keys), summing up to ~119 seconds of spend. This approximates a 60-second sliding window without per-second key overhead. For a safety circuit breaker the over-counting is intentional — it errs towards tripping.
- Hourly spend sums current and previous hour bucket (2 key lookups).
- If either limit is exceeded: lock is set, webhook fires (if configured), and the NEXT request is blocked.

## Redis Key Schema

| Key | Type | TTL | Purpose |
|-----|------|-----|---------|
| `agent_state:locked` | String (`"1"`) | None (sticky) | Circuit breaker active flag |
| `agent_state:lock_reason` | String | None | Human-readable trip reason |
| `agent_prompts:history` | List | None (LTRIM-bounded) | Rolling prompt hash window |
| `spend:minute:YYYYMMDDHHMM` | String (int64 µ$) | 10 min | Per-minute spend bucket |
| `spend:hour:YYYYMMDDHH` | String (int64 µ$) | 2 hr | Per-hour spend bucket |
| `spend:total` | Float | None | Cumulative spend since last reset |
| `metrics:request_count` | Integer | None | Total intercepted requests |
| `metrics:last_model` | String | None | Most recently seen model name |

`POST /api/reset` deletes: `agent_state:locked`, `agent_state:lock_reason`, `agent_prompts:history`.
Spend and request-count keys are intentionally preserved across resets for auditing.

## Routing Logic (`/v1/chat/completions`)

Model name is extracted from the JSON body `"model"` field. Resolution order:

1. Check `config.routing.providers` — if any provider key is a prefix of the model name (case-insensitive), route there.
2. Built-in prefix rules:
   - `deepseek-*` → `https://api.deepseek.com`
   - `gpt-*`, `o1*`, `o3*`, `chatgpt*` → `https://api.openai.com`
3. Fall back to `config.routing.default_openai_provider` (default: `openai`).

Anthropic requests (`/v1/messages`) always route to `https://api.anthropic.com`.

## Admin API

Both endpoints are on port 8531 and include `Access-Control-Allow-Origin: *`.

| Endpoint | Method | Description |
|----------|--------|-------------|
| `GET /api/status` | GET | JSON metrics snapshot (spend, request count, lock state, limits) |
| `POST /api/reset` | POST | Clears lock, lock reason, and prompt history |
| `GET /` | GET | Embedded dashboard HTML |

## Pricing Model

Token costs are defined per model in `config/killswitch.yaml` under `pricing_matrix.models` as USD per million tokens. When a model is not found, `default_input_cost_per_m` and `default_output_cost_per_m` are used.

```
cost_usd = (input_tokens × input_cost_per_m + output_tokens × output_cost_per_m) / 1_000_000
```

Stored as `int64` microdollars (`cost_usd × 1_000_000`) in Redis to enable atomic `INCRBY` without floating-point concurrency issues.

## Build Process

The Dockerfile uses a two-stage build:

1. **Builder** (`golang:1.22-alpine`): `go mod download -x` (verifies deps against committed `go.sum`), then `CGO_ENABLED=0 go build -ldflags="-s -w"` produces a statically linked binary.
2. **Runtime** (`alpine:3.19`): Copies only the binary + CA certificates. Final image is ~15MB.

No local Go toolchain is required — all compilation happens inside Docker.

## Concurrency Model

- Each inbound HTTP request is handled in its own goroutine by Go's `net/http` server.
- All shared state lives in Redis; no in-process shared state (no mutexes needed in the proxy layer).
- Redis pipeline batches related operations into single round trips.
- The webhook fires in a detached goroutine (`go s.fireWebhook(reason)`) to avoid blocking the response path.
- **Graceful shutdown:** On SIGTERM, the proxy stops accepting new connections and drains in-flight requests for up to 30 seconds. Streaming SSE responses in progress continue until they complete or the drain timeout expires.

## Circuit Breaker State Machine

```
         ┌──────────────────────────────────────────┐
         │  OPEN (normal operation)                 │
         │  locked == false                         │
         └──────────────────────────────────────────┘
                  │                    ▲
   velocity/loop  │                    │  POST /api/reset
   limit exceeded │                    │
                  ▼                    │
         ┌──────────────────────────────────────────┐
         │  TRIPPED                                 │
         │  locked == "1"                           │
         │  All requests → HTTP 402                 │
         └──────────────────────────────────────────┘
```

The breaker has no automatic re-open timer. A human (or monitoring script) must call `POST /api/reset` after investigating the cause.
