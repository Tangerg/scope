package instrument

import (
	"context"
	"slices"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/Tangerg/scope/otel/errortelemetry"
)

// Measurement is one operation observed as a span and as a duration over the
// same two instants. Every Scope instrumentation ends its operations through
// End, so a failure always marks the span and becomes the duration's
// error.type.
type Measurement struct {
	span      trace.Span
	startedAt time.Time
}

func StartMeasurement(
	ctx context.Context,
	tracer trace.Tracer,
	name string,
	kind trace.SpanKind,
	attributes ...attribute.KeyValue,
) (context.Context, Measurement) {
	startedAt := time.Now()
	spanCtx, span := tracer.Start(ctx, name,
		trace.WithSpanKind(kind),
		trace.WithTimestamp(startedAt),
		trace.WithAttributes(attributes...),
	)
	return spanCtx, Measurement{span: span, startedAt: startedAt}
}

func (m Measurement) SetAttributes(attributes ...attribute.KeyValue) {
	m.span.SetAttributes(attributes...)
}

// End records errorType, when it is a classification, on the span and the
// duration, then ends the span. It returns the instant the operation ended.
func (m Measurement) End(
	ctx context.Context,
	duration metric.Float64Histogram,
	errorType attribute.KeyValue,
	attributes ...attribute.KeyValue,
) time.Time {
	finishedAt := time.Now()
	if errorType.Valid() {
		errortelemetry.Record(m.span, errorType, trace.WithTimestamp(finishedAt))
		attributes = append(slices.Clip(attributes), errorType)
	}
	duration.Record(ctx, finishedAt.Sub(m.startedAt).Seconds(), metric.WithAttributes(attributes...))
	m.span.End(trace.WithTimestamp(finishedAt))
	return finishedAt
}
