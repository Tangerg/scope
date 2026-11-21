// Package moderation instruments Core moderation calls with OpenTelemetry.
package moderation

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/samber/lo"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/semconv/v1.41.0/genaiconv"
	"go.opentelemetry.io/otel/trace"

	coremoderation "github.com/Tangerg/scope/core/moderation"
	"github.com/Tangerg/scope/otel/errortelemetry"
	"github.com/Tangerg/scope/otel/internal/instrument"
)

const (
	instrumentationName                                  = "github.com/Tangerg/scope/otel/moderation"
	operationName            genaiconv.OperationNameAttr = "moderate"
	errorTypeInvalidRequest                              = "moderation.invalid_request"
	errorTypeInvalidOptions                              = "moderation.invalid_options"
	errorTypeInvalidResponse                             = "moderation.invalid_response"
)

var (
	ErrInvalidConfig = errors.New("otel/moderation: invalid config")
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

// Middleware observes moderation metadata without recording classified text or
// provider category names.
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
func (m Middleware) Call(next coremoderation.Model) coremoderation.Model {
	if lo.IsNil(next) {
		return nil
	}
	if !m.client.Ready() {
		err := fmt.Errorf("%w: middleware must be constructed with NewMiddleware", ErrInvalidConfig)
		return coremoderation.ModelFunc(func(context.Context, *coremoderation.Request) (*coremoderation.Response, error) { return nil, err })
	}
	return coremoderation.ModelFunc(func(ctx context.Context, request *coremoderation.Request) (response *coremoderation.Response, err error) {
		spanCtx, operation := m.client.StartOperation(ctx, operationName, requestModel(request), requestAttributes(request)...)
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

func requestModel(request *coremoderation.Request) string {
	if request == nil {
		return ""
	}
	return request.Options.Model
}

func requestAttributes(request *coremoderation.Request) []attribute.KeyValue {
	if request == nil {
		return nil
	}
	return []attribute.KeyValue{instrument.InputCountKey.Int(len(request.Texts))}
}

func errorTypeAttribute(err error) attribute.KeyValue {
	return errortelemetry.Classify(err,
		errortelemetry.Class{Err: coremoderation.ErrInvalidRequest, Type: errorTypeInvalidRequest},
		errortelemetry.Class{Err: coremoderation.ErrInvalidOptions, Type: errorTypeInvalidOptions},
		errortelemetry.Class{Err: coremoderation.ErrInvalidResponse, Type: errorTypeInvalidResponse},
	)
}
