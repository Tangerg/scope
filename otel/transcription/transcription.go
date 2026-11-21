// Package transcription instruments Core transcription calls with OpenTelemetry.
package transcription

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

	coretranscription "github.com/Tangerg/scope/core/transcription"
	"github.com/Tangerg/scope/otel/errortelemetry"
	"github.com/Tangerg/scope/otel/internal/instrument"
)

const (
	instrumentationName                                  = "github.com/Tangerg/scope/otel/transcription"
	operationName            genaiconv.OperationNameAttr = "transcribe"
	errorTypeInvalidRequest                              = "transcription.invalid_request"
	errorTypeInvalidOptions                              = "transcription.invalid_options"
	errorTypeInvalidResponse                             = "transcription.invalid_response"
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
		ClassifyError: errorTypeAttribute,
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
		spanCtx, operation := m.client.StartOperation(ctx, operationName, requestModel(request), semconv.GenAIOutputTypeText)
		defer errortelemetry.Finish(&err, func(observedError error) {
			if response != nil && response.Metadata != nil {
				operation.ObserveResponseModel(response.Metadata.Model)
			}
			operation.End(spanCtx, observedError)
		})
		response, err = next.Call(spanCtx, request)
		return response, err
	})
}

func requestModel(request *coretranscription.Request) string {
	if request == nil {
		return ""
	}
	return request.Options.Model
}

func errorTypeAttribute(err error) attribute.KeyValue {
	return errortelemetry.Classify(err,
		errortelemetry.Class{Err: coretranscription.ErrInvalidRequest, Type: errorTypeInvalidRequest},
		errortelemetry.Class{Err: coretranscription.ErrInvalidOptions, Type: errorTypeInvalidOptions},
		errortelemetry.Class{Err: coretranscription.ErrInvalidResponse, Type: errorTypeInvalidResponse},
	)
}
