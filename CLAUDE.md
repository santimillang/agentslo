# agentslo – notes for Claude Code

OTel Collector connector (traces → metrics) that computes SLO burn rates,
loop detection and a health score for AI agents from GenAI semconv spans.
Public repo: https://github.com/santimillang/agentslo

## State at handoff (2026-11-07, second session)

- Collector pinned to core v0.164.0 / stable modules v1.70.0; contrib
  modules (clickhouseexporter, attributesprocessor) v0.164.1, a patch
  release built against the same core. `go.mod` is `go 1.26.0`; Docker
  build image is `golang:1.26`.
- `go build ./...`, `go vet ./...`, `go test ./...` pass. Connector tests in
  `connector/agentsloconnector/connector_test.go`.
- `-race` is unavailable locally (Windows, no cgo); run it in CI.
- `cd deploy && docker compose up --build` works end to end: spans and all
  `agentslo.*` metrics land in ClickHouse (`otel.otel_traces`,
  `otel.otel_metrics_gauge`, `otel.otel_metrics_sum`); research-agent health
  ~60 vs support-bot 100 after ~90s of the `mixed` scenario.
- Fixed during e2e: LLM spans without `gen_ai.conversation.id` inflated the
  LoopRate denominator (engine now counts sessions from tool/task spans only);
  raw tool args were reaching ClickHouse via the traces pipeline (now hashed
  by `attributes/redact_tool_args` in `deploy/otelcol.yaml`).

- An A,B,A,B run now reports as `oscillation` only, not also `repeat`.
- CI (`.github/workflows/ci.yml`): gofmt, vet, `go test -race`, ocb build,
  config validation.
- Grafana 13.3.0 + grafana-clickhouse-datasource 4.23.4 in compose
  (localhost:3000, anonymous viewer). Dashboard JSON is
  `deploy/grafana/dashboards/agentslo.json`; colors come from the dataviz
  skill's validated dark palette (agents = slots 1-2, loop kinds = 3-5,
  status colors only on the stat tiles).
- Grafana does not notice edits to the dashboard JSON through the Docker
  Desktop bind mount on Windows: `docker compose restart grafana` after
  changing it.
- README media in `docs/`: `dashboard.png` (headless Chrome,
  `--window-size=1600,1250 --virtual-time-budget=15000`, kiosk URL with a
  fixed `from`/`to` range), `timelapse.gif` (a frame every 15s over a fixed
  range, assembled with Pillow), `hover.gif` (Claude in Chrome
  gif_creator, resized to 960px). If you retake them, re-check the numbers
  quoted in README "What it looks like".
- `ToolErrRate` and `LoopRate` are rolling over `ratio_window` (default 1h):
  tool calls in a bucket ring like burn rate, sessions with a last-seen
  time. `Engine.Evict` only prunes aged-out state, never resets values.
  Verified in the demo across the 10-minute eviction: no drop.

## Next steps

1. Tag v0.1.0: README install instructions that don't rely on the local
   `replaces`, GitHub release, optionally publish the collector image to GHCR.
2. Health stays at 100 for an agent with no tasks; consider "no data".
3. Engine state is per collector instance (see README "Status").
4. Try it against a real instrumented agent instead of `fakeagent`.

## Conventions

- Keep `internal/detect` free of collector imports so it stays fast to test.
- Metric attributes reuse GenAI semconv keys (`gen_ai.agent.name`).
- Never persist raw tool arguments: the connector hashes them in
  `convert.go`, and the deploy config hashes them before any exporter.
