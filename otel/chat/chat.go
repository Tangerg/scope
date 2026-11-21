// Package chat instruments Core chat capabilities with OpenTelemetry.
package chat

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"strings"
	"time"

	"github.com/samber/lo"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/metric"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
	"go.opentelemetry.io/otel/semconv/v1.41.0/genaiconv"
	"go.opentelemetry.io/otel/trace"

	corechat "github.com/Tangerg/scope/core/chat"
	toolcontract "github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/otel/errortelemetry"
	"github.com/Tangerg/scope/otel/internal/genaimetric"
	"github.com/Tangerg/scope/otel/internal/instrument"
)

const (
	instrumentationName            = "github.com/Tangerg/scope/otel/chat"
	errorTypeInvalidRequest        = "chat.invalid_request"
	errorTypeInvalidResponse       = "chat.invalid_response"
	errorTypeInvalidMessage        = "chat.invalid_message"
	errorTypeInvalidPart           = "chat.invalid_part"
	errorTypeInvalidToolCall       = "chat.invalid_tool_call"
	errorTypeInvalidToolResult     = "chat.invalid_tool_result"
	errorTypeInvalidToolDefinition = "chat.invalid_tool_definition"
	errorTypeInvalidOutputFormat   = "chat.invalid_output_format"
	errorTypeInvalidOptions        = "chat.invalid_options"
	errorTypeInvalidUsage          = "chat.invalid_usage"
	errorTypeNilStream             = "chat.nil_stream"
	cacheWriteInputTokensKey       = attribute.Key("gen_ai.usage.cache_write.input_tokens")
)

var (
	ErrInvalidConfig = errors.New("otel/chat: invalid config")
)

// Provider is normalized to lowercase for stable telemetry dimensions. Nil
// signal providers use the corresponding OpenTelemetry globals.
type MiddlewareConfig struct {
	Provider       string
	TracerProvider trace.TracerProvider
	MeterProvider  metric.MeterProvider
	LoggerProvider log.LoggerProvider
}

func (m MiddlewareConfig) Validate() error {
	if strings.TrimSpace(m.Provider) == "" {
		return fmt.Errorf("%w: provider is required", ErrInvalidConfig)
	}
	return nil
}

// Middleware adds GenAI spans, metrics, and exception events to synchronous and
// streaming chat capabilities. It is immutable after construction and safe for
// concurrent use.
type Middleware struct {
	client instrument.Client
	tokens genaiconv.ClientTokenUsage
	timing instrument.ChunkTiming
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
	timing, err := instrument.NewChunkTiming(client.Meter)
	if err != nil {
		return Middleware{}, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}
	return Middleware{client: client, tokens: tokens, timing: timing}, nil
}

// Call is a [corechat.CallMiddleware]. It preserves the wrapped model's response
// and error exactly; observation is a read-only side effect.
func (m Middleware) Call(next corechat.Model) corechat.Model {
	if lo.IsNil(next) {
		return nil
	}
	if err := m.validate(); err != nil {
		return corechat.ModelFunc(func(context.Context, *corechat.Request) (*corechat.Response, error) {
			return nil, err
		})
	}
	return corechat.ModelFunc(func(ctx context.Context, request *corechat.Request) (response *corechat.Response, err error) {
		ctx, operation := m.start(ctx, request, false)
		observation := responseObservation{operation: operation}
		defer errortelemetry.Finish(&err, func(observedError error) {
			m.finish(ctx, observation, observedError)
		})
		response, err = next.Call(ctx, request)
		if response != nil {
			observation.observeMetadata(response.Metadata)
			if response.Output != nil {
				observation.finishReason = response.Output.FinishReason
			}
		}
		return response, err
	})
}

// Stream is a [corechat.StreamMiddleware]. Observation starts when the caller
// iterates and ends synchronously on completion, provider failure, or early
// stop. Deltas pass through unchanged and content is never buffered. Every
// non-nil delta counts as a chunk, including metadata-only increments, and
// known usage survives an incomplete stream.
func (m Middleware) Stream(next corechat.Streamer) corechat.Streamer {
	if lo.IsNil(next) {
		return nil
	}
	if err := m.validate(); err != nil {
		return corechat.StreamerFunc(func(context.Context, *corechat.Request) iter.Seq2[*corechat.ResponseDelta, error] {
			return func(yield func(*corechat.ResponseDelta, error) bool) {
				yield(nil, err)
			}
		})
	}
	return corechat.StreamerFunc(func(ctx context.Context, request *corechat.Request) iter.Seq2[*corechat.ResponseDelta, error] {
		return func(yield func(*corechat.ResponseDelta, error) bool) {
			spanCtx, operation := m.start(ctx, request, true)
			observation := responseObservation{operation: operation}
			var (
				streamErr error
				stopped   bool
			)
			defer errortelemetry.Finish(&streamErr, func(observedError error) {
				m.finish(spanCtx, observation, observedError)
			})

			sequence := next.Stream(spanCtx, request)
			if sequence == nil {
				streamErr = corechat.ErrNilStream
				yield(nil, streamErr)
				return
			}
			sequence(func(chunk *corechat.ResponseDelta, err error) bool {
				if stopped {
					return false
				}
				if chunk != nil {
					receivedAt := time.Now()
					observation.observeMetadata(chunk.Metadata)
					if chunk.FinishReason != "" {
						observation.finishReason = chunk.FinishReason
					}
					operation.ObserveChunk(spanCtx, m.timing, receivedAt)
				}
				if err != nil {
					streamErr = err
					stopped = true
					yield(chunk, err)
					return false
				}
				stopped = !yield(chunk, nil)
				return !stopped
			})
		}
	})
}

func (m Middleware) validate() error {
	if !m.client.Ready() || lo.IsNil(m.tokens.Inst()) || !m.timing.Ready() {
		return fmt.Errorf("%w: middleware must be constructed with NewMiddleware", ErrInvalidConfig)
	}
	return nil
}

func (m Middleware) start(ctx context.Context, request *corechat.Request, streaming bool) (context.Context, *instrument.Operation) {
	attrs := requestAttributes(request)
	if streaming {
		attrs = append(attrs, semconv.GenAIRequestStream(true))
	}
	model := ""
	if request != nil {
		model = request.Options.Model
	}
	return m.client.StartOperation(ctx, genaiconv.OperationNameChat, model, attrs...)
}

func (m Middleware) finish(ctx context.Context, observation responseObservation, err error) {
	operation := observation.operation
	if observation.finishReason != "" {
		operation.SetAttributes(semconv.GenAIResponseFinishReasons(observation.finishReason.String()))
	}
	if usage := observation.usage; usage != nil {
		operation.SetAttributes(usageAttributes(usage)...)
		operation.RecordTokens(ctx, m.tokens, genaiconv.TokenTypeInput, usage.InputTokens)
		operation.RecordTokens(ctx, m.tokens, genaiconv.TokenTypeOutput, usage.OutputTokens)
	}
	operation.End(ctx, err)
}

func requestAttributes(request *corechat.Request) []attribute.KeyValue {
	if request == nil {
		return nil
	}
	options := request.Options
	var attrs []attribute.KeyValue
	if outputType, ok := outputTypeAttribute(options.OutputFormat); ok {
		attrs = append(attrs, outputType)
	}
	if options.MaxOutputTokens != nil {
		attrs = append(attrs, semconv.GenAIRequestMaxTokensKey.Int64(*options.MaxOutputTokens))
	}
	if options.Temperature != nil {
		attrs = append(attrs, semconv.GenAIRequestTemperature(*options.Temperature))
	}
	if options.TopP != nil {
		attrs = append(attrs, semconv.GenAIRequestTopP(*options.TopP))
	}
	if options.TopK != nil {
		attrs = append(attrs, semconv.GenAIRequestTopKKey.Int64(*options.TopK))
	}
	if options.FrequencyPenalty != nil {
		attrs = append(attrs, semconv.GenAIRequestFrequencyPenalty(*options.FrequencyPenalty))
	}
	if options.PresencePenalty != nil {
		attrs = append(attrs, semconv.GenAIRequestPresencePenalty(*options.PresencePenalty))
	}
	if len(options.Stop) > 0 {
		attrs = append(attrs, semconv.GenAIRequestStopSequences(options.Stop...))
	}
	return attrs
}

func outputTypeAttribute(format *corechat.OutputFormat) (attribute.KeyValue, bool) {
	if format == nil {
		return attribute.KeyValue{}, false
	}
	switch format.Type {
	case corechat.OutputFormatText:
		return semconv.GenAIOutputTypeText, true
	case corechat.OutputFormatJSON, corechat.OutputFormatJSONSchema:
		return semconv.GenAIOutputTypeJSON, true
	default:
		return attribute.KeyValue{}, false
	}
}

// responseObservation owns accounting snapshots across iterator callbacks.
// Provider content and mutable metadata remain owned by the caller.
type responseObservation struct {
	operation    *instrument.Operation
	usage        *corechat.Usage
	finishReason corechat.FinishReason
}

func (r *responseObservation) observeMetadata(metadata *corechat.ResponseMetadata) {
	if metadata == nil {
		return
	}
	if metadata.ID != "" {
		r.operation.SetAttributes(semconv.GenAIResponseID(metadata.ID))
	}
	r.operation.ObserveResponseModel(metadata.Model)
	if metadata.Usage != nil {
		usage := metadata.Usage.Clone()
		r.usage = &usage
	}
}

func errorTypeAttribute(err error) attribute.KeyValue {
	return errortelemetry.Classify(err,
		errortelemetry.Class{Err: corechat.ErrInvalidRequest, Type: errorTypeInvalidRequest},
		errortelemetry.Class{Err: corechat.ErrInvalidResponse, Type: errorTypeInvalidResponse},
		errortelemetry.Class{Err: corechat.ErrInvalidMessage, Type: errorTypeInvalidMessage},
		errortelemetry.Class{Err: corechat.ErrInvalidPart, Type: errorTypeInvalidPart},
		errortelemetry.Class{Err: corechat.ErrInvalidToolCall, Type: errorTypeInvalidToolCall},
		errortelemetry.Class{Err: corechat.ErrInvalidToolResult, Type: errorTypeInvalidToolResult},
		errortelemetry.Class{Err: toolcontract.ErrInvalidDefinition, Type: errorTypeInvalidToolDefinition},
		errortelemetry.Class{Err: corechat.ErrInvalidOutputFormat, Type: errorTypeInvalidOutputFormat},
		errortelemetry.Class{Err: corechat.ErrInvalidOptions, Type: errorTypeInvalidOptions},
		errortelemetry.Class{Err: corechat.ErrInvalidUsage, Type: errorTypeInvalidUsage},
		errortelemetry.Class{Err: corechat.ErrNilStream, Type: errorTypeNilStream},
	)
}

// Only the final known snapshot belongs on the span; optional breakdowns from
// an earlier snapshot must not survive a replacement that omits them.
func usageAttributes(usage *corechat.Usage) []attribute.KeyValue {
	attributes := []attribute.KeyValue{
		semconv.GenAIUsageInputTokensKey.Int64(usage.InputTokens),
		semconv.GenAIUsageOutputTokensKey.Int64(usage.OutputTokens),
	}
	if usage.CacheReadInputTokens != nil {
		attributes = append(attributes, semconv.GenAIUsageCacheReadInputTokensKey.Int64(*usage.CacheReadInputTokens))
	}
	if usage.CacheWriteInputTokens != nil {
		attributes = append(attributes, cacheWriteInputTokensKey.Int64(*usage.CacheWriteInputTokens))
	}
	if usage.ReasoningTokens != nil {
		attributes = append(attributes, semconv.GenAIUsageReasoningOutputTokensKey.Int64(*usage.ReasoningTokens))
	}
	return attributes
}
