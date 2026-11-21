// Package image instruments Core image-generation calls with OpenTelemetry.
package image

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

	coreimage "github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/otel/errortelemetry"
	"github.com/Tangerg/scope/otel/internal/instrument"
)

const (
	instrumentationName      = "github.com/Tangerg/scope/otel/image"
	errorTypeInvalidRequest  = "image.invalid_request"
	errorTypeInvalidOptions  = "image.invalid_options"
	errorTypeInvalidResponse = "image.invalid_response"
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
func (m Middleware) Call(next coreimage.Model) coreimage.Model {
	if lo.IsNil(next) {
		return nil
	}
	if !m.client.Ready() {
		err := fmt.Errorf("%w: middleware must be constructed with NewMiddleware", ErrInvalidConfig)
		return coreimage.ModelFunc(func(context.Context, *coreimage.Request) (*coreimage.Response, error) { return nil, err })
	}
	return coreimage.ModelFunc(func(ctx context.Context, request *coreimage.Request) (response *coreimage.Response, err error) {
		spanCtx, operation := m.client.StartOperation(ctx, genaiconv.OperationNameGenerateContent, requestModel(request), semconv.GenAIOutputTypeImage)
		defer errortelemetry.Finish(&err, func(observedError error) { operation.End(spanCtx, observedError) })
		return next.Call(spanCtx, request)
	})
}

func requestModel(request *coreimage.Request) string {
	if request == nil {
		return ""
	}
	return request.Options.Model
}

func errorTypeAttribute(err error) attribute.KeyValue {
	return errortelemetry.Classify(err,
		errortelemetry.Class{Err: coreimage.ErrInvalidRequest, Type: errorTypeInvalidRequest},
		errortelemetry.Class{Err: coreimage.ErrInvalidOptions, Type: errorTypeInvalidOptions},
		errortelemetry.Class{Err: coreimage.ErrInvalidResponse, Type: errorTypeInvalidResponse},
	)
}
