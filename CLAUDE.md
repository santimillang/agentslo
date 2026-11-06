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

## Next steps, in order

1. Small dashboard (ClickHouse queries → HTML or Grafana).
2. GitHub Actions: vet, test (with `-race` on Linux), ocb build.
3. Blog-post-style README section with a screenshot.

Known quirk, not yet decided: an A,B,A,B oscillation with fixed args also
trips `repeat` (A appears Threshold times in the window), so one session can
count under both kinds.

## Conventions

- Keep `internal/detect` free of collector imports so it stays fast to test.
- Metric attributes reuse GenAI semconv keys (`gen_ai.agent.name`).
- Never persist raw tool arguments: the connector hashes them in
  `convert.go`, and the deploy config hashes them before any exporter.
