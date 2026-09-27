// Package errortelemetry owns the content-free projection of classified failures.
package errortelemetry

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/log"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
	"go.opentelemetry.io/otel/trace"
)

const (
	exceptionEvent      = "exception"
	genAIExceptionEvent = "gen_ai.client.operation.exception"
	panicClassification = "panic"
)

type panicError struct{}

func (panicError) Error() string { return panicClassification }

func (panicError) ErrorType() string { return panicClassification }

// Finish must be deferred directly by the call boundary before invoking its
// delegate. A panic is observed without inspecting its value, then propagated
// unchanged. The caller's returned error remains owned by the delegate.
func Finish(err *error, record func(error)) {
	panicked := recover()
	observedError := *err
	if panicked != nil {
		observedError = panicError{}
	}
	record(observedError)
	if panicked != nil {
		panic(panicked)
	}
}

// Record accepts only a stable classification so raw provider messages cannot
// reach span status or exception attributes through this boundary.
func Record(span trace.Span, classification attribute.KeyValue, options ...trace.EventOption) {
	span.SetAttributes(classification)
	span.SetStatus(codes.Error, classification.Value.AsString())
	options = append(options, trace.WithAttributes(semconv.ExceptionType(classification.Value.AsString())))
	span.AddEvent(exceptionEvent, options...)
}

// EmitGenAIException uses the official event model. Exception type is sufficient
// when the exception message is intentionally omitted for content isolation.
// Caller cancellation must not suppress its own diagnostic; the SDK owns export.
func EmitGenAIException(ctx context.Context, logger log.Logger, classification attribute.KeyValue, occurredAt time.Time) {
	var record log.Record
	record.SetEventName(genAIExceptionEvent)
	record.SetTimestamp(occurredAt)
	record.SetSeverity(log.SeverityWarn)
	record.SetSeverityText("WARN")
	record.AddAttributes(semconv.ExceptionType(classification.Value.AsString()))
	logger.Emit(context.WithoutCancel(ctx), record)
}
