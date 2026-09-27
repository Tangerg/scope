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
	assessmentIDAttribute        = "eval.assessment.id"
	metricNamespaceAttribute     = "eval.metric.namespace"
	metricNameAttribute          = "eval.metric.name"
	verdictAttribute             = "eval.verdict"
	decisionPolicyAttribute      = "eval.decision.policy"
	scoreAttribute               = "eval.score"
	measurementAttribute         = "eval.measurement"
	operationEvaluate            = "evaluate"
	errorCanceled                = "context.canceled"
	errorDeadline                = "context.deadline_exceeded"
	errorInvalidConfig           = "eval.invalid_evaluator_config"
	errorInvalidReport           = "eval.invalid_report"
)

var (
	ErrInvalidConfig     = errors.New("otel/eval: invalid config")
	ErrInvalidAssessment = errors.New("otel/eval: invalid assessment")
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

// Wrap decorates one assessment without observing or retaining its subject.
// Assessment identity is bound before evaluation, including failed attempts.
// IDs identify configured assessments and must not contain per-case data.
func (m Middleware[T]) Wrap(next coreeval.Assessment[T]) (coreeval.Assessment[T], error) {
	if lo.IsNil(m.tracer) || lo.IsNil(m.duration) {
		return coreeval.Assessment[T]{}, fmt.Errorf("%w: middleware must be constructed with NewMiddleware", ErrInvalidConfig)
	}
	if err := next.Validate(); err != nil {
		return coreeval.Assessment[T]{}, fmt.Errorf("%w: %w", ErrInvalidAssessment, err)
	}
	evaluator := next.Evaluator
	identity := attribute.String(assessmentIDAttribute, string(next.ID))
	next.Evaluator = coreeval.EvaluatorFunc[T](func(ctx context.Context, subject T) (report coreeval.Report, err error) {
		startedAt := time.Now()
		operation := attribute.String(operationAttribute, operationEvaluate)
		spanCtx, span := m.tracer.Start(ctx, spanName,
			trace.WithSpanKind(trace.SpanKindInternal),
			trace.WithTimestamp(startedAt),
			trace.WithAttributes(operation, identity),
		)
		defer errortelemetry.Finish(&err, func(observedError error) {
			finishedAt := time.Now()
			defer span.End(trace.WithTimestamp(finishedAt))
			attributes := []attribute.KeyValue{operation, identity}
			if observedError == nil {
				span.SetAttributes(reportAttributes(report)...)
				attributes = append(attributes, metricIdentityAttributes(report.Metric)...)
			} else {
				errorType := errorTypeAttribute(observedError)
				errortelemetry.Record(span, errorType, trace.WithTimestamp(finishedAt))
				attributes = append(attributes, errorType)
			}
			m.duration.Record(
				spanCtx,
				finishedAt.Sub(startedAt).Seconds(),
				metric.WithAttributes(attributes...),
			)
		})
		report, err = evaluator.Evaluate(spanCtx, subject)
		if err == nil {
			err = report.Validate()
		}
		if err != nil {
			return coreeval.Report{}, err
		}
		return report, nil
	})
	return next, nil
}

func reportAttributes(report coreeval.Report) []attribute.KeyValue {
	attributes := metricIdentityAttributes(report.Metric)
	if report.Decision != nil {
		attributes = append(attributes,
			attribute.String(verdictAttribute, string(report.Decision.Verdict)),
			attribute.String(decisionPolicyAttribute, report.Decision.Policy),
		)
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
