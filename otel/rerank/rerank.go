// Package rerank instruments Core reranking calls with OpenTelemetry.
package rerank

import (
	"context"
	"errors"
	"fmt"
	"strings"

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
	instrumentationName                                  = "github.com/Tangerg/scope/otel/rerank"
	operationName            genaiconv.OperationNameAttr = "rerank"
	documentCountKey                                     = attribute.Key("gen_ai.request.document.count")
	errorTypeInvalidRequest                              = "rerank.invalid_request"
	errorTypeInvalidOptions                              = "rerank.invalid_options"
	errorTypeInvalidResponse                             = "rerank.invalid_response"
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
		ClassifyError: errorTypeAttribute,
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
		spanCtx, operation := m.client.StartOperation(ctx, operationName, requestModel(request), requestAttributes(request)...)
		defer errortelemetry.Finish(&err, func(observedError error) {
			m.finish(spanCtx, operation, response, observedError)
		})
		response, err = next.Call(spanCtx, request)
		return response, err
	})
}

func requestModel(request *corererank.Request) string {
	if request == nil {
		return ""
	}
	return request.Options.Model
}

func requestAttributes(request *corererank.Request) []attribute.KeyValue {
	if request == nil {
		return nil
	}
	return []attribute.KeyValue{documentCountKey.Int(len(request.Documents))}
}

func (m Middleware) finish(ctx context.Context, operation *instrument.Operation, response *corererank.Response, err error) {
	if response != nil && response.Metadata != nil {
		operation.ObserveResponseModel(response.Metadata.Model)
		if usage := response.Metadata.Usage; usage != nil && usage.InputTokens >= 0 {
			operation.SetAttributes(semconv.GenAIUsageInputTokensKey.Int64(usage.InputTokens))
			operation.RecordTokens(ctx, m.tokens, genaiconv.TokenTypeInput, usage.InputTokens)
		}
	}
	operation.End(ctx, err)
}

func errorTypeAttribute(err error) attribute.KeyValue {
	return errortelemetry.Classify(err,
		errortelemetry.Class{Err: corererank.ErrInvalidRequest, Type: errorTypeInvalidRequest},
		errortelemetry.Class{Err: corererank.ErrInvalidOptions, Type: errorTypeInvalidOptions},
		errortelemetry.Class{Err: corererank.ErrInvalidResponse, Type: errorTypeInvalidResponse},
	)
}
