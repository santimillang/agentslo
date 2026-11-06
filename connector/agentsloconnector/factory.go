// Package agentsloconnector is an OpenTelemetry Collector connector that reads
// GenAI spans and emits agent reliability metrics: SLO burn rates, loop
// detections and a per-agent health score.
//
// Pipeline shape:
//
//	traces:  otlp -> [agentslo, clickhouse]
//	metrics: agentslo -> clickhouse
package agentsloconnector

import (
	"context"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/connector"
	"go.opentelemetry.io/collector/consumer"
)

var componentType = component.MustNewType("agentslo")

const stability = component.StabilityLevelDevelopment

// NewFactory returns the connector factory, registered in builder-config.yaml.
func NewFactory() connector.Factory {
	return connector.NewFactory(
		componentType,
		func() component.Config { return createDefaultConfig() },
		connector.WithTracesToMetrics(createTracesToMetrics, stability),
	)
}

func createTracesToMetrics(
	_ context.Context,
	set connector.Settings,
	cfg component.Config,
	next consumer.Metrics,
) (connector.Traces, error) {
	return newConnector(set.Logger, cfg.(*Config), next), nil
}
