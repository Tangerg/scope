// Package tool instruments Core tool execution with OpenTelemetry.
package tool

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	apiotel "go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/chat"
	coretool "github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/otel/internal/errortelemetry"
)

const (
	instrumentationName = "github.com/Tangerg/scope/otel/tool"
	operationName       = "execute_tool"
	toolTypeFunction    = "function"
	durationMetricName  = "gen_ai.execute_tool.duration"
	durationUnit        = "s"
	errorTypeCanceled   = "context.canceled"
	errorTypeDeadline   = "context.deadline_exceeded"
)

var (
	ErrInvalidConfig = errors.New("otel/tool: invalid config")
	ErrInvalidTool   = errors.New("otel/tool: invalid tool")
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

// Middleware instruments exact Tool.Call boundaries without recording model
// arguments or results. It is immutable after construction and safe for
// concurrent use.
type Middleware struct {
	tracer   trace.Tracer
	duration metric.Float64Histogram
}

// NewMiddleware fixes instrument identity and provider binding once so every
// tool invocation contributes to the same telemetry series.
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
		durationMetricName,
		metric.WithDescription("GenAI tool execution duration."),
		metric.WithUnit(durationUnit),
		metric.WithExplicitBucketBoundaries(0.01, 0.02, 0.04, 0.08, 0.16, 0.32, 0.64, 1.28, 2.56, 5.12, 10.24, 20.48, 40.96, 81.92),
	)
	if err != nil {
		return Middleware{}, fmt.Errorf("%w: create duration histogram: %w", ErrInvalidConfig, err)
	}
	return Middleware{
		tracer: tracerProvider.Tracer(instrumentationName), duration: duration,
	}, nil
}

// Wrap freezes the Tool definition used for both execution identity and model
// exposure. Optional capabilities remain discoverable through Tool.Unwrap.
func (m Middleware) Wrap(next coretool.Tool) (coretool.Tool, error) {
	if lo.IsNil(m.tracer) || lo.IsNil(m.duration) {
		return nil, fmt.Errorf("%w: middleware must be constructed with NewMiddleware", ErrInvalidConfig)
	}
	if lo.IsNil(next) {
		return nil, fmt.Errorf("%w: value must not be nil", ErrInvalidTool)
	}
	definition := next.Definition().Clone()
	if err := definition.Validate(); err != nil {
		return nil, fmt.Errorf("%w: definition: %w", ErrInvalidTool, err)
	}
	return &instrumentedTool{middleware: m, next: next, definition: definition}, nil
}

type instrumentedTool struct {
	middleware Middleware
	next       coretool.Tool
	definition chat.ToolDefinition
}

func (i *instrumentedTool) Definition() chat.ToolDefinition {
	return i.definition.Clone()
}

func (i *instrumentedTool) Unwrap() coretool.Tool { return i.next }

func (i *instrumentedTool) Call(ctx context.Context, invocation coretool.Invocation) (chat.ToolOutput, error) {
	attributes := []attribute.KeyValue{
		semconv.GenAIOperationNameExecuteTool,
		semconv.GenAIToolName(i.definition.Name),
		semconv.GenAIToolType(toolTypeFunction),
	}
	startedAt := time.Now()
	ctx, span := i.middleware.tracer.Start(
		ctx,
		strings.Join([]string{operationName, i.definition.Name}, " "),
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithTimestamp(startedAt),
		trace.WithAttributes(attributes...),
	)
	result, err := i.next.Call(ctx, invocation)
	finishedAt := time.Now()
	if err != nil {
		errorType := errorTypeAttribute(err)
		errortelemetry.Record(span, errorType, trace.WithTimestamp(finishedAt))
		attributes = append(attributes, errorType)
	}
	span.End(trace.WithTimestamp(finishedAt))
	i.middleware.duration.Record(
		ctx,
		finishedAt.Sub(startedAt).Seconds(),
		metric.WithAttributes(attributes...),
	)
	return result, err
}

func errorTypeAttribute(err error) attribute.KeyValue {
	switch {
	case errors.Is(err, context.Canceled):
		return semconv.ErrorTypeKey.String(errorTypeCanceled)
	case errors.Is(err, context.DeadlineExceeded):
		return semconv.ErrorTypeKey.String(errorTypeDeadline)
	default:
		return semconv.ErrorType(err)
	}
}

var _ coretool.WrappingTool = (*instrumentedTool)(nil)
