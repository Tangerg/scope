// Package image instruments Core image-generation calls with OpenTelemetry.
package image

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

	coreimage "github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/otel/errortelemetry"
	"github.com/Tangerg/scope/otel/internal/genaimetric"
	"github.com/Tangerg/scope/otel/internal/instrument"
)

const (
	instrumentationName = "github.com/Tangerg/scope/otel/image"
	errorInvalidRequest = "image.invalid_request"
	errorInvalidOutput  = "image.invalid_response"
)

var (
	ErrInvalidConfig = errors.New("otel/image: invalid config")
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
	tracerProvider := instrument.TracerProvider(config.TracerProvider)
	loggerProvider := instrument.LoggerProvider(config.LoggerProvider)
	meterProvider := instrument.MeterProvider(config.MeterProvider)
	duration, err := genaiconv.NewClientOperationDuration(meterProvider.Meter(instrumentationName), genaimetric.DurationBuckets())
	if err != nil {
		return Middleware{}, fmt.Errorf("%w: create duration histogram: %w", ErrInvalidConfig, err)
	}
	return Middleware{
		logger:   loggerProvider.Logger(instrumentationName),
		provider: instrument.Dimension(config.Provider),
		tracer:   tracerProvider.Tracer(instrumentationName),
		duration: duration,
	}, nil
}

// Call instruments next without changing its response or error.
func (m Middleware) Call(next coreimage.Model) coreimage.Model {
	if lo.IsNil(next) {
		return nil
	}
	if lo.IsNil(m.logger) || lo.IsNil(m.tracer) || lo.IsNil(m.duration.Inst()) {
		err := fmt.Errorf("%w: middleware must be constructed with NewMiddleware", ErrInvalidConfig)
		return coreimage.ModelFunc(func(context.Context, *coreimage.Request) (*coreimage.Response, error) { return nil, err })
	}
	return coreimage.ModelFunc(func(ctx context.Context, request *coreimage.Request) (response *coreimage.Response, err error) {
		spanCtx, span, startedAt := instrument.StartClientSpan(ctx, m.tracer, m.spanName(request), m.requestAttributes(request)...)
		defer errortelemetry.Finish(&err, func(observedError error) {
			finishedAt := time.Now()
			defer span.End(trace.WithTimestamp(finishedAt))
			metricAttributes := m.metricAttributes(request)
			if observedError != nil {
				errorType := errorTypeAttribute(observedError)
				errortelemetry.Record(span, errorType, trace.WithTimestamp(finishedAt))
				errortelemetry.EmitGenAIException(spanCtx, m.logger, errorType, finishedAt)
				metricAttributes = append(metricAttributes, errorType)
			}
			m.duration.Record(spanCtx, finishedAt.Sub(startedAt).Seconds(),
				genaiconv.OperationNameGenerateContent,
				genaiconv.ProviderNameAttr(m.provider),
				metricAttributes...,
			)
		})
		return next.Call(spanCtx, request)
	})
}

func (m Middleware) spanName(request *coreimage.Request) string {
	if request == nil || request.Options.Model == "" {
		return string(genaiconv.OperationNameGenerateContent)
	}
	return string(genaiconv.OperationNameGenerateContent) + " " + request.Options.Model
}

func (m Middleware) requestAttributes(request *coreimage.Request) []attribute.KeyValue {
	attributes := []attribute.KeyValue{
		semconv.GenAIOperationNameKey.String(string(genaiconv.OperationNameGenerateContent)),
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
	return errortelemetry.Classify(err,
		errortelemetry.Class{Err: coreimage.ErrInvalidRequest, Type: errorInvalidRequest},
		errortelemetry.Class{Err: coreimage.ErrInvalidOptions, Type: errorInvalidRequest},
		errortelemetry.Class{Err: coreimage.ErrInvalidResponse, Type: errorInvalidOutput},
	)
}
