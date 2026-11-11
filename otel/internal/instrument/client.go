package instrument

import (
	"fmt"

	"github.com/samber/lo"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/semconv/v1.41.0/genaiconv"
	"go.opentelemetry.io/otel/trace"

	"github.com/Tangerg/scope/otel/internal/genaimetric"
)

// Client is the telemetry every Core model middleware records with. Each
// middleware adds only its capability's histograms from Meter, so the shared
// instruments and their readiness rule have one owner.
type Client struct {
	Logger   log.Logger
	Provider string
	Tracer   trace.Tracer
	Meter    metric.Meter
	Duration genaiconv.ClientOperationDuration
}

type ClientConfig struct {
	InstrumentationName string
	Provider            string
	TracerProvider      trace.TracerProvider
	MeterProvider       metric.MeterProvider
	LoggerProvider      log.LoggerProvider
}

func NewClient(config ClientConfig) (Client, error) {
	meter := MeterProvider(config.MeterProvider).Meter(config.InstrumentationName)
	duration, err := genaiconv.NewClientOperationDuration(meter, genaimetric.DurationBuckets())
	if err != nil {
		return Client{}, fmt.Errorf("create duration histogram: %w", err)
	}
	return Client{
		Logger:   LoggerProvider(config.LoggerProvider).Logger(config.InstrumentationName),
		Provider: Dimension(config.Provider),
		Tracer:   TracerProvider(config.TracerProvider).Tracer(config.InstrumentationName),
		Meter:    meter,
		Duration: duration,
	}, nil
}

// Ready reports a Client NewClient produced; a zero Client records nothing.
func (c Client) Ready() bool {
	return !lo.IsNil(c.Logger) && !lo.IsNil(c.Tracer) && !lo.IsNil(c.Duration.Inst())
}
