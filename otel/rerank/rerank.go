// Package rerank instruments Core reranking calls with OpenTelemetry.
package rerank

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/samber/lo"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/metric"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
	"go.opentelemetry.io/otel/semconv/v1.41.0/genaiconv"
	"go.opentelemetry.io/otel/trace"

	corererank "github.com/Tangerg/scope/core/rerank"
	"github.com/Tangerg/scope/otel/errortelemetry"
	"github.com/Tangerg/scope/otel/internal/genaimetric"
	"github.com/Tangerg/scope/otel/internal/instrument"
)

const (
	instrumentationName = "github.com/Tangerg/scope/otel/rerank"
	operationName       = "rerank"
	documentCountKey    = attribute.Key("gen_ai.request.document.count")
	errorInvalidRequest = "rerank.invalid_request"
	errorInvalidOutput  = "rerank.invalid_response"
)

var (
	ErrInvalidConfig = errors.New("otel/rerank: invalid config")
)

// Nil providers use the corresponding OpenTelemetry globals.
type MiddlewareConfig struct {
	Provider       string
	TracerProvider trace.TracerProvider
	MeterProvider  metric.MeterProvider
	// LoggerProvider receives GenAI exception events. Nil uses the global provider.
	LoggerProvider log.LoggerProvider
}

func (m MiddlewareConfig) Validate() error {
	if strings.TrimSpace(m.Provider) == "" {
		return fmt.Errorf("%w: provider is required", ErrInvalidConfig)
	}
	return nil
}

// Middleware is an immutable reranking instrumentation decorator.
type Middleware struct {
	client instrument.Client
	tokens genaiconv.ClientTokenUsage
}

func NewMiddleware(config MiddlewareConfig) (Middleware, error) {
	if err := config.Validate(); err != nil {
		return Middleware{}, err
	}
	client, err := instrument.NewClient(instrument.ClientConfig{
		InstrumentationName: instrumentationName, Provider: config.Provider,
		TracerProvider: config.TracerProvider, MeterProvider: config.MeterProvider, LoggerProvider: config.LoggerProvider,
	})
	if err != nil {
		return Middleware{}, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}
	tokens, err := genaiconv.NewClientTokenUsage(client.Meter, genaimetric.TokenBuckets())
	if err != nil {
		return Middleware{}, fmt.Errorf("%w: create token histogram: %w", ErrInvalidConfig, err)
	}
	return Middleware{client: client, tokens: tokens}, nil
}

// Call instruments next without changing its response or error.
func (m Middleware) Call(next corererank.Model) corererank.Model {
	if lo.IsNil(next) {
		return nil
	}
	if !m.client.Ready() || lo.IsNil(m.tokens.Inst()) {
		err := fmt.Errorf("%w: middleware must be constructed with NewMiddleware", ErrInvalidConfig)
		return corererank.ModelFunc(func(context.Context, *corererank.Request) (*corererank.Response, error) { return nil, err })
	}
	return corererank.ModelFunc(func(ctx context.Context, request *corererank.Request) (response *corererank.Response, err error) {
		spanCtx, span, startedAt := instrument.StartClientSpan(ctx, m.client.Tracer, m.spanName(request), m.requestAttributes(request)...)
		defer errortelemetry.Finish(&err, func(observedError error) {
			m.finish(spanCtx, span, request, response, observedError, startedAt, time.Now())
		})
		response, err = next.Call(spanCtx, request)
		return response, err
	})
}

func (m Middleware) spanName(request *corererank.Request) string {
	if request == nil || request.Options.Model == "" {
		return operationName
	}
	return operationName + " " + request.Options.Model
}

func (m Middleware) requestAttributes(request *corererank.Request) []attribute.KeyValue {
	attributes := []attribute.KeyValue{
		semconv.GenAIOperationNameKey.String(operationName),
		semconv.GenAIProviderNameKey.String(m.client.Provider),
	}
	if request == nil {
		return attributes
	}
	if request.Options.Model != "" {
		attributes = append(attributes, semconv.GenAIRequestModel(request.Options.Model))
	}
	return append(attributes, documentCountKey.Int(len(request.Documents)))
}

func (m Middleware) finish(
	ctx context.Context,
	span trace.Span,
	request *corererank.Request,
	response *corererank.Response,
	err error,
	startedAt, finishedAt time.Time,
) {
	defer span.End(trace.WithTimestamp(finishedAt))
	attributes := m.metricAttributes(request, response)
	if response != nil && response.Metadata != nil && response.Metadata.Model != "" {
		span.SetAttributes(semconv.GenAIResponseModel(response.Metadata.Model))
	}
	if response != nil && response.Metadata != nil && response.Metadata.Usage != nil &&
		response.Metadata.Usage.InputTokens >= 0 {
		span.SetAttributes(semconv.GenAIUsageInputTokensKey.Int64(response.Metadata.Usage.InputTokens))
		m.tokens.Record(ctx, response.Metadata.Usage.InputTokens,
			genaiconv.OperationNameAttr(operationName),
			genaiconv.ProviderNameAttr(m.client.Provider),
			genaiconv.TokenTypeInput,
			attributes...,
		)
	}

	if err != nil {
		errorType := errorTypeAttribute(err)
		errortelemetry.Record(span, errorType, trace.WithTimestamp(finishedAt))
		errortelemetry.EmitGenAIException(ctx, m.client.Logger, errorType, finishedAt)
		attributes = append(attributes, errorType)
	}
	operation := genaiconv.OperationNameAttr(operationName)
	m.client.Duration.Record(ctx, finishedAt.Sub(startedAt).Seconds(), operation, genaiconv.ProviderNameAttr(m.client.Provider), attributes...)
}

func (m Middleware) metricAttributes(request *corererank.Request, response *corererank.Response) []attribute.KeyValue {
	var attributes []attribute.KeyValue
	if request != nil && request.Options.Model != "" {
		attributes = append(attributes, semconv.GenAIRequestModel(request.Options.Model))
	}
	model := ""
	if response != nil && response.Metadata != nil {
		model = response.Metadata.Model
	}
	if model != "" {
		attributes = append(attributes, semconv.GenAIResponseModel(model))
	}
	return attributes
}

func errorTypeAttribute(err error) attribute.KeyValue {
	return errortelemetry.Classify(err,
		errortelemetry.Class{Err: corererank.ErrInvalidRequest, Type: errorInvalidRequest},
		errortelemetry.Class{Err: corererank.ErrInvalidOptions, Type: errorInvalidRequest},
		errortelemetry.Class{Err: corererank.ErrInvalidResponse, Type: errorInvalidOutput},
	)
}
