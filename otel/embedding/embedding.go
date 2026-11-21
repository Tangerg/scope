// Package embedding instruments Core embedding calls with OpenTelemetry.
package embedding

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

	coreembedding "github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/otel/errortelemetry"
	"github.com/Tangerg/scope/otel/internal/genaimetric"
	"github.com/Tangerg/scope/otel/internal/instrument"
)

const (
	instrumentationName      = "github.com/Tangerg/scope/otel/embedding"
	errorTypeInvalidRequest  = "embedding.invalid_request"
	errorTypeInvalidOptions  = "embedding.invalid_options"
	errorTypeInvalidResponse = "embedding.invalid_response"
)

var (
	ErrInvalidConfig = errors.New("otel/embedding: invalid config")
)

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

// Middleware is an immutable embedding instrumentation decorator.
type Middleware struct {
	client instrument.Client
	tokens genaiconv.ClientTokenUsage
}

// NewMiddleware snapshots provider identity and resolves nil OTel providers to
// the official process globals.
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
func (m Middleware) Call(next coreembedding.Model) coreembedding.Model {
	if lo.IsNil(next) {
		return nil
	}
	if !m.client.Ready() || lo.IsNil(m.tokens.Inst()) {
		err := fmt.Errorf("%w: middleware must be constructed with NewMiddleware", ErrInvalidConfig)
		return coreembedding.ModelFunc(func(context.Context, *coreembedding.Request) (*coreembedding.Response, error) { return nil, err })
	}
	return coreembedding.ModelFunc(func(ctx context.Context, request *coreembedding.Request) (response *coreembedding.Response, err error) {
		spanCtx, operation := m.client.StartOperation(ctx, genaiconv.OperationNameEmbeddings, requestModel(request), requestAttributes(request)...)
		defer errortelemetry.Finish(&err, func(observedError error) {
			m.finish(spanCtx, operation, response, observedError)
		})
		response, err = next.Call(spanCtx, request)
		return response, err
	})
}

func requestModel(request *coreembedding.Request) string {
	if request == nil {
		return ""
	}
	return request.Options.Model
}

func requestAttributes(request *coreembedding.Request) []attribute.KeyValue {
	if request == nil {
		return nil
	}
	var attributes []attribute.KeyValue
	if request.Options.Dimensions != nil {
		attributes = append(attributes, semconv.GenAIEmbeddingsDimensionCountKey.Int64(*request.Options.Dimensions))
	}
	return append(attributes, instrument.InputCountKey.Int(len(request.Texts)))
}

func (m Middleware) finish(ctx context.Context, operation *instrument.Operation, response *coreembedding.Response, err error) {
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
		errortelemetry.Class{Err: coreembedding.ErrInvalidRequest, Type: errorTypeInvalidRequest},
		errortelemetry.Class{Err: coreembedding.ErrInvalidOptions, Type: errorTypeInvalidOptions},
		errortelemetry.Class{Err: coreembedding.ErrInvalidResponse, Type: errorTypeInvalidResponse},
	)
}
