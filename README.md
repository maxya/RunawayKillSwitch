# RunawayKillSwitch

Network-layer financial circuit breaker for autonomous AI agents. Intercepts outbound
LLM API traffic, tracks token spend velocity, and cuts access when runaway loops are detected.

## Quick Start

```bash
docker compose up -d
```

Open the dashboard at **http://localhost:8531**

## How to Configure Your Agent

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

## Ports

| Port | Purpose |
|------|---------|
| 8530 | AI API proxy — point agents here |
| 8531 | Admin dashboard + REST API |

## Admin REST API

| Endpoint | Method | Description |
|----------|--------|-------------|
| `http://localhost:8531/api/status` | GET | JSON metrics snapshot |
| `http://localhost:8531/api/reset` | POST | Reset circuit breaker and prompt history |

## Resetting the Circuit Breaker

**Via dashboard:** Open http://localhost:8531 and click "Reset Circuit Breaker"

**Via CLI:** `curl -X POST http://localhost:8531/api/reset`

## Configuration

Edit `config/killswitch.yaml`. The config is volume-mounted into the container,
so changes take effect on the next `docker compose restart proxy-engine` (no rebuild needed).

Key settings:
- `limits.max_spend_per_minute_usd` — trip threshold for the last 60-second window
- `limits.max_spend_per_hour_usd` — trip threshold for the last hour
- `limits.max_consecutive_identical_prompts` — trip threshold for loop detection
- `pricing_matrix.models` — per-model token costs (per million tokens)
- `notifications.webhook.url` — Discord/Slack webhook for alerts
