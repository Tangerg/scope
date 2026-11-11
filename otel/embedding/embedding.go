// Package embedding instruments Core embedding calls with OpenTelemetry.
package embedding

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

	coreembedding "github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/otel/errortelemetry"
	"github.com/Tangerg/scope/otel/internal/genaimetric"
	"github.com/Tangerg/scope/otel/internal/instrument"
)

const (
	instrumentationName = "github.com/Tangerg/scope/otel/embedding"
	errorInvalidRequest = "embedding.invalid_request"
	errorInvalidOutput  = "embedding.invalid_response"
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
		spanCtx, span, startedAt := instrument.StartClientSpan(ctx, m.client.Tracer, m.spanName(request), m.requestAttributes(request)...)
		defer errortelemetry.Finish(&err, func(observedError error) {
			m.finish(spanCtx, span, request, response, observedError, startedAt, time.Now())
		})
		response, err = next.Call(spanCtx, request)
		return response, err
	})
}

func (m Middleware) spanName(request *coreembedding.Request) string {
	if request == nil || request.Options.Model == "" {
		return string(genaiconv.OperationNameEmbeddings)
	}
	return string(genaiconv.OperationNameEmbeddings) + " " + request.Options.Model
}

func (m Middleware) requestAttributes(request *coreembedding.Request) []attribute.KeyValue {
	attributes := []attribute.KeyValue{
		semconv.GenAIOperationNameKey.String(string(genaiconv.OperationNameEmbeddings)),
		semconv.GenAIProviderNameKey.String(m.client.Provider),
	}
	if request == nil {
		return attributes
	}
	if request.Options.Model != "" {
		attributes = append(attributes, semconv.GenAIRequestModel(request.Options.Model))
	}
	if request.Options.Dimensions != nil {
		attributes = append(attributes, semconv.GenAIEmbeddingsDimensionCountKey.Int64(*request.Options.Dimensions))
	}
	return append(attributes, instrument.InputCountKey.Int(len(request.Texts)))
}

func (m Middleware) finish(
	ctx context.Context,
	span trace.Span,
	request *coreembedding.Request,
	response *coreembedding.Response,
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
			genaiconv.OperationNameEmbeddings,
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
	m.client.Duration.Record(ctx, finishedAt.Sub(startedAt).Seconds(),
		genaiconv.OperationNameEmbeddings,
		genaiconv.ProviderNameAttr(m.client.Provider),
		attributes...,
	)
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
	if model != "" {
		attributes = append(attributes, semconv.GenAIResponseModel(model))
	}
	return attributes
}

func errorTypeAttribute(err error) attribute.KeyValue {
	return errortelemetry.Classify(err,
		errortelemetry.Class{Err: coreembedding.ErrInvalidRequest, Type: errorInvalidRequest},
		errortelemetry.Class{Err: coreembedding.ErrInvalidOptions, Type: errorInvalidRequest},
		errortelemetry.Class{Err: coreembedding.ErrInvalidResponse, Type: errorInvalidOutput},
	)
}
