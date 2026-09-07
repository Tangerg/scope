package a2a

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
	"go.opentelemetry.io/otel/trace"
)

// a2aTracer is the package tracer. Spans are no-ops unless the application
// installs a TracerProvider; the a2a layer takes whatever is globally
// configured rather than accepting one by DI.
var a2aTracer = otel.Tracer("github.com/Tangerg/scope/a2a")

// attrAgentName tags an A2A client span with the remote agent's name —
// brand-neutral GenAI semconv.
const attrAgentName = "gen_ai.agent.name"

// attrTaskID / attrContextID tag a server span with the A2A task identity
// (no semconv covers A2A tasks, so bare domain keys per the repo's
// observability convention).
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
