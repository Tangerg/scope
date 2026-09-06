// Package image instruments Core image-generation calls with OpenTelemetry.
package image

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/samber/lo"
	apiotel "go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/metric"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
	"go.opentelemetry.io/otel/semconv/v1.41.0/genaiconv"
	"go.opentelemetry.io/otel/trace"

	coreimage "github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/otel/internal/errortelemetry"
)

const (
	instrumentationName = "github.com/Tangerg/scope/otel/image"
	operationName       = "generate_content"
	errorCanceled       = "context.canceled"
	errorDeadline       = "context.deadline_exceeded"
	errorInvalidRequest = "image.invalid_request"
	errorInvalidOutput  = "image.invalid_response"
)

var (
	ErrInvalidConfig = errors.New("otel/image: invalid config")
	ErrInvalidModel  = errors.New("otel/image: invalid model")
)

// MiddlewareConfig identifies the provider and optional OTel providers used by
// image instrumentation.
type MiddlewareConfig struct {
	Provider       string
	TracerProvider trace.TracerProvider
	MeterProvider  metric.MeterProvider
	// LoggerProvider receives GenAI exception events. Nil uses the global provider.
	LoggerProvider log.LoggerProvider
}

// Validate checks construction inputs without resolving global providers.
func (m MiddlewareConfig) Validate() error {
	if strings.TrimSpace(m.Provider) == "" {
		return fmt.Errorf("%w: provider is required", ErrInvalidConfig)
	}
	return nil
}

// Middleware is an immutable image-generation instrumentation decorator.
type Middleware struct {
	logger   log.Logger
	provider string
	tracer   trace.Tracer
	duration genaiconv.ClientOperationDuration
}

// NewMiddleware snapshots provider identity and resolves nil OTel providers to
// the official process globals.
func NewMiddleware(config MiddlewareConfig) (Middleware, error) {
	if err := config.Validate(); err != nil {
		return Middleware{}, err
	}
	tracerProvider := config.TracerProvider
	if lo.IsNil(tracerProvider) {
		tracerProvider = apiotel.GetTracerProvider()
	}
	loggerProvider := config.LoggerProvider
	if lo.IsNil(loggerProvider) {
		loggerProvider = global.GetLoggerProvider()
	}
	meterProvider := config.MeterProvider
	if lo.IsNil(meterProvider) {
		meterProvider = apiotel.GetMeterProvider()
	}
	duration, err := genaiconv.NewClientOperationDuration(meterProvider.Meter(instrumentationName), metric.WithExplicitBucketBoundaries(
		0.01, 0.02, 0.04, 0.08, 0.16, 0.32, 0.64, 1.28, 2.56, 5.12, 10.24, 20.48, 40.96, 81.92,
	))
	if err != nil {
		return Middleware{}, fmt.Errorf("%w: create duration histogram: %w", ErrInvalidConfig, err)
	}
	return Middleware{
		logger:   loggerProvider.Logger(instrumentationName),
		provider: strings.ToLower(strings.TrimSpace(config.Provider)),
		tracer:   tracerProvider.Tracer(instrumentationName),
		duration: duration,
	}, nil
}

// Wrap decorates one image Model without observing prompts or generated media.
func (m Middleware) Wrap(next coreimage.Model) (coreimage.Model, error) {
	if lo.IsNil(m.logger) || lo.IsNil(m.tracer) || lo.IsNil(m.duration.Inst()) {
		return nil, fmt.Errorf("%w: middleware must be constructed with NewMiddleware", ErrInvalidConfig)
	}
	if lo.IsNil(next) {
		return nil, fmt.Errorf("%w: value must not be nil", ErrInvalidModel)
	}
	return coreimage.ModelFunc(func(ctx context.Context, request *coreimage.Request) (*coreimage.Response, error) {
		startedAt := time.Now()
		attributes := m.requestAttributes(request)
		spanCtx, span := m.tracer.Start(ctx, m.spanName(request),
			trace.WithSpanKind(trace.SpanKindClient),
			trace.WithTimestamp(startedAt),
			trace.WithAttributes(attributes...),
		)
		response, err := next.Call(spanCtx, request)
		finishedAt := time.Now()
		defer span.End(trace.WithTimestamp(finishedAt))
		metricAttributes := m.metricAttributes(request)
		if err != nil {
			errorType := errorTypeAttribute(err)
			errortelemetry.Record(span, errorType, trace.WithTimestamp(finishedAt))
			errortelemetry.EmitGenAIException(spanCtx, m.logger, errorType, finishedAt)
			metricAttributes = append(metricAttributes, errorType)
		}
		m.duration.Record(spanCtx, finishedAt.Sub(startedAt).Seconds(),
			genaiconv.OperationNameGenerateContent,
			genaiconv.ProviderNameAttr(m.provider),
			metricAttributes...,
		)
		return response, err
	}), nil
}

func (m Middleware) spanName(request *coreimage.Request) string {
	if request == nil || request.Options.Model == "" {
		return operationName
	}
	return operationName + " " + request.Options.Model
}

func (m Middleware) requestAttributes(request *coreimage.Request) []attribute.KeyValue {
	attributes := []attribute.KeyValue{
		semconv.GenAIOperationNameGenerateContent,
		semconv.GenAIOutputTypeImage,
		semconv.GenAIProviderNameKey.String(m.provider),
	}
	if request != nil && request.Options.Model != "" {
		attributes = append(attributes, semconv.GenAIRequestModel(request.Options.Model))
	}
	return attributes
}

func (m Middleware) metricAttributes(request *coreimage.Request) []attribute.KeyValue {
	if request == nil || request.Options.Model == "" {
		return nil
	}
	return []attribute.KeyValue{
		semconv.GenAIRequestModel(request.Options.Model),
	}
}

func errorTypeAttribute(err error) attribute.KeyValue {
	switch {
	case errors.Is(err, context.Canceled):
		return semconv.ErrorTypeKey.String(errorCanceled)
	case errors.Is(err, context.DeadlineExceeded):
		return semconv.ErrorTypeKey.String(errorDeadline)
	case errors.Is(err, coreimage.ErrInvalidRequest), errors.Is(err, coreimage.ErrInvalidOptions):
		return semconv.ErrorTypeKey.String(errorInvalidRequest)
	case errors.Is(err, coreimage.ErrInvalidResponse):
		return semconv.ErrorTypeKey.String(errorInvalidOutput)
	default:
		return semconv.ErrorType(err)
	}
}
