// Package embedding instruments Core embedding calls with OpenTelemetry.
package embedding

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

	coreembedding "github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/otel/internal/errortelemetry"
)

const (
	instrumentationName = "github.com/Tangerg/scope/otel/embedding"
	operationName       = "embeddings"
	inputCountKey       = attribute.Key("gen_ai.request.input.count")
	errorCanceled       = "context.canceled"
	errorDeadline       = "context.deadline_exceeded"
	errorInvalidRequest = "embedding.invalid_request"
	errorInvalidOutput  = "embedding.invalid_response"
)

var (
	ErrInvalidConfig = errors.New("otel/embedding: invalid config")
	ErrInvalidModel  = errors.New("otel/embedding: invalid model")
)

// MiddlewareConfig identifies the provider and optional OTel providers used by
// embedding instrumentation.
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

// Middleware is an immutable embedding instrumentation decorator.
type Middleware struct {
	logger   log.Logger
	provider string
	tracer   trace.Tracer
	duration genaiconv.ClientOperationDuration
	tokens   genaiconv.ClientTokenUsage
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
	meter := meterProvider.Meter(instrumentationName)
	duration, err := genaiconv.NewClientOperationDuration(meter)
	if err != nil {
		return Middleware{}, fmt.Errorf("%w: create duration histogram: %w", ErrInvalidConfig, err)
	}
	tokens, err := genaiconv.NewClientTokenUsage(meter)
	if err != nil {
		return Middleware{}, fmt.Errorf("%w: create token histogram: %w", ErrInvalidConfig, err)
	}
	return Middleware{
		logger:   loggerProvider.Logger(instrumentationName),
		provider: strings.ToLower(strings.TrimSpace(config.Provider)),
		tracer:   tracerProvider.Tracer(instrumentationName),
		duration: duration,
		tokens:   tokens,
	}, nil
}

// Wrap decorates one embedding Model without changing its request, response,
// or error semantics.
func (m Middleware) Wrap(next coreembedding.Model) (coreembedding.Model, error) {
	if lo.IsNil(m.logger) || lo.IsNil(m.tracer) || lo.IsNil(m.duration.Inst()) || lo.IsNil(m.tokens.Inst()) {
		return nil, fmt.Errorf("%w: middleware must be constructed with NewMiddleware", ErrInvalidConfig)
	}
	if lo.IsNil(next) {
		return nil, fmt.Errorf("%w: value must not be nil", ErrInvalidModel)
	}
	return coreembedding.ModelFunc(func(ctx context.Context, request *coreembedding.Request) (*coreembedding.Response, error) {
		startedAt := time.Now()
		attributes := m.requestAttributes(request)
		spanCtx, span := m.tracer.Start(ctx, m.spanName(request),
			trace.WithSpanKind(trace.SpanKindClient),
			trace.WithAttributes(attributes...),
		)
		response, err := next.Call(spanCtx, request)
		m.finish(spanCtx, span, request, response, err, time.Since(startedAt))
		return response, err
	}), nil
}

func (m Middleware) spanName(request *coreembedding.Request) string {
	if request == nil || request.Options.Model == "" {
		return operationName
	}
	return operationName + " " + request.Options.Model
}

func (m Middleware) requestAttributes(request *coreembedding.Request) []attribute.KeyValue {
	attributes := []attribute.KeyValue{
		semconv.GenAIOperationNameEmbeddings,
		semconv.GenAIProviderNameKey.String(m.provider),
	}
	if request == nil {
		return attributes
	}
	if request.Options.Model != "" {
		attributes = append(attributes, semconv.GenAIRequestModel(request.Options.Model))
	}
	return append(attributes, inputCountKey.Int(len(request.Texts)))
}

func (m Middleware) finish(
	ctx context.Context,
	span trace.Span,
	request *coreembedding.Request,
	response *coreembedding.Response,
	err error,
	elapsed time.Duration,
) {
	defer span.End()
	attributes := m.metricAttributes(request, response)
	if response != nil && response.Metadata != nil && response.Metadata.Model != "" {
		span.SetAttributes(semconv.GenAIResponseModel(response.Metadata.Model))
	}
	if err != nil {
		errorType := errorTypeAttribute(err)
		errortelemetry.Record(span, errorType)
		errortelemetry.EmitGenAIException(ctx, m.logger, errorType, time.Now())
		attributes = append(attributes, errorType)
	}
	m.duration.Record(ctx, elapsed.Seconds(),
		genaiconv.OperationNameEmbeddings,
		genaiconv.ProviderNameAttr(m.provider),
		attributes...,
	)
	if err == nil && response != nil && response.Metadata != nil && response.Metadata.Usage != nil &&
		response.Metadata.Usage.InputTokens > 0 {
		m.tokens.Record(ctx, response.Metadata.Usage.InputTokens,
			genaiconv.OperationNameEmbeddings,
			genaiconv.ProviderNameAttr(m.provider),
			genaiconv.TokenTypeInput,
			attributes...,
		)
	}
}

func (m Middleware) metricAttributes(
	request *coreembedding.Request,
	response *coreembedding.Response,
) []attribute.KeyValue {
	var attributes []attribute.KeyValue
	if request != nil && request.Options.Model != "" {
		attributes = append(attributes, semconv.GenAIRequestModel(request.Options.Model))
	}
	model := ""
	if response != nil && response.Metadata != nil {
		model = response.Metadata.Model
	}
	if model == "" && request != nil {
		model = request.Options.Model
	}
	if model != "" {
		attributes = append(attributes, semconv.GenAIResponseModel(model))
	}
	return attributes
}

func errorTypeAttribute(err error) attribute.KeyValue {
	switch {
	case errors.Is(err, context.Canceled):
		return semconv.ErrorTypeKey.String(errorCanceled)
	case errors.Is(err, context.DeadlineExceeded):
		return semconv.ErrorTypeKey.String(errorDeadline)
	case errors.Is(err, coreembedding.ErrInvalidRequest), errors.Is(err, coreembedding.ErrInvalidOptions):
		return semconv.ErrorTypeKey.String(errorInvalidRequest)
	case errors.Is(err, coreembedding.ErrInvalidResponse):
		return semconv.ErrorTypeKey.String(errorInvalidOutput)
	default:
		return semconv.ErrorType(err)
	}
}
