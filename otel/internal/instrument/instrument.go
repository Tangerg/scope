// Package instrument owns the rules every Scope instrumentation shares: which
// OpenTelemetry providers it records through, how configured identities become
// telemetry dimensions, and the attributes Scope defines beyond the semantic
// conventions.
package instrument

import (
	"context"
	"strings"
	"time"

	"github.com/samber/lo"
	apiotel "go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// InputCountKey counts the inputs of one batched GenAI request, which the
// semantic conventions do not yet name.
const InputCountKey = attribute.Key("gen_ai.request.input.count")

// TracerProvider resolves nil to the process global, as the OpenTelemetry API
// specifies for instrumentation libraries.
func TracerProvider(provider trace.TracerProvider) trace.TracerProvider {
	if lo.IsNil(provider) {
		return apiotel.GetTracerProvider()
	}
	return provider
}

// MeterProvider resolves nil to the process global.
func MeterProvider(provider metric.MeterProvider) metric.MeterProvider {
	if lo.IsNil(provider) {
		return apiotel.GetMeterProvider()
	}
	return provider
}

// LoggerProvider resolves nil to the process global.
func LoggerProvider(provider log.LoggerProvider) log.LoggerProvider {
	if lo.IsNil(provider) {
		return apiotel.GetLoggerProvider()
	}
	return provider
}

// Dimension normalizes a configured identity, such as a provider or database
// system name, so equal identities produce one telemetry dimension.
func Dimension(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

// StartClientSpan gives a client operation's span and duration measurement the
// same start instant. Capability-specific attributes and completion stay local.
func StartClientSpan(ctx context.Context, tracer trace.Tracer, name string, attributes ...attribute.KeyValue) (context.Context, trace.Span, time.Time) {
	startedAt := time.Now()
	spanCtx, span := tracer.Start(ctx, name,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithTimestamp(startedAt),
		trace.WithAttributes(attributes...),
	)
	return spanCtx, span, startedAt
}
