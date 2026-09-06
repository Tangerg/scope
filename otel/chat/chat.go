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
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
	"go.opentelemetry.io/otel/semconv/v1.41.0/genaiconv"
	"go.opentelemetry.io/otel/trace"

	"github.com/samber/lo"

	corechat "github.com/Tangerg/scope/core/chat"
)

const (
	instrumentationName            = "github.com/Tangerg/scope/otel/chat"
	chatOperationName              = "chat"
	incompleteFinishReason         = "error"
	errorTypeContextCanceled       = "context.canceled"
	errorTypeDeadlineExceeded      = "context.deadline_exceeded"
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

// MiddlewareConfig identifies the remote GenAI provider and optionally supplies
// providers scoped to this middleware. Provider is normalized to lowercase so
// span and metric dimensions remain stable. The global OpenTelemetry providers
// are used when TracerProvider or MeterProvider is nil.
type MiddlewareConfig struct {
	Provider       string
	TracerProvider trace.TracerProvider
	MeterProvider  metric.MeterProvider
}

func (m MiddlewareConfig) Validate() error {
	if strings.TrimSpace(m.Provider) == "" {
		return fmt.Errorf("%w: provider is required", ErrInvalidConfig)
	}
	return nil
}

// Middleware adds GenAI spans and metrics to synchronous and streaming
// chat capabilities. It is immutable after construction and safe for
// concurrent use.
type Middleware struct {
	provider      string
	tracer        trace.Tracer
	duration      genaiconv.ClientOperationDuration
	tokens        genaiconv.ClientTokenUsage
	firstChunk    genaiconv.ClientOperationTimeToFirstChunk
	chunkInterval genaiconv.ClientOperationTimePerOutputChunk
}

// NewMiddleware fixes instrument identity and provider binding once so every
// model request contributes to the same telemetry series.
func NewMiddleware(config MiddlewareConfig) (Middleware, error) {
	if err := config.Validate(); err != nil {
		return Middleware{}, err
	}
	provider := strings.ToLower(strings.TrimSpace(config.Provider))

	tracerProvider := config.TracerProvider
	if lo.IsNil(tracerProvider) {
		tracerProvider = apiotel.GetTracerProvider()
	}
	meterProvider := config.MeterProvider
	if lo.IsNil(meterProvider) {
		meterProvider = apiotel.GetMeterProvider()
	}

	meter := meterProvider.Meter(instrumentationName)
	durationBuckets := metric.WithExplicitBucketBoundaries(
		0.01, 0.02, 0.04, 0.08, 0.16, 0.32, 0.64, 1.28, 2.56, 5.12, 10.24, 20.48, 40.96, 81.92,
	)
	duration, err := genaiconv.NewClientOperationDuration(meter, durationBuckets)
	if err != nil {
		return Middleware{}, fmt.Errorf("%w: create duration histogram: %w", ErrInvalidConfig, err)
	}
	tokens, err := genaiconv.NewClientTokenUsage(meter, metric.WithExplicitBucketBoundaries(
		1, 4, 16, 64, 256, 1024, 4096, 16384, 65536, 262144, 1048576, 4194304, 16777216, 67108864,
	))
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
	return corechat.ModelFunc(func(ctx context.Context, request *corechat.Request) (*corechat.Response, error) {
		started := time.Now()
		ctx, span := m.start(ctx, request, started, false)
		response, err := next.Call(ctx, request)
		var observation responseObservation
		if response != nil {
			observation.observeMetadata(span, response.Metadata)
			if response.Output != nil {
				observation.finishReason = response.Output.FinishReason
			}
		}
		m.finish(ctx, span, request, observation, err, started)
		return response, err
	})
}

// Stream is a [corechat.StreamMiddleware]. Instrumentation starts lazily when the
// caller iterates and ends synchronously on completion, provider failure, or
// early consumer stop. Deltas are forwarded unchanged. Only identity, cumulative
// usage, finish reason, and arrival times are observed; content is never buffered
// or assembled into a second response. Each non-nil delta is a received chunk,
// including metadata-only increments. Known usage survives an incomplete stream.
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
			defer func() {
				m.finish(spanCtx, span, request, observation, streamErr, started)
			}()

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
	if lo.IsNil(m.tracer) || lo.IsNil(m.duration.Inst()) || lo.IsNil(m.tokens.Inst()) ||
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
	finishReason := observation.finishReason.String()
	if finishReason == "" {
		finishReason = incompleteFinishReason
	}
	span.SetAttributes(semconv.GenAIResponseFinishReasons(finishReason))
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		span.SetAttributes(errorTypeAttribute(err))
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
	if observation.inputTokens > 0 {
		m.tokens.Record(ctx, observation.inputTokens,
			genaiconv.OperationNameChat,
			genaiconv.ProviderNameAttr(m.provider),
			genaiconv.TokenTypeInput,
			attrs...,
		)
	}
	if observation.outputTokens > 0 {
		m.tokens.Record(ctx, observation.outputTokens,
			genaiconv.OperationNameChat,
			genaiconv.ProviderNameAttr(m.provider),
			genaiconv.TokenTypeOutput,
			attrs...,
		)
	}
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
	if options.OutputFormat != nil {
		switch options.OutputFormat.Type {
		case corechat.OutputFormatText:
			attrs = append(attrs, semconv.GenAIOutputTypeText)
		case corechat.OutputFormatJSON, corechat.OutputFormatJSONSchema:
			attrs = append(attrs, semconv.GenAIOutputTypeJSON)
		}
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

// responseObservation retains only scalar facts across iterator callbacks.
// Provider content and mutable metadata remain owned by the caller.
type responseObservation struct {
	model        string
	inputTokens  int64
	outputTokens int64
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
	usage := metadata.Usage
	if usage.InputTokens > 0 || usage.OutputTokens > 0 || usage.ReasoningTokens != nil ||
		usage.CacheReadInputTokens != nil || usage.CacheWriteInputTokens != nil {
		r.inputTokens, r.outputTokens = usage.InputTokens, usage.OutputTokens
	}
	if usage.InputTokens > 0 {
		attributes = append(attributes, semconv.GenAIUsageInputTokensKey.Int64(usage.InputTokens))
	}
	if usage.OutputTokens > 0 {
		attributes = append(attributes, semconv.GenAIUsageOutputTokensKey.Int64(usage.OutputTokens))
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
	switch {
	case errors.Is(err, context.Canceled):
		return semconv.ErrorTypeKey.String(errorTypeContextCanceled)
	case errors.Is(err, context.DeadlineExceeded):
		return semconv.ErrorTypeKey.String(errorTypeDeadlineExceeded)
	case errors.Is(err, corechat.ErrInvalidRequest):
		return semconv.ErrorTypeKey.String(errorTypeInvalidRequest)
	case errors.Is(err, corechat.ErrInvalidResponse):
		return semconv.ErrorTypeKey.String(errorTypeInvalidResponse)
	case errors.Is(err, corechat.ErrInvalidMessage):
		return semconv.ErrorTypeKey.String(errorTypeInvalidMessage)
	case errors.Is(err, corechat.ErrInvalidPart):
		return semconv.ErrorTypeKey.String(errorTypeInvalidPart)
	case errors.Is(err, corechat.ErrInvalidToolCall):
		return semconv.ErrorTypeKey.String(errorTypeInvalidToolCall)
	case errors.Is(err, corechat.ErrInvalidToolResult):
		return semconv.ErrorTypeKey.String(errorTypeInvalidToolResult)
	case errors.Is(err, corechat.ErrInvalidToolDefinition):
		return semconv.ErrorTypeKey.String(errorTypeInvalidToolDefinition)
	case errors.Is(err, corechat.ErrInvalidOutputFormat):
		return semconv.ErrorTypeKey.String(errorTypeInvalidOutputFormat)
	case errors.Is(err, corechat.ErrInvalidOptions):
		return semconv.ErrorTypeKey.String(errorTypeInvalidOptions)
	case errors.Is(err, corechat.ErrInvalidUsage):
		return semconv.ErrorTypeKey.String(errorTypeInvalidUsage)
	case errors.Is(err, ErrNilStream):
		return semconv.ErrorTypeKey.String(errorTypeNilStream)
	default:
		return semconv.ErrorType(err)
	}
}
