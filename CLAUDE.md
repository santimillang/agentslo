# agentslo – notes for Claude Code

OTel Collector connector (traces → metrics) that computes SLO burn rates,
loop detection and a health score for AI agents from GenAI semconv spans.

## State at handoff (2026-11-06, second session)

- Collector pinned to core v0.164.0 / stable modules v1.70.0; contrib
  clickhouseexporter v0.164.1 (patch release built against the same core).
  `go.mod` is now `go 1.26.0`; Docker build image is `golang:1.26`.
- `go build ./...`, `go vet ./...`, `go test ./...` all pass. Connector
  tests in `connector/agentsloconnector/connector_test.go` (97.8% coverage);
  `internal/detect` 86.4%. No API drift was needed for v0.164.
- `builder --config builder-config.yaml` (ocb v0.164.0) builds
  `_build/otelcol-agentslo`, and `validate --config deploy/otelcol.yaml` passes.
- `-race` is unavailable locally (Windows, no cgo); run it in CI.

## Next steps, in order

4. Start Docker Desktop, then `cd deploy && docker compose up --build`; confirm spans and
   `agentslo.*` metrics land in ClickHouse, and that `research-agent` health
   drops below `support-bot`.
5. Then: a small dashboard (ClickHouse queries → HTML or Grafana), GitHub
   Actions (vet, test, ocb build), and a blog-post-style README section with
   a screenshot.

## Conventions

- Keep `internal/detect` free of collector imports so it stays fast to test.
- Metric attributes reuse GenAI semconv keys (`gen_ai.agent.name`).
- Never persist raw tool arguments; hash them in `convert.go`.
