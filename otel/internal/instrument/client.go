package instrument

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/samber/lo"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/metric"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
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
	classify func(error) attribute.KeyValue
}

type ClientConfig struct {
	InstrumentationName string
	Provider            string
	TracerProvider      trace.TracerProvider
	MeterProvider       metric.MeterProvider
	LoggerProvider      log.LoggerProvider
	// ClassifyError maps a capability failure to its low-cardinality
	// error.type; each capability owns which Core sentinels it names.
	ClassifyError func(error) attribute.KeyValue
}

func NewClient(config ClientConfig) (Client, error) {
	if config.ClassifyError == nil {
		return Client{}, errors.New("error classifier is required")
	}
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
		classify: config.ClassifyError,
	}, nil
}

// Ready reports a Client NewClient produced; a zero Client records nothing.
func (c Client) Ready() bool {
	return !lo.IsNil(c.Logger) && !lo.IsNil(c.Tracer) && !lo.IsNil(c.Duration.Inst()) && c.classify != nil
}

// StartOperation names the span "{operation} {request model}", omitting an
// unknown model as the GenAI semantic conventions specify, and starts it at the
// instant every duration of the operation is measured from.
func (c Client) StartOperation(
	ctx context.Context,
	name genaiconv.OperationNameAttr,
	requestModel string,
	attributes ...attribute.KeyValue,
) (context.Context, *Operation) {
	startedAt := time.Now()
	spanName := string(name)
	spanAttributes := []attribute.KeyValue{
		semconv.GenAIOperationNameKey.String(string(name)),
		semconv.GenAIProviderNameKey.String(c.Provider),
	}
	if requestModel != "" {
		spanName += " " + requestModel
		spanAttributes = append(spanAttributes, semconv.GenAIRequestModel(requestModel))
	}
	spanCtx, span := c.Tracer.Start(ctx, spanName,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithTimestamp(startedAt),
		trace.WithAttributes(append(spanAttributes, attributes...)...),
	)
	return spanCtx, &Operation{client: c, name: name, span: span, startedAt: startedAt, requestModel: requestModel}
}
