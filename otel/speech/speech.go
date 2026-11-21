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
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/metric"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
	"go.opentelemetry.io/otel/semconv/v1.41.0/genaiconv"
	"go.opentelemetry.io/otel/trace"

	corespeech "github.com/Tangerg/scope/core/speech"
	"github.com/Tangerg/scope/otel/errortelemetry"
	"github.com/Tangerg/scope/otel/internal/instrument"
)

const (
	instrumentationName                                  = "github.com/Tangerg/scope/otel/speech"
	operationName            genaiconv.OperationNameAttr = "synthesize_speech"
	errorTypeInvalidRequest                              = "speech.invalid_request"
	errorTypeInvalidOptions                              = "speech.invalid_options"
	errorTypeInvalidResponse                             = "speech.invalid_response"
	errorTypeInvalidStreamer                             = "otel.speech.invalid_streamer"
)

var (
	ErrInvalidConfig   = errors.New("otel/speech: invalid config")
	ErrInvalidStreamer = errors.New("otel/speech: invalid streamer")
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

// Middleware observes synthesis metadata without recording input text, voice
// names, or generated audio.
type Middleware struct {
	client instrument.Client
	timing instrument.ChunkTiming
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
	timing, err := instrument.NewChunkTiming(client.Meter)
	if err != nil {
		return Middleware{}, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}
	return Middleware{client: client, timing: timing}, nil
}

// Call instruments next without changing its response or error.
func (m Middleware) Call(next corespeech.Model) corespeech.Model {
	if lo.IsNil(next) {
		return nil
	}
	if err := m.validate(); err != nil {
		return corespeech.ModelFunc(func(context.Context, *corespeech.Request) (*corespeech.Response, error) { return nil, err })
	}
	return corespeech.ModelFunc(func(ctx context.Context, request *corespeech.Request) (response *corespeech.Response, err error) {
		ctx, operation := m.start(ctx, request, false)
		defer errortelemetry.Finish(&err, func(observedError error) { operation.End(ctx, observedError) })
		response, err = next.Call(ctx, request)
		observeResponseModel(operation, response)
		return response, err
	})
}

// Stream decorates one streaming speech capability. Provider work remains
// lazy and stopping iteration closes the observation synchronously.
func (m Middleware) Stream(next corespeech.Streamer) corespeech.Streamer {
	if lo.IsNil(next) {
		return nil
	}
	if err := m.validate(); err != nil {
		return corespeech.StreamerFunc(func(context.Context, *corespeech.Request) iter.Seq2[*corespeech.Response, error] {
			return func(yield func(*corespeech.Response, error) bool) { yield(nil, err) }
		})
	}
	return corespeech.StreamerFunc(func(ctx context.Context, request *corespeech.Request) iter.Seq2[*corespeech.Response, error] {
		return func(yield func(*corespeech.Response, error) bool) {
			spanCtx, operation := m.start(ctx, request, true)
			var streamErr error
			defer errortelemetry.Finish(&streamErr, func(observedError error) { operation.End(spanCtx, observedError) })
			sequence := next.Stream(spanCtx, request)
			if sequence == nil {
				streamErr = fmt.Errorf("%w: nil stream sequence", ErrInvalidStreamer)
				yield(nil, streamErr)
				return
			}
			for response, err := range sequence {
				if response != nil {
					receivedAt := time.Now()
					observeResponseModel(operation, response)
					operation.ObserveChunk(spanCtx, m.timing, receivedAt)
				}
				streamErr = err
				keepGoing := yield(response, err)
				if err != nil || !keepGoing {
					return
				}
			}
		}
	})
}

func (m Middleware) validate() error {
	if !m.client.Ready() || !m.timing.Ready() {
		return fmt.Errorf("%w: middleware must be constructed with NewMiddleware", ErrInvalidConfig)
	}
	return nil
}

func (m Middleware) start(ctx context.Context, request *corespeech.Request, streaming bool) (context.Context, *instrument.Operation) {
	model := ""
	if request != nil {
		model = request.Options.Model
	}
	return m.client.StartOperation(ctx, operationName, model, semconv.GenAIOutputTypeSpeech, semconv.GenAIRequestStreamKey.Bool(streaming))
}

func observeResponseModel(operation *instrument.Operation, response *corespeech.Response) {
	if response != nil && response.Metadata != nil {
		operation.ObserveResponseModel(response.Metadata.Model)
	}
}

func errorTypeAttribute(err error) attribute.KeyValue {
	return errortelemetry.Classify(err,
		errortelemetry.Class{Err: corespeech.ErrInvalidRequest, Type: errorTypeInvalidRequest},
		errortelemetry.Class{Err: corespeech.ErrInvalidOptions, Type: errorTypeInvalidOptions},
		errortelemetry.Class{Err: corespeech.ErrInvalidResponse, Type: errorTypeInvalidResponse},
		errortelemetry.Class{Err: ErrInvalidStreamer, Type: errorTypeInvalidStreamer},
	)
}
