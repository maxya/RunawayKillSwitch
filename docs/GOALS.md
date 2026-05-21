# Goals — RunawayKillSwitch

## Problem Statement

Autonomous AI agents running unattended (background shells, long-running Claude Code sessions, n8n workflows, cron-triggered pipelines) hit unhandled edge cases and enter recursive error-correction loops. Because the agent runs without supervision, it can execute hundreds of LLM calls per hour, burning $100–$500 of cloud API credits before anyone notices.

Existing safeguards fail in at least one of these ways:
- **Language-locked** — Python SDK guards disappear when you switch to a TypeScript agent.
- **Tool-locked** — Claude Code monitors don't protect n8n or Aider sessions.
- **Blunt monthly caps** — platform spend limits catch the bill, not the runaway loop in progress.
- **Require code changes** — wrapping every LLM call with a guard couples safety to implementation.

## Primary Goal

Provide a **network-transparent circuit breaker** that stops runaway LLM spend within seconds of detection, regardless of which agent framework, programming language, or cloud provider is in use — requiring zero modifications to agent application code.

## Success Criteria

A deployment is successful when all of the following hold:

1. **Proxy transparency** — Any AI agent that works with `ANTHROPIC_BASE_URL=https://api.anthropic.com` works identically with `ANTHROPIC_BASE_URL=http://localhost:8530` (modulo the circuit breaker blocking on trips).
2. **Token capture accuracy** — Token counts recorded in Redis match the counts returned by the provider in the response body, for both streaming and non-streaming responses, for both Anthropic and OpenAI-compatible APIs.
3. **Velocity trip time** — A runaway loop that exceeds `max_spend_per_minute_usd` is blocked on the request *after* the limit is crossed (not the same request, since token counts are only available post-response). Trip latency is under 2 seconds from limit-crossing to first blocked request.
4. **Loop detection** — `N` consecutive requests carrying identical `messages` arrays trigger the breaker before request `N+1` is forwarded.
5. **Zero-downtime reset** — `POST /api/reset` clears the breaker and makes the proxy operational again in under 500ms, with no container restart required.
6. **Build reproducibility** — `docker compose up --build` on a clean machine with internet access produces a running, operational stack with no manual steps.

## Non-Goals

These are intentionally out of scope. Do not implement them.

- **Automatic re-open** — The breaker does not auto-reset after a cooldown window. Human review is required. This is a safety property, not a limitation.
- **Multi-tenant / SaaS** — RunawayKillSwitch is a single-developer local tool. No authentication, no per-user isolation, no billing.
- **Prompt content analysis** — The proxy looks at structural metadata (token counts, prompt hashes, model strings) only. It does not parse, score, or mutate prompt text.
- **Response mutation** — The proxy never modifies provider responses (other than capturing the body for non-streaming token extraction before forwarding). No content filtering, no summarization, no injection.
- **TLS termination** — The proxy listens on plain HTTP inside Docker; TLS is the agent's responsibility (agents connect to `https://api.anthropic.com` via the proxy's outbound connection, not through the proxy's listen port).
- **Persistent spend history** — Redis runs without AOF/RDB persistence (`--appendonly no`). Spend history resets on container restart. Long-term billing analytics belong in the provider's dashboard.
- **Load balancing** — Single upstream per provider. No retry logic, no failover.
- **GUI configuration editor** — The dashboard is read-only. Config changes require editing `killswitch.yaml` and restarting the container.
- **Windows native** — Docker Desktop on Windows is supported. Native Windows binaries are not a target.

## Design Principles

**Transparency over features.** The proxy must be invisible to agents during normal operation. Any behavior that an agent cannot reproduce by calling the provider directly is a defect.

**Small surface area.** The proxy only examines what it must: model strings, token counts, request frequency. The less it touches, the fewer ways it can break.

**Fail open on proxy errors.** If the proxy encounters an internal error (Redis timeout, parse failure), it should log the error and forward the request anyway, rather than blocking legitimate traffic. The circuit breaker should only block on deliberate decisions, not on infrastructure hiccups. *(Exception: if Redis is completely unavailable at startup, the proxy should not start.)*

**Sticky lock.** Once tripped, the breaker stays tripped. An automated re-open creates a false sense of safety — the underlying loop cause may still be present.

**Config over code.** All tunable values (limits, pricing, routing, notifications) live in `killswitch.yaml`. No recompile should ever be needed to adjust operational parameters.

## Target Users

- Developers running Claude Code, Aider, or similar agent tools in long-running sessions
- Engineers building multi-agent pipelines (LangGraph, CrewAI, AutoGen, n8n) that make autonomous LLM calls
- Teams running AI-powered background workers in Docker Compose stacks
- Anyone who has ever woken up to an unexpectedly large LLM bill

## Open Source Positioning

RunawayKillSwitch is positioned as a zero-dependency Docker Compose drop-in. The entire stack is:
- One `docker compose up --build -d` command
- One environment variable change on the agent (`ANTHROPIC_BASE_URL` or `OPENAI_BASE_URL`)
- One YAML file to configure

No SDK installation, no API wrappers, no language runtime requirements on the host.
