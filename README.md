# agentslo

**SRE-style reliability for AI agents, as an OpenTelemetry Collector connector.**

LLM observability tools are good at answering "why did this trace do that?".
They are less good at answering the on-call question: *is this agent burning
its error budget right now, and should someone get paged?*

agentslo reads standard [OTel GenAI spans](https://opentelemetry.io/docs/specs/semconv/gen-ai/)
in the collector and turns them into reliability signals:

| Signal | What it catches |
|---|---|
| `agentslo.burn_rate{window=1h\|6h}` | Error-budget burn on agent *tasks* (`invoke_agent` spans), not individual LLM calls |
| `agentslo.alert.firing{rule=page\|ticket}` | Multi-window, multi-burn-rate alerts (Google SRE workbook) |
| `agentslo.loop.events{kind}` | `repeat` (same tool + args), `retry_storm` (same tool failing), `oscillation` (A,B,A,B…) |
| `agentslo.tool.error_ratio` | Failed tool calls / tool calls |
| `agentslo.health` | One 0–100 score per agent, weighted toward budget burn |

A task that *succeeds* after grinding the same tool call eight times still
counts as bad. That's the point: those are the runs that quietly cost money
and frustrate users while every error-rate dashboard stays green.

## Why a connector

- **Framework-agnostic.** Works with anything that emits GenAI semconv spans:
  no SDK, no decorators, no code changes in the agent.
- **Backend-agnostic.** Emits plain OTel metrics. ClickHouse is the demo;
  Prometheus, Elastic or any vendor works the same.
- **Privacy-preserving.** Tool arguments are fingerprinted (truncated SHA-256)
  for loop detection and never stored.

```
traces:  otlp ──► agentslo ──► (metrics pipeline) ──► clickhouse
              └─► clickhouse (raw spans)
```

## Run the demo

```bash
cd deploy
docker compose up --build
# raw spans + agentslo metrics land in ClickHouse (database `otel`)
```

`examples/fakeagent` emits two agents: `support-bot` (healthy) and
`research-agent` (occasionally loops, retry-storms, oscillates, runs slow).
Set `SCENARIO=loop|retry_storm|oscillation|slow|healthy` to force one pattern.

## Configuration

```yaml
connectors:
  agentslo:
    objective: 0.99        # SLO on agent tasks
    latency_target: 30s    # tasks slower than this are bad (0 = off)
    emit_interval: 15s
    loop:
      window: 10           # last N tool calls per session
      threshold: 4         # occurrences that trip a pattern
      idle_ttl: 30m
```

## Layout

- `internal/detect` – the engine (loops, burn rates, health). Stdlib only, fully unit-tested.
- `connector/agentsloconnector` – collector wiring: pdata → engine → metrics.
- `deploy/` – ocb build, collector config, docker-compose with ClickHouse.
- `examples/fakeagent` – span generator with known failure patterns.

## Status

Early. v0 keeps state in memory per collector instance, so run it behind a
load-balancing exporter keyed by `gen_ai.conversation.id` when scaling out.
