# CLAUDE.md

System architecture, components, and data flow: @docs/ARCHITECTURE.md
Project goals, success criteria, and non-goals: @docs/GOALS.md
Go code standards, naming, error handling, logging: @docs/CODE-STANDARD.md
Testing strategy, commands, and patterns: @docs/TESTS.md

## Commands

```bash
# Build and start the full stack (first run ~2–4 min for Go compile)
docker compose up --build -d

# Tail proxy logs
docker compose logs -f proxy-engine

# Verify stack health
docker compose ps
docker exec killswitch-db redis-cli ping        # → PONG
curl -s http://localhost:8531/api/status | python3 -m json.tool

# Reset circuit breaker
curl -X POST http://localhost:8531/api/reset

# Rebuild proxy only (no Redis restart)
docker compose build proxy-engine && docker compose up -d proxy-engine

# Stop everything
docker compose down

# Wipe state (Redis data)
docker compose down -v
```

After any code change: `docker compose build proxy-engine` must succeed with zero errors before finishing.

MANDATORY FOR ALL AGENTS: after every code, config, or Dockerfile change — rebuild with `docker compose build proxy-engine`, verify `docker compose up -d` starts cleanly, hit `curl -s http://localhost:8531/api/status` and confirm `"locked": false`. Fix every build failure and runtime error before finishing.

## Critical rules

- **Proxy port 8530 only** — NEVER bind port 443 or 80; this is a local developer tool, not an internet-facing service
- **No body mutation for Anthropic** — pass Anthropic requests unmodified; only OpenAI-compat requests get `stream_options` injected
- **Integer microdollar accounting** — all Redis spend counters use `int64` microdollars (`cost × 1_000_000`); NEVER store floats in Redis atomic counters
- **Lock is sticky** — once set, the circuit breaker lock persists until an explicit `POST /api/reset`; it is NOT auto-released after the window cools down
- **Config is live** — `config/killswitch.yaml` is volume-mounted; changing it requires `docker compose restart proxy-engine` (no rebuild), NOT a full `docker compose up --build`
- **No secrets in source** — API keys flow through request headers from the agent; NEVER read or log API key values in proxy code
- **slog everywhere** — use `log/slog` for all structured logging; NEVER use `fmt.Println` or the `log` package for operational messages
- **Context propagation** — every Redis call and upstream HTTP call must receive `r.Context()`, never `context.Background()` in the hot path
- **SSE fidelity** — the proxy must forward each SSE line to the client before processing it; NEVER buffer a full stream before writing

## Architecture summary

```
Agent (ANTHROPIC_BASE_URL=http://localhost:8530)
  │
  ▼  port 8530
proxy-engine (Go)  ──redis──►  state-db (Redis 7.2)
  │  port 8531
  ▼
Admin UI + REST API (http://localhost:8531)
```

Five-stage lifecycle per request: **Lock Check → Prompt Hash → Forward → Token Capture → Velocity Check**

Full diagram and stage specifications: @docs/ARCHITECTURE.md

## Ports

| Port | Service | Purpose |
|------|---------|---------|
| 8530 | proxy-engine | AI API proxy — point agents here |
| 8531 | proxy-engine | Admin dashboard + REST API |
| 6379 | state-db | Redis (internal; exposed for local inspection) |

## Config hot-reload

`config/killswitch.yaml` controls all limits, pricing, routing, and notifications. It is bind-mounted at `/app/config/killswitch.yaml` inside the container. To apply changes:

```bash
docker compose restart proxy-engine   # reload config — no rebuild needed
```

## Keeping docs updated

After completing significant changes, update the relevant doc files:
- New Go packages or patterns → `docs/CODE-STANDARD.md`
- Architecture changes → `docs/ARCHITECTURE.md`
- New test patterns → `docs/TESTS.md`
- Goal changes → `docs/GOALS.md`
