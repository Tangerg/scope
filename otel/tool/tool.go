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
	"github.com/Tangerg/scope/otel/internal/genaimetric"
)

const (
	instrumentationName = "github.com/Tangerg/scope/otel/tool"
	operationName       = "execute_tool"
	toolTypeFunction    = "function"
	durationMetricName  = "gen_ai.execute_tool.duration"
	durationUnit        = "s"
)

var (
	ErrInvalidConfig = errors.New("otel/tool: invalid config")
	ErrInvalidTool   = errors.New("otel/tool: invalid tool")
)

// Nil providers use the corresponding OpenTelemetry globals.
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
		genaimetric.DurationBuckets(),
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

func (i *instrumentedTool) Call(ctx context.Context, invocation coretool.Invocation) (result chat.ToolOutput, err error) {
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
	defer errortelemetry.Finish(&err, func(observedError error) {
		finishedAt := time.Now()
		defer span.End(trace.WithTimestamp(finishedAt))
		if observedError != nil {
			errorType := errortelemetry.Classify(observedError)
			errortelemetry.Record(span, errorType, trace.WithTimestamp(finishedAt))
			attributes = append(attributes, errorType)
		}
		i.middleware.duration.Record(
			ctx,
			finishedAt.Sub(startedAt).Seconds(),
			metric.WithAttributes(attributes...),
		)
	})
	return i.next.Call(ctx, invocation)
}

var _ coretool.WrappingTool = (*instrumentedTool)(nil)
