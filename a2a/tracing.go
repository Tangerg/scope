package a2a

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
	"go.opentelemetry.io/otel/trace"
)

var a2aTracer = otel.Tracer("github.com/Tangerg/scope/a2a")

const attrAgentName = "gen_ai.agent.name"

const (
	attrTaskID    = "task.id"
	attrContextID = "context.id"
)

// Raw remote errors can contain business content; telemetry retains only their classification.
func recordSpanError(span trace.Span, err error) {
	classification := semconv.ErrorType(err)
	switch {
	case errors.Is(err, context.Canceled):
		classification = semconv.ErrorTypeKey.String("context.canceled")
	case errors.Is(err, context.DeadlineExceeded):
		classification = semconv.ErrorTypeKey.String("context.deadline_exceeded")
	}
	span.SetAttributes(classification)
	span.SetStatus(codes.Error, classification.Value.AsString())
	span.AddEvent("exception", trace.WithAttributes(semconv.ExceptionType(classification.Value.AsString())))
}
