// Package tool instruments Core tool execution with OpenTelemetry.
package tool

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/samber/lo"
	apiotel "go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
	"go.opentelemetry.io/otel/trace"

	coretool "github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/otel/errortelemetry"
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

func (m Middleware) Call(next coretool.Caller) coretool.Caller {
	return &instrumentedCaller{middleware: m, next: next}
}

type instrumentedCaller struct {
	middleware Middleware
	next       coretool.Caller
}

func (i *instrumentedCaller) Call(ctx context.Context, invocation coretool.Invocation) (result coretool.Result, err error) {
	if lo.IsNil(i.middleware.tracer) || lo.IsNil(i.middleware.duration) {
		return coretool.Result{}, ErrInvalidConfig
	}
	if lo.IsNil(i.next) {
		return coretool.Result{}, ErrInvalidTool
	}
	name := invocation.Definition().Name
	attributes := []attribute.KeyValue{
		semconv.GenAIOperationNameExecuteTool,
		semconv.GenAIToolName(name),
		semconv.GenAIToolType(toolTypeFunction),
	}
	startedAt := time.Now()
	ctx, span := i.middleware.tracer.Start(
		ctx,
		strings.Join([]string{operationName, name}, " "),
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

var _ coretool.Caller = (*instrumentedCaller)(nil)
