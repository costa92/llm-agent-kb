// Package obs assembles the otel TracerProvider for kbd.
package obs

import (
	"context"

	otelexport "github.com/costa92/llm-agent-otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// Config selects the OTLP exporter target.
type Config struct {
	ServiceName string
	Endpoint    string
	Protocol    string
	Insecure    bool
}

// NewTracerProvider builds an SDK TracerProvider via the otel helper. The
// caller owns Shutdown.
func NewTracerProvider(ctx context.Context, cfg Config) (*sdktrace.TracerProvider, error) {
	return otelexport.NewTracerProvider(ctx, otelexport.ExporterConfig{
		Protocol: cfg.Protocol,
		Endpoint: cfg.Endpoint,
		Insecure: cfg.Insecure,
	})
}
