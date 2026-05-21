# RunawayKillSwitch

**Network-layer financial circuit breaker and infinite loop protection for autonomous AI agents.**

Stops runaway LLM spend within seconds — no SDK, no code changes, no language dependencies. Drop-in Docker Compose stack that intercepts outbound AI API traffic at the network layer.

```
Agent ──► RunawayKillSwitch (port 8530) ──► Anthropic / OpenAI / DeepSeek / OpenRouter
              │
              ├── Redis spend counters (velocity tracking)
              ├── Prompt hash loop detection
              └── Circuit breaker (HTTP 402 on trip)
```

## Why This Exists

Autonomous AI agents running unattended (Claude Code sessions, Aider, n8n workflows, cron-triggered pipelines) hit unhandled edge cases and enter recursive error-correction loops. Because the agent runs without supervision, it can execute hundreds of LLM calls per hour, burning **$100–$500 of cloud API credits** before anyone notices.

Existing safeguards fail in at least one of these ways:

| Problem | RunawayKillSwitch Solution |
|---------|---------------------------|
| **Language-locked** — Python SDK guards disappear when you switch to TypeScript | **Network-transparent** — intercepts any tool via `BASE_URL` env var |
| **Tool-locked** — Claude Code monitors don't protect n8n or Aider sessions | **Universal** — supports Anthropic, OpenAI, DeepSeek, OpenRouter simultaneously |
| **Blunt monthly caps** — platform spend limits catch the bill, not the runaway loop in progress | **Velocity-driven** — kills a $5/min rogue loop within seconds |
| **Require code changes** — wrapping every LLM call couples safety to implementation | **Zero code changes** — one environment variable, done |

## Quick Start

```bash
git clone https://github.com/your-org/runaway-killswitch.git
cd runaway-killswitch
docker compose up -d
```

Open the dashboard at **http://localhost:8531**

## Configure Your Agent

Point your agent's base URL to the proxy instead of the cloud provider directly.

**Claude Code / Anthropic SDK:**
```bash
export ANTHROPIC_BASE_URL=http://localhost:8530
```

**Python openai library:**
```python
import openai
client = openai.OpenAI(api_key="your-key", base_url="http://localhost:8530/v1")
```

**Node.js openai library:**
```js
import OpenAI from 'openai';
const client = new OpenAI({ apiKey: 'your-key', baseURL: 'http://localhost:8530/v1' });
```

**DeepSeek:**
```bash
export OPENAI_BASE_URL=http://localhost:8530/v1
export OPENAI_API_KEY=your-deepseek-key
```

**OpenRouter:**
Edit `config/killswitch.yaml` and set `routing.default_openai_provider: openrouter`.

## How It Works

Every request passes through a **5-stage interception lifecycle**:

1. **Lock Check** — Is the circuit breaker active? If yes, return HTTP 402 immediately.
2. **Prompt Hash** — SHA-256 hash of the `messages` array. Detects recursive loops by checking for N consecutive identical prompts.
3. **Upstream Forward** — Request forwarded unmodified to the cloud provider. OpenAI streaming requests get `stream_options: {include_usage: true}` injected automatically.
4. **Token Capture** — Response streams through the proxy line-by-line. Token counts extracted from SSE events (Anthropic `message_start`/`message_delta`, OpenAI final `usage` chunk) without buffering.
5. **Velocity Check** — Tokens × model pricing = USD cost. Stored in Redis minute/hour buckets as integer microdollars. Sliding window sums checked against limits. If exceeded → circuit breaker trips.

### Detection Methods

| Method | What It Catches | Trip Speed |
|--------|----------------|------------|
| **Spend velocity** | Token burn rate exceeds $/min or $/hour threshold | Next request after limit crossed |
| **Prompt loop** | N consecutive requests with identical `messages` arrays | Before request N+1 is forwarded |

### Architecture

```
┌─────────────────────────────────────────────────────────────┐
│  Developer Machine                                          │
│                                                             │
│  Agent process                                              │
│  ANTHROPIC_BASE_URL=http://localhost:8530                   │
│         │                                                   │
│         ▼                                                   │
│  ┌──────────────────────────────────────────────────────┐   │
│  │  DOCKER COMPOSE NETWORK                               │   │
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

**Stack:** Go 1.22 + Redis 7.2, ~15MB final container image, zero external dependencies beyond Docker.

## Dashboard

Real-time monitoring at **http://localhost:8531**:

- Live spend velocity (1-min, 5-min, hourly windows)
- Total spend and request count
- Progress bars showing limit utilization with color-coded warnings
- Circuit breaker status with trip reason
- One-click reset button

## Configuration

All tunable values live in `config/killswitch.yaml`. Edit and restart — no rebuild needed:

```bash
docker compose restart proxy-engine
```

### Spend Limits

```yaml
limits:
  max_spend_per_minute_usd: 1.50       # Trip if > $1.50 in any 60-second window
  max_spend_per_hour_usd: 12.00        # Trip if > $12.00 in any hour
  max_consecutive_identical_prompts: 4 # Trip after 4 identical prompt hashes
```

### Model Pricing

Per-model costs in USD per million tokens. Unknown models fall back to defaults:

```yaml
pricing_matrix:
  default_input_cost_per_m: 3.00
  default_output_cost_per_m: 15.00
  models:
    claude-sonnet-4-5:
      input_cost_per_m: 3.00
      output_cost_per_m: 15.00
    gpt-4o:
      input_cost_per_m: 2.50
      output_cost_per_m: 10.00
    # ... add any model
```

### Notifications

```yaml
notifications:
  system_bell: true  # ASCII bell in container logs when breaker trips
  webhook:
    enabled: false
    url: ""          # Discord/Slack webhook URL
    format: "json_summary"
```

## Admin REST API

| Endpoint | Method | Description |
|----------|--------|-------------|
| `http://localhost:8531/api/status` | GET | JSON metrics snapshot |
| `http://localhost:8531/api/reset` | POST | Reset circuit breaker and prompt history |

**Reset via CLI:**
```bash
curl -X POST http://localhost:8531/api/reset
```

**Check status:**
```bash
curl -s http://localhost:8531/api/status | python3 -m json.tool
```

## Ports

| Port | Purpose |
|------|---------|
| 8530 | AI API proxy — point agents here |
| 8531 | Admin dashboard + REST API |
| 6379 | Redis (internal; exposed for local inspection) |

## Makefile

```bash
make build        # Build Docker images
make up           # Start stack detached
make down         # Stop containers
make down-v       # Stop and wipe Redis data
make restart      # Restart proxy (config hot-reload)
make logs         # Tail proxy logs
make status       # Health check
make test         # Run all tests
make test-unit    # Unit tests only
make test-coverage # Coverage report
make clean        # Full teardown
```

## Use Cases

- **Claude Code** — protect long-running coding sessions from infinite linter/test-fix loops
- **Aider** — stop recursive code repair cycles that burn tokens
- **Multi-agent pipelines** — LangGraph, CrewAI, AutoGen workflows with autonomous LLM calls
- **n8n / automated workflows** — background AI tasks that run unattended
- **Cron-triggered agents** — scheduled scripts that can fail silently and loop
- **Any agent framework** — works at the network layer, so it's framework-agnostic

## Design Principles

- **Transparency** — invisible to agents during normal operation; any behavior an agent can't reproduce by calling the provider directly is a defect
- **Small surface area** — only examines what it must: model strings, token counts, request frequency
- **Fail open** — internal errors (Redis timeout, parse failure) log a warning and forward the request; the breaker only blocks on deliberate decisions
- **Sticky lock** — once tripped, stays tripped until explicit `POST /api/reset`; no automatic re-open
- **Config over code** — all tunable values in YAML; no recompile needed

## What This Is Not

- Not a SaaS or multi-tenant service — single-developer local tool
- Not a prompt content analyzer — looks at structural metadata only, never reads or scores prompt text
- Not a response modifier — never changes provider responses
- Not a persistent billing tracker — Redis runs without persistence; history resets on restart
- Not a load balancer — single upstream per provider, no retry logic

## Development

```bash
# Build and start
make build && make up

# Run tests
make test-unit        # No Docker required
make test-integration # Spins up Redis container automatically

# View coverage
make test-coverage
```

## License

MIT
