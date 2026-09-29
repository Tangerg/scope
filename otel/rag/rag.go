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
)

var (
	ErrInvalidConfig    = errors.New("otel/rag: invalid config")
	ErrInvalidRetriever = errors.New("otel/rag: invalid retriever")
)

// Nil providers use the corresponding OpenTelemetry globals.
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
) (candidates corerag.Candidates, err error) {
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
	defer errortelemetry.Finish(&err, func(observedError error) {
		finishedAt := time.Now()
		defer span.End(trace.WithTimestamp(finishedAt))
		if observedError != nil {
			errorType := errortelemetry.Classify(observedError)
			errortelemetry.Record(span, errorType, trace.WithTimestamp(finishedAt))
			metricAttributes = append(metricAttributes, errorType)
		}
		i.middleware.duration.Record(
			ctx,
			finishedAt.Sub(startedAt).Seconds(),
			metric.WithAttributes(metricAttributes...),
		)
	})
	candidates, err = i.next.Retrieve(ctx, query)
	span.SetAttributes(attribute.Int(documentCountAttribute, len(candidates)))
	return candidates, err
}

var _ corerag.Retriever = (*instrumentedRetriever)(nil)
