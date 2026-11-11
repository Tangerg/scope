// Package transcription instruments Core transcription calls with OpenTelemetry.
package transcription

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

	coretranscription "github.com/Tangerg/scope/core/transcription"
	"github.com/Tangerg/scope/otel/errortelemetry"
	"github.com/Tangerg/scope/otel/internal/instrument"
)

const (
	instrumentationName = "github.com/Tangerg/scope/otel/transcription"
	operationName       = "transcribe"
	errorInvalidRequest = "transcription.invalid_request"
	errorInvalidOutput  = "transcription.invalid_response"
)

var (
	ErrInvalidConfig = errors.New("otel/transcription: invalid config")
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

// Middleware observes transcription calls without recording audio, transcript
// text, or language hints.
type Middleware struct {
	client instrument.Client
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
	return Middleware{client: client}, nil
}

// Call instruments next without changing its response or error.
func (m Middleware) Call(next coretranscription.Model) coretranscription.Model {
	if lo.IsNil(next) {
		return nil
	}
	if !m.client.Ready() {
		err := fmt.Errorf("%w: middleware must be constructed with NewMiddleware", ErrInvalidConfig)
		return coretranscription.ModelFunc(func(context.Context, *coretranscription.Request) (*coretranscription.Response, error) {
			return nil, err
		})
	}
	return coretranscription.ModelFunc(func(ctx context.Context, request *coretranscription.Request) (response *coretranscription.Response, err error) {
		spanCtx, span, startedAt := instrument.StartClientSpan(ctx, m.client.Tracer, m.spanName(request), m.requestAttributes(request)...)
		defer errortelemetry.Finish(&err, func(observedError error) {
			m.finish(spanCtx, span, request, response, observedError, startedAt, time.Now())
		})
		response, err = next.Call(spanCtx, request)
		return response, err
	})
}

func (m Middleware) spanName(request *coretranscription.Request) string {
	if request == nil || request.Options.Model == "" {
		return operationName
	}
	return operationName + " " + request.Options.Model
}

func (m Middleware) requestAttributes(request *coretranscription.Request) []attribute.KeyValue {
	attributes := []attribute.KeyValue{
		semconv.GenAIOperationNameKey.String(operationName),
		semconv.GenAIProviderNameKey.String(m.client.Provider),
		semconv.GenAIOutputTypeText,
	}
	if request != nil && request.Options.Model != "" {
		attributes = append(attributes, semconv.GenAIRequestModel(request.Options.Model))
	}
	return attributes
}

func (m Middleware) finish(
	ctx context.Context,
	span trace.Span,
	request *coretranscription.Request,
	response *coretranscription.Response,
	err error,
	startedAt, finishedAt time.Time,
) {
	defer span.End(trace.WithTimestamp(finishedAt))
	attributes := m.metricAttributes(request, response)
	if response != nil && response.Metadata != nil && response.Metadata.Model != "" {
		span.SetAttributes(semconv.GenAIResponseModel(response.Metadata.Model))
	}
	if err != nil {
		errorType := errorTypeAttribute(err)
		errortelemetry.Record(span, errorType, trace.WithTimestamp(finishedAt))
		errortelemetry.EmitGenAIException(ctx, m.client.Logger, errorType, finishedAt)
		attributes = append(attributes, errorType)
	}
	m.client.Duration.Record(ctx, finishedAt.Sub(startedAt).Seconds(),
		genaiconv.OperationNameAttr(operationName), genaiconv.ProviderNameAttr(m.client.Provider), attributes...,
	)
}

func (m Middleware) metricAttributes(
	request *coretranscription.Request,
	response *coretranscription.Response,
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
		errortelemetry.Class{Err: coretranscription.ErrInvalidRequest, Type: errorInvalidRequest},
		errortelemetry.Class{Err: coretranscription.ErrInvalidOptions, Type: errorInvalidRequest},
		errortelemetry.Class{Err: coretranscription.ErrInvalidResponse, Type: errorInvalidOutput},
	)
}
