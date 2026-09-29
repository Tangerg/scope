// Package chat instruments Core chat capabilities with OpenTelemetry.
package chat

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"strings"
	"time"

	apiotel "go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/metric"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
	"go.opentelemetry.io/otel/semconv/v1.41.0/genaiconv"
	"go.opentelemetry.io/otel/trace"

	"github.com/samber/lo"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/otel/internal/errortelemetry"
	"github.com/Tangerg/scope/otel/internal/genaimetric"
)

const (
	instrumentationName            = "github.com/Tangerg/scope/otel/chat"
	chatOperationName              = "chat"
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
	errorTypeNilStream             = "otel.chat.nil_stream"
	cacheWriteInputTokensKey       = attribute.Key("gen_ai.usage.cache_write.input_tokens")
)

var (
	ErrInvalidConfig = errors.New("otel/chat: invalid config")
	ErrNilStream     = errors.New("otel/chat: nil stream sequence")
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
	logger        log.Logger
	provider      string
	tracer        trace.Tracer
	duration      genaiconv.ClientOperationDuration
	tokens        genaiconv.ClientTokenUsage
	firstChunk    genaiconv.ClientOperationTimeToFirstChunk
	chunkInterval genaiconv.ClientOperationTimePerOutputChunk
}

func NewMiddleware(config MiddlewareConfig) (Middleware, error) {
	if err := config.Validate(); err != nil {
		return Middleware{}, err
	}
	provider := strings.ToLower(strings.TrimSpace(config.Provider))

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
	durationBuckets := genaimetric.DurationBuckets()
	duration, err := genaiconv.NewClientOperationDuration(meter, durationBuckets)
	if err != nil {
		return Middleware{}, fmt.Errorf("%w: create duration histogram: %w", ErrInvalidConfig, err)
	}
	tokens, err := genaiconv.NewClientTokenUsage(meter, genaimetric.TokenBuckets())
	if err != nil {
		return Middleware{}, fmt.Errorf("%w: create token histogram: %w", ErrInvalidConfig, err)
	}
	firstChunk, err := genaiconv.NewClientOperationTimeToFirstChunk(meter, durationBuckets)
	if err != nil {
		return Middleware{}, fmt.Errorf("%w: create first-chunk histogram: %w", ErrInvalidConfig, err)
	}
	chunkInterval, err := genaiconv.NewClientOperationTimePerOutputChunk(meter, durationBuckets)
	if err != nil {
		return Middleware{}, fmt.Errorf("%w: create chunk-interval histogram: %w", ErrInvalidConfig, err)
	}

	return Middleware{
		logger:   loggerProvider.Logger(instrumentationName),
		provider: provider, tracer: tracerProvider.Tracer(instrumentationName),
		duration: duration, tokens: tokens, firstChunk: firstChunk, chunkInterval: chunkInterval,
	}, nil
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
		started := time.Now()
		ctx, span := m.start(ctx, request, started, false)
		var observation responseObservation
		defer errortelemetry.Finish(&err, func(observedError error) {
			m.finish(ctx, span, request, observation, observedError, started)
		})
		response, err = next.Call(ctx, request)
		if response != nil {
			observation.observeMetadata(span, response.Metadata)
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
			started := time.Now()
			spanCtx, span := m.start(ctx, request, started, true)
			var (
				observation   responseObservation
				previousChunk time.Time
				streamErr     error
				stopped       bool
			)
			defer errortelemetry.Finish(&streamErr, func(observedError error) {
				m.finish(spanCtx, span, request, observation, observedError, started)
			})

			sequence := next.Stream(spanCtx, request)
			if sequence == nil {
				streamErr = ErrNilStream
				yield(nil, streamErr)
				return
			}
			sequence(func(chunk *corechat.ResponseDelta, err error) bool {
				if stopped {
					return false
				}
				if chunk != nil {
					receivedAt := time.Now()
					observation.observeMetadata(span, chunk.Metadata)
					if chunk.FinishReason != "" {
						observation.finishReason = chunk.FinishReason
					}
					attributes := observation.metricAttributes(request)
					if previousChunk.IsZero() {
						elapsed := receivedAt.Sub(started).Seconds()
						span.SetAttributes(semconv.GenAIResponseTimeToFirstChunk(elapsed))
						m.firstChunk.Record(spanCtx, elapsed,
							genaiconv.OperationNameChat, genaiconv.ProviderNameAttr(m.provider), attributes...,
						)
					} else {
						m.chunkInterval.Record(spanCtx, receivedAt.Sub(previousChunk).Seconds(),
							genaiconv.OperationNameChat, genaiconv.ProviderNameAttr(m.provider), attributes...,
						)
					}
					previousChunk = receivedAt
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
	if lo.IsNil(m.logger) || lo.IsNil(m.tracer) || lo.IsNil(m.duration.Inst()) || lo.IsNil(m.tokens.Inst()) ||
		lo.IsNil(m.firstChunk.Inst()) || lo.IsNil(m.chunkInterval.Inst()) {
		return fmt.Errorf("%w: middleware must be constructed with NewMiddleware", ErrInvalidConfig)
	}
	return nil
}

func (m Middleware) start(
	ctx context.Context,
	request *corechat.Request,
	started time.Time,
	streaming bool,
) (context.Context, trace.Span) {
	model := requestModel(request)
	name := chatOperationName
	if model != "" {
		name = chatOperationName + " " + model
	}
	attrs := requestAttributes(request)
	if streaming {
		attrs = append(attrs, semconv.GenAIRequestStream(true))
	}
	attrs = append(attrs,
		semconv.GenAIOperationNameChat,
		semconv.GenAIProviderNameKey.String(m.provider),
	)
	return m.tracer.Start(ctx, name,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithTimestamp(started),
		trace.WithAttributes(attrs...),
	)
}

func (m Middleware) finish(
	ctx context.Context,
	span trace.Span,
	request *corechat.Request,
	observation responseObservation,
	err error,
	started time.Time,
) {
	finished := time.Now()
	defer span.End(trace.WithTimestamp(finished))
	observation.recordUsage(span)
	if observation.finishReason != "" {
		span.SetAttributes(semconv.GenAIResponseFinishReasons(observation.finishReason.String()))
	}
	if err != nil {
		errorType := errorTypeAttribute(err)
		errortelemetry.Record(span, errorType, trace.WithTimestamp(finished))
		errortelemetry.EmitGenAIException(ctx, m.logger, errorType, finished)
	}
	m.recordMetrics(ctx, request, observation, finished.Sub(started), err)
}

func (m Middleware) recordMetrics(
	ctx context.Context,
	request *corechat.Request,
	observation responseObservation,
	elapsed time.Duration,
	err error,
) {
	attrs := observation.metricAttributes(request)
	durationAttrs := attrs
	if err != nil {
		durationAttrs = append(durationAttrs, errorTypeAttribute(err))
	}
	m.duration.Record(ctx, elapsed.Seconds(),
		genaiconv.OperationNameChat,
		genaiconv.ProviderNameAttr(m.provider),
		durationAttrs...,
	)
	if observation.usage == nil {
		return
	}
	m.tokens.Record(ctx, observation.usage.InputTokens,
		genaiconv.OperationNameChat,
		genaiconv.ProviderNameAttr(m.provider),
		genaiconv.TokenTypeInput,
		attrs...,
	)
	m.tokens.Record(ctx, observation.usage.OutputTokens,
		genaiconv.OperationNameChat,
		genaiconv.ProviderNameAttr(m.provider),
		genaiconv.TokenTypeOutput,
		attrs...,
	)
}

func requestAttributes(request *corechat.Request) []attribute.KeyValue {
	if request == nil {
		return nil
	}
	options := request.Options
	var attrs []attribute.KeyValue
	if options.Model != "" {
		attrs = append(attrs, semconv.GenAIRequestModel(options.Model))
	}
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
	model        string
	usage        *corechat.Usage
	finishReason corechat.FinishReason
}

func (r *responseObservation) observeMetadata(span trace.Span, metadata *corechat.ResponseMetadata) {
	if metadata == nil {
		return
	}
	var attributes []attribute.KeyValue
	if metadata.ID != "" {
		attributes = append(attributes, semconv.GenAIResponseID(metadata.ID))
	}
	if metadata.Model != "" {
		r.model = metadata.Model
		attributes = append(attributes, semconv.GenAIResponseModel(metadata.Model))
	}
	if metadata.Usage != nil {
		usage := *metadata.Usage
		if usage.ReasoningTokens != nil {
			usage.ReasoningTokens = new(*usage.ReasoningTokens)
		}
		if usage.CacheReadInputTokens != nil {
			usage.CacheReadInputTokens = new(*usage.CacheReadInputTokens)
		}
		if usage.CacheWriteInputTokens != nil {
			usage.CacheWriteInputTokens = new(*usage.CacheWriteInputTokens)
		}
		r.usage = &usage
	}
	span.SetAttributes(attributes...)
}

func (r responseObservation) metricAttributes(request *corechat.Request) []attribute.KeyValue {
	var attributes []attribute.KeyValue
	if model := requestModel(request); model != "" {
		attributes = append(attributes, semconv.GenAIRequestModel(model))
	}
	if r.model != "" {
		attributes = append(attributes, semconv.GenAIResponseModel(r.model))
	}
	return attributes
}

func requestModel(request *corechat.Request) string {
	if request == nil {
		return ""
	}
	return request.Options.Model
}

func errorTypeAttribute(err error) attribute.KeyValue {
	return errortelemetry.Classify(err,
		errortelemetry.Class{Err: corechat.ErrInvalidRequest, Type: errorTypeInvalidRequest},
		errortelemetry.Class{Err: corechat.ErrInvalidResponse, Type: errorTypeInvalidResponse},
		errortelemetry.Class{Err: corechat.ErrInvalidMessage, Type: errorTypeInvalidMessage},
		errortelemetry.Class{Err: corechat.ErrInvalidPart, Type: errorTypeInvalidPart},
		errortelemetry.Class{Err: corechat.ErrInvalidToolCall, Type: errorTypeInvalidToolCall},
		errortelemetry.Class{Err: corechat.ErrInvalidToolResult, Type: errorTypeInvalidToolResult},
		errortelemetry.Class{Err: corechat.ErrInvalidToolDefinition, Type: errorTypeInvalidToolDefinition},
		errortelemetry.Class{Err: corechat.ErrInvalidOutputFormat, Type: errorTypeInvalidOutputFormat},
		errortelemetry.Class{Err: corechat.ErrInvalidOptions, Type: errorTypeInvalidOptions},
		errortelemetry.Class{Err: corechat.ErrInvalidUsage, Type: errorTypeInvalidUsage},
		errortelemetry.Class{Err: ErrNilStream, Type: errorTypeNilStream},
	)
}

// Only the final known snapshot belongs on the span; optional breakdowns from
// an earlier snapshot must not survive a replacement that omits them.
func (r responseObservation) recordUsage(span trace.Span) {
	if r.usage == nil {
		return
	}
	usage := r.usage
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
	span.SetAttributes(attributes...)
}
