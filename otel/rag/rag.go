// Package rag instruments retrieval capabilities with OpenTelemetry.
package rag

import (
	"context"
	"errors"
	"fmt"
	"time"

	apiotel "go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/otel/internal/errortelemetry"
	corerag "github.com/Tangerg/scope/rag"
)

const (
	instrumentationName          = "github.com/Tangerg/scope/otel/rag"
	retrieveSpanName             = "rag.retrieve"
	operationDurationName        = "rag.operation.duration"
	operationDurationDescription = "RAG operation duration."
	operationDurationUnit        = "s"
	operationAttributeName       = "rag.operation.name"
	documentCountAttribute       = "rag.document.count"
	retrieveOperation            = "retrieve"
	errorTypeCanceled            = "context.canceled"
	errorTypeDeadlineExceeded    = "context.deadline_exceeded"
)

var (
	ErrInvalidConfig    = errors.New("otel/rag: invalid config")
	ErrInvalidRetriever = errors.New("otel/rag: invalid retriever")
)

// MiddlewareConfig takes providers rather than a tracer and meter so the
// choice of telemetry destination stays with the composition root. A nil
// provider falls back to the OpenTelemetry global, which is the convenient
// default for an application and the wrong one for a test that must observe
// only its own instruments.
type MiddlewareConfig struct {
	TracerProvider trace.TracerProvider
	MeterProvider  metric.MeterProvider
}

// Middleware observes retrieval without recording query text or document
// content. Construct it once at the composition root and wrap only the final
// Retriever or branches that need distinct attribution.
type Middleware struct {
	tracer   trace.Tracer
	duration metric.Float64Histogram
}

// NewMiddleware fixes instrument identity and provider binding once so every
// retrieval contributes to the same telemetry series.
func NewMiddleware(config MiddlewareConfig) (Middleware, error) {
	tracerProvider := config.TracerProvider
	if lo.IsNil(tracerProvider) {
		tracerProvider = apiotel.GetTracerProvider()
	}
	meterProvider := config.MeterProvider
	if lo.IsNil(meterProvider) {
		meterProvider = apiotel.GetMeterProvider()
	}
	duration, err := meterProvider.Meter(instrumentationName).Float64Histogram(
		operationDurationName,
		metric.WithDescription(operationDurationDescription),
		metric.WithUnit(operationDurationUnit),
	)
	if err != nil {
		return Middleware{}, fmt.Errorf("%w: create duration histogram: %w", ErrInvalidConfig, err)
	}
	return Middleware{
		tracer: tracerProvider.Tracer(instrumentationName), duration: duration,
	}, nil
}

func (m Middleware) Wrap(next corerag.Retriever) (corerag.Retriever, error) {
	if lo.IsNil(m.tracer) || lo.IsNil(m.duration) {
		return nil, fmt.Errorf("%w: middleware must be constructed with NewMiddleware", ErrInvalidConfig)
	}
	if lo.IsNil(next) {
		return nil, fmt.Errorf("%w: value must not be nil", ErrInvalidRetriever)
	}
	return &instrumentedRetriever{middleware: m, next: next}, nil
}

type instrumentedRetriever struct {
	middleware Middleware
	next       corerag.Retriever
}

func (i *instrumentedRetriever) Retrieve(
	ctx context.Context,
	query corerag.Query,
) (corerag.Candidates, error) {
	metricAttributes := []attribute.KeyValue{
		attribute.String(operationAttributeName, retrieveOperation),
	}
	startedAt := time.Now()
	ctx, span := i.middleware.tracer.Start(
		ctx,
		retrieveSpanName,
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(metricAttributes...),
	)
	candidates, err := corerag.Retrieve(ctx, i.next, query)
	span.SetAttributes(attribute.Int(documentCountAttribute, len(candidates)))
	if err != nil {
		errorType := errorTypeAttribute(err)
		errortelemetry.Record(span, errorType)
		metricAttributes = append(metricAttributes, errorType)
	}
	span.End()
	i.middleware.duration.Record(
		ctx,
		time.Since(startedAt).Seconds(),
		metric.WithAttributes(metricAttributes...),
	)
	return candidates, err
}

func errorTypeAttribute(err error) attribute.KeyValue {
	switch {
	case errors.Is(err, context.Canceled):
		return semconv.ErrorTypeKey.String(errorTypeCanceled)
	case errors.Is(err, context.DeadlineExceeded):
		return semconv.ErrorTypeKey.String(errorTypeDeadlineExceeded)
	default:
		return semconv.ErrorType(err)
	}
}

var _ corerag.Retriever = (*instrumentedRetriever)(nil)
