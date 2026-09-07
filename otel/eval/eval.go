// Package eval instruments generic Scope evaluation calls with
// OpenTelemetry.
package eval

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/samber/lo"
	apiotel "go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
	"go.opentelemetry.io/otel/trace"

	coreeval "github.com/Tangerg/scope/eval"
	"github.com/Tangerg/scope/otel/internal/errortelemetry"
)

const (
	instrumentationName          = "github.com/Tangerg/scope/otel/eval"
	spanName                     = "eval.evaluate"
	operationDurationMetric      = "eval.operation.duration"
	operationDurationDescription = "Eval operation duration."
	operationDurationUnit        = "s"
	operationAttribute           = "eval.operation.name"
	metricNamespaceAttribute     = "eval.metric.namespace"
	metricNameAttribute          = "eval.metric.name"
	verdictAttribute             = "eval.verdict"
	scoreAttribute               = "eval.score"
	measurementAttribute         = "eval.measurement"
	operationEvaluate            = "evaluate"
	errorCanceled                = "context.canceled"
	errorDeadline                = "context.deadline_exceeded"
	errorInvalidConfig           = "eval.invalid_evaluator_config"
	errorInvalidReport           = "eval.invalid_report"
)

var (
	ErrInvalidConfig    = errors.New("otel/eval: invalid config")
	ErrInvalidEvaluator = errors.New("otel/eval: invalid evaluator")
)

// MiddlewareConfig supplies optional OTel providers for evaluation
// instrumentation.
type MiddlewareConfig struct {
	TracerProvider trace.TracerProvider
	MeterProvider  metric.MeterProvider
}

// Middleware is typed by the evaluated subject so Wrap remains the single
// composition API despite Go methods not supporting their own type parameters.
type Middleware[T any] struct {
	tracer   trace.Tracer
	duration metric.Float64Histogram
}

// NewMiddleware resolves nil OTel providers to the official process globals.
func NewMiddleware[T any](config MiddlewareConfig) (Middleware[T], error) {
	tracerProvider := config.TracerProvider
	if lo.IsNil(tracerProvider) {
		tracerProvider = apiotel.GetTracerProvider()
	}
	meterProvider := config.MeterProvider
	if lo.IsNil(meterProvider) {
		meterProvider = apiotel.GetMeterProvider()
	}
	duration, err := meterProvider.Meter(instrumentationName).Float64Histogram(
		operationDurationMetric,
		metric.WithDescription(operationDurationDescription),
		metric.WithUnit(operationDurationUnit),
	)
	if err != nil {
		return Middleware[T]{}, fmt.Errorf("%w: create duration histogram: %w", ErrInvalidConfig, err)
	}
	return Middleware[T]{
		tracer:   tracerProvider.Tracer(instrumentationName),
		duration: duration,
	}, nil
}

// Wrap decorates one evaluator without observing or retaining its subject.
func (m Middleware[T]) Wrap(next coreeval.Evaluator[T]) (coreeval.Evaluator[T], error) {
	if lo.IsNil(m.tracer) || lo.IsNil(m.duration) {
		return nil, fmt.Errorf("%w: middleware must be constructed with NewMiddleware", ErrInvalidConfig)
	}
	if lo.IsNil(next) {
		return nil, fmt.Errorf("%w: value must not be nil", ErrInvalidEvaluator)
	}
	return coreeval.EvaluatorFunc[T](func(ctx context.Context, subject T) (coreeval.Report, error) {
		startedAt := time.Now()
		operation := attribute.String(operationAttribute, operationEvaluate)
		spanCtx, span := m.tracer.Start(ctx, spanName,
			trace.WithSpanKind(trace.SpanKindInternal),
			trace.WithAttributes(operation),
		)
		report, err := next.Evaluate(spanCtx, subject)
		attributes := []attribute.KeyValue{operation}
		if err == nil {
			outcomeAttributes := reportAttributes(report)
			span.SetAttributes(outcomeAttributes...)
			attributes = append(attributes, metricIdentityAttributes(report.Metric)...)
		} else {
			errorType := errorTypeAttribute(err)
			errortelemetry.Record(span, errorType)
			attributes = append(attributes, errorType)
		}
		span.End()
		m.duration.Record(
			spanCtx,
			time.Since(startedAt).Seconds(),
			metric.WithAttributes(attributes...),
		)
		return report, err
	}), nil
}

func reportAttributes(report coreeval.Report) []attribute.KeyValue {
	attributes := metricIdentityAttributes(report.Metric)
	if report.Verdict != coreeval.VerdictUnspecified {
		attributes = append(attributes, attribute.String(verdictAttribute, string(report.Verdict)))
	}
	if report.Score != nil {
		attributes = append(attributes, attribute.Float64(scoreAttribute, float64(*report.Score)))
	}
	if report.Measurement != nil {
		attributes = append(attributes, attribute.Float64(measurementAttribute, *report.Measurement))
	}
	return attributes
}

func metricIdentityAttributes(metricValue coreeval.Metric) []attribute.KeyValue {
	attributes := make([]attribute.KeyValue, 0, 2)
	if metricValue.Namespace() != "" {
		attributes = append(attributes, attribute.String(metricNamespaceAttribute, metricValue.Namespace()))
	}
	if metricValue.Name() != "" {
		attributes = append(attributes, attribute.String(metricNameAttribute, string(metricValue.Name())))
	}
	return attributes
}

func errorTypeAttribute(err error) attribute.KeyValue {
	switch {
	case errors.Is(err, context.Canceled):
		return semconv.ErrorTypeKey.String(errorCanceled)
	case errors.Is(err, context.DeadlineExceeded):
		return semconv.ErrorTypeKey.String(errorDeadline)
	case errors.Is(err, coreeval.ErrInvalidEvaluatorConfig):
		return semconv.ErrorTypeKey.String(errorInvalidConfig)
	case errors.Is(err, coreeval.ErrInvalidReport):
		return semconv.ErrorTypeKey.String(errorInvalidReport)
	default:
		return semconv.ErrorType(err)
	}
}
