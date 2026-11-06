.PHONY: test vet build collector demo

test:
	go test ./... -count=1 -cover

vet:
	go vet ./...

build:
	go build ./...

collector:
	go install go.opentelemetry.io/collector/cmd/builder@v0.164.0
	builder --config builder-config.yaml

demo:
	cd deploy && docker compose up --build
