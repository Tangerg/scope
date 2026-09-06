// Package speech instruments Core speech-synthesis calls with OpenTelemetry.
package speech

import (
	"context"
	"errors"
	"fmt"
	"iter"
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

	corespeech "github.com/Tangerg/scope/core/speech"
	"github.com/Tangerg/scope/otel/internal/errortelemetry"
)

const (
	instrumentationName          = "github.com/Tangerg/scope/otel/speech"
	operationName                = "synthesize_speech"
	operationDurationMetric      = "gen_ai.client.operation.duration"
	operationDurationDescription = "GenAI client operation duration."
	operationDurationUnit        = "s"
	errorCanceled                = "context.canceled"
	errorDeadline                = "context.deadline_exceeded"
	errorInvalidRequest          = "speech.invalid_request"
	errorInvalidOutput           = "speech.invalid_response"
)

var (
	ErrInvalidConfig   = errors.New("otel/speech: invalid config")
	ErrInvalidModel    = errors.New("otel/speech: invalid model")
	ErrInvalidStreamer = errors.New("otel/speech: invalid streamer")
)

// MiddlewareConfig identifies the provider and optional OTel providers used by
// speech instrumentation.
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

// Middleware observes synthesis metadata without recording input text, voice
// names, or generated audio.
type Middleware struct {
	logger        log.Logger
	provider      string
	tracer        trace.Tracer
	duration      metric.Float64Histogram
	firstChunk    genaiconv.ClientOperationTimeToFirstChunk
	chunkInterval genaiconv.ClientOperationTimePerOutputChunk
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
	durationBuckets := metric.WithExplicitBucketBoundaries(
		0.01, 0.02, 0.04, 0.08, 0.16, 0.32, 0.64, 1.28, 2.56, 5.12, 10.24, 20.48, 40.96, 81.92,
	)
	duration, err := meter.Float64Histogram(
		operationDurationMetric,
		metric.WithDescription(operationDurationDescription),
		metric.WithUnit(operationDurationUnit),
		durationBuckets,
	)
	if err != nil {
		return Middleware{}, fmt.Errorf("%w: create duration histogram: %w", ErrInvalidConfig, err)
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
		firstChunk: firstChunk, chunkInterval: chunkInterval,
		logger:   loggerProvider.Logger(instrumentationName),
		provider: strings.ToLower(strings.TrimSpace(config.Provider)),
		tracer:   tracerProvider.Tracer(instrumentationName),
		duration: duration,
	}, nil
}

// Wrap decorates one synchronous speech Model.
func (m Middleware) Wrap(next corespeech.Model) (corespeech.Model, error) {
	if err := m.validate(); err != nil {
		return nil, err
	}
	if lo.IsNil(next) {
		return nil, fmt.Errorf("%w: value must not be nil", ErrInvalidModel)
	}
	return corespeech.ModelFunc(func(ctx context.Context, request *corespeech.Request) (*corespeech.Response, error) {
		ctx, observation := m.start(ctx, request, false)
		response, err := next.Call(ctx, request)
		observation.observeResponse(response)
		observation.finish(err)
		return response, err
	}), nil
}

// WrapStream decorates one streaming speech capability. Provider work remains
// lazy and stopping iteration closes the observation synchronously.
func (m Middleware) WrapStream(next corespeech.Streamer) (corespeech.Streamer, error) {
	if err := m.validate(); err != nil {
		return nil, err
	}
	if lo.IsNil(next) {
		return nil, fmt.Errorf("%w: value must not be nil", ErrInvalidStreamer)
	}
	return corespeech.StreamerFunc(func(ctx context.Context, request *corespeech.Request) iter.Seq2[*corespeech.Response, error] {
		return func(yield func(*corespeech.Response, error) bool) {
			spanCtx, observation := m.start(ctx, request, true)
			var streamErr error
			defer func() { observation.finish(streamErr) }()
			for response, err := range next.Stream(spanCtx, request) {
				observation.observeChunk(response)
				streamErr = err
				keepGoing := yield(response, err)
				if err != nil || !keepGoing {
					return
				}
			}
		}
	}), nil
}

func (m Middleware) validate() error {
	if lo.IsNil(m.logger) || lo.IsNil(m.tracer) || lo.IsNil(m.duration) || lo.IsNil(m.firstChunk.Inst()) || lo.IsNil(m.chunkInterval.Inst()) {
		return fmt.Errorf("%w: middleware must be constructed with NewMiddleware", ErrInvalidConfig)
	}
	return nil
}

func (m Middleware) start(ctx context.Context, request *corespeech.Request, streaming bool) (context.Context, observation) {
	startedAt := time.Now()
	attributes := append(m.requestAttributes(request), semconv.GenAIRequestStreamKey.Bool(streaming))
	spanCtx, span := m.tracer.Start(ctx, m.spanName(request),
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithTimestamp(startedAt),
		trace.WithAttributes(attributes...),
	)
	return spanCtx, observation{
		middleware: m,
		ctx:        spanCtx,
		span:       span,
		startedAt:  startedAt,
		request:    request,
	}
}

func (m Middleware) spanName(request *corespeech.Request) string {
	if request == nil || request.Options.Model == "" {
		return operationName
	}
	return operationName + " " + request.Options.Model
}

func (m Middleware) requestAttributes(request *corespeech.Request) []attribute.KeyValue {
	attributes := []attribute.KeyValue{
		semconv.GenAIOperationNameKey.String(operationName),
		semconv.GenAIProviderNameKey.String(m.provider),
		semconv.GenAIOutputTypeSpeech,
	}
	if request != nil && request.Options.Model != "" {
		attributes = append(attributes, semconv.GenAIRequestModel(request.Options.Model))
	}
	return attributes
}

type observation struct {
	middleware    Middleware
	ctx           context.Context
	span          trace.Span
	startedAt     time.Time
	request       *corespeech.Request
	responseModel string
	lastChunkAt   time.Time
}

func (o *observation) observeResponse(response *corespeech.Response) {
	if response != nil && response.Metadata != nil && response.Metadata.Model != "" {
		o.responseModel = response.Metadata.Model
	}
}

func (o *observation) observeChunk(response *corespeech.Response) {
	if response == nil {
		return
	}
	arrivedAt := time.Now()
	o.observeResponse(response)
	attributes := o.middleware.metricAttributes(o.request, o.responseModel)
	operation := genaiconv.OperationNameAttr(operationName)
	provider := genaiconv.ProviderNameAttr(o.middleware.provider)
	if o.lastChunkAt.IsZero() {
		elapsed := arrivedAt.Sub(o.startedAt).Seconds()
		o.span.SetAttributes(semconv.GenAIResponseTimeToFirstChunk(elapsed))
		o.middleware.firstChunk.Record(o.ctx, elapsed, operation, provider, attributes...)
	} else {
		o.middleware.chunkInterval.Record(o.ctx, arrivedAt.Sub(o.lastChunkAt).Seconds(), operation, provider, attributes...)
	}
	o.lastChunkAt = arrivedAt
}

func (o observation) finish(err error) {
	finishedAt := time.Now()
	attributes := o.middleware.metricAttributes(o.request, o.responseModel)
	if o.responseModel != "" {
		o.span.SetAttributes(semconv.GenAIResponseModel(o.responseModel))
	}
	if err != nil {
		errorType := errorTypeAttribute(err)
		errortelemetry.Record(o.span, errorType, trace.WithTimestamp(finishedAt))
		errortelemetry.EmitGenAIException(o.ctx, o.middleware.logger, errorType, finishedAt)
		attributes = append(attributes, errorType)
	}
	o.span.End(trace.WithTimestamp(finishedAt))
	o.middleware.duration.Record(
		o.ctx,
		finishedAt.Sub(o.startedAt).Seconds(),
		metric.WithAttributes(attributes...),
	)
}

func (m Middleware) metricAttributes(
	request *corespeech.Request,
	responseModel string,
) []attribute.KeyValue {
	attributes := []attribute.KeyValue{
		semconv.GenAIOperationNameKey.String(operationName),
		semconv.GenAIProviderNameKey.String(m.provider),
	}
	if request != nil && request.Options.Model != "" {
		attributes = append(attributes, semconv.GenAIRequestModel(request.Options.Model))
	}
	model := responseModel
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
	case errors.Is(err, corespeech.ErrInvalidRequest), errors.Is(err, corespeech.ErrInvalidOptions):
		return semconv.ErrorTypeKey.String(errorInvalidRequest)
	case errors.Is(err, corespeech.ErrInvalidResponse):
		return semconv.ErrorTypeKey.String(errorInvalidOutput)
	default:
		return semconv.ErrorType(err)
	}
}
