package eval

import (
	"context"
	"errors"
	"fmt"

	"github.com/samber/lo"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	coreeval "github.com/Tangerg/scope/eval"
	"github.com/Tangerg/scope/otel/errortelemetry"
	"github.com/Tangerg/scope/otel/internal/instrument"
)

const (
	instrumentationName             = "github.com/Tangerg/scope/otel/eval"
	spanName                        = "eval.evaluate"
	operationDurationMetric         = "eval.operation.duration"
	operationDurationDescription    = "Eval operation duration."
	operationDurationUnit           = "s"
	operationAttribute              = "eval.operation.name"
	assessmentIDAttribute           = "eval.assessment.id"
	metricNamespaceAttribute        = "eval.metric.namespace"
	metricNameAttribute             = "eval.metric.name"
	verdictAttribute                = "eval.verdict"
	decisionPolicyAttribute         = "eval.decision.policy"
	scoreAttribute                  = "eval.score"
	measurementAttribute            = "eval.measurement"
	operationEvaluate               = "evaluate"
	errorTypeInvalidEvaluatorConfig = "eval.invalid_evaluator_config"
	errorTypeInvalidReport          = "eval.invalid_report"
)

var ErrInvalidConfig = errors.New("otel/eval: invalid config")

type MiddlewareConfig struct {
	TracerProvider trace.TracerProvider
	MeterProvider  metric.MeterProvider
}

// Middleware binds the subject type once; Evaluate decorates assessments of
// that type.
type Middleware[T any] struct {
	tracer   trace.Tracer
	duration metric.Float64Histogram
}

// NewMiddleware resolves nil OTel providers to the official process globals.
func NewMiddleware[T any](config MiddlewareConfig) (Middleware[T], error) {
	tracerProvider := instrument.TracerProvider(config.TracerProvider)
	meterProvider := instrument.MeterProvider(config.MeterProvider)
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

// Evaluate decorates one assessment under the same identity without observing
// or retaining its subject. Assessment identity is bound before evaluation,
// including failed attempts. IDs identify configured assessments and must not
// contain per-case data. An unconstructed Middleware yields an assessment
// whose every evaluation fails with ErrInvalidConfig; an assessment without a
// valid identity is returned unchanged and keeps failing its own validation.
func (m Middleware[T]) Evaluate(next coreeval.Assessment[T]) coreeval.Assessment[T] {
	assessment, err := coreeval.NewAssessment(coreeval.AssessmentConfig[T]{ID: next.ID(), Evaluator: m.observe(next)})
	if err != nil {
		return next
	}
	return assessment
}

// observe returns the evaluator that spans and measures each evaluation of
// next.
func (m Middleware[T]) observe(next coreeval.Assessment[T]) coreeval.Evaluator[T] {
	if lo.IsNil(m.tracer) || lo.IsNil(m.duration) {
		err := fmt.Errorf("%w: middleware must be constructed with NewMiddleware", ErrInvalidConfig)
		return coreeval.EvaluatorFunc[T](func(context.Context, T) (coreeval.Report, error) { return coreeval.Report{}, err })
	}
	identity := attribute.String(assessmentIDAttribute, string(next.ID()))
	return coreeval.EvaluatorFunc[T](func(ctx context.Context, subject T) (report coreeval.Report, err error) {
		operation := attribute.String(operationAttribute, operationEvaluate)
		spanCtx, measurement := instrument.StartMeasurement(ctx, m.tracer, spanName, trace.SpanKindInternal, operation, identity)
		defer errortelemetry.Finish(&err, func(observedError error) {
			attributes := []attribute.KeyValue{operation, identity}
			if observedError == nil {
				measurement.SetAttributes(reportAttributes(report)...)
				attributes = append(attributes, metricIdentityAttributes(report.Metric)...)
			}
			measurement.End(spanCtx, m.duration, errorTypeAttribute(observedError), attributes...)
		})
		report, err = next.Evaluate(spanCtx, subject)
		if err != nil {
			return coreeval.Report{}, err
		}
		return report, nil
	})
}

func reportAttributes(report coreeval.Report) []attribute.KeyValue {
	attributes := metricIdentityAttributes(report.Metric)
	if report.Decision != nil {
		attributes = append(attributes,
			attribute.String(verdictAttribute, string(report.Decision.Verdict)),
			attribute.String(decisionPolicyAttribute, report.Decision.Policy.String()),
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
	return errortelemetry.Classify(err,
		errortelemetry.Class{Err: coreeval.ErrInvalidEvaluatorConfig, Type: errorTypeInvalidEvaluatorConfig},
		errortelemetry.Class{Err: coreeval.ErrInvalidReport, Type: errorTypeInvalidReport},
	)
}
