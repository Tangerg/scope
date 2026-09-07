package otel_test

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	corechat "github.com/Tangerg/scope/core/chat"
	otelchat "github.com/Tangerg/scope/otel/chat"

	coreembedding "github.com/Tangerg/scope/core/embedding"
	otelembedding "github.com/Tangerg/scope/otel/embedding"

	coreimage "github.com/Tangerg/scope/core/image"
	otelimage "github.com/Tangerg/scope/otel/image"

	coremoderation "github.com/Tangerg/scope/core/moderation"
	otelmoderation "github.com/Tangerg/scope/otel/moderation"

	corererank "github.com/Tangerg/scope/core/rerank"
	otelrerank "github.com/Tangerg/scope/otel/rerank"

	corespeech "github.com/Tangerg/scope/core/speech"
	otelspeech "github.com/Tangerg/scope/otel/speech"

	coretranscription "github.com/Tangerg/scope/core/transcription"
	oteltranscription "github.com/Tangerg/scope/otel/transcription"
)

func TestModelExceptionsRemainCorrelatedAndContentFree(t *testing.T) {
	calls := []struct {
		name string
		call func(context.Context, trace.TracerProvider, log.LoggerProvider, error) error
	}{
		{name: "chat", call: func(ctx context.Context, tracerProvider trace.TracerProvider, loggerProvider log.LoggerProvider, failure error) error {
			middleware, err := otelchat.NewMiddleware(otelchat.MiddlewareConfig{Provider: "provider", TracerProvider: tracerProvider, LoggerProvider: loggerProvider})
			if err != nil {
				return err
			}
			model := corechat.ModelFunc(func(context.Context, *corechat.Request) (*corechat.Response, error) { return nil, failure })
			wrapped := middleware.Call(model)
			_, err = wrapped.Call(ctx, nil)
			return err
		}},
		{name: "embedding", call: func(ctx context.Context, tracerProvider trace.TracerProvider, loggerProvider log.LoggerProvider, failure error) error {
			middleware, err := otelembedding.NewMiddleware(otelembedding.MiddlewareConfig{Provider: "provider", TracerProvider: tracerProvider, LoggerProvider: loggerProvider})
			if err != nil {
				return err
			}
			model := coreembedding.ModelFunc(func(context.Context, *coreembedding.Request) (*coreembedding.Response, error) { return nil, failure })
			wrapped, err := middleware.Wrap(model)
			if err != nil {
				return err
			}
			_, err = wrapped.Call(ctx, nil)
			return err
		}},
		{name: "image", call: func(ctx context.Context, tracerProvider trace.TracerProvider, loggerProvider log.LoggerProvider, failure error) error {
			middleware, err := otelimage.NewMiddleware(otelimage.MiddlewareConfig{Provider: "provider", TracerProvider: tracerProvider, LoggerProvider: loggerProvider})
			if err != nil {
				return err
			}
			model := coreimage.ModelFunc(func(context.Context, *coreimage.Request) (*coreimage.Response, error) { return nil, failure })
			wrapped, err := middleware.Wrap(model)
			if err != nil {
				return err
			}
			_, err = wrapped.Call(ctx, nil)
			return err
		}},
		{name: "moderation", call: func(ctx context.Context, tracerProvider trace.TracerProvider, loggerProvider log.LoggerProvider, failure error) error {
			middleware, err := otelmoderation.NewMiddleware(otelmoderation.MiddlewareConfig{Provider: "provider", TracerProvider: tracerProvider, LoggerProvider: loggerProvider})
			if err != nil {
				return err
			}
			model := coremoderation.ModelFunc(func(context.Context, *coremoderation.Request) (*coremoderation.Response, error) { return nil, failure })
			wrapped, err := middleware.Wrap(model)
			if err != nil {
				return err
			}
			_, err = wrapped.Call(ctx, nil)
			return err
		}},
		{name: "rerank", call: func(ctx context.Context, tracerProvider trace.TracerProvider, loggerProvider log.LoggerProvider, failure error) error {
			middleware, err := otelrerank.NewMiddleware(otelrerank.MiddlewareConfig{Provider: "provider", TracerProvider: tracerProvider, LoggerProvider: loggerProvider})
			if err != nil {
				return err
			}
			model := corererank.ModelFunc(func(context.Context, *corererank.Request) (*corererank.Response, error) { return nil, failure })
			wrapped, err := middleware.Wrap(model)
			if err != nil {
				return err
			}
			_, err = wrapped.Call(ctx, nil)
			return err
		}},
		{name: "speech", call: func(ctx context.Context, tracerProvider trace.TracerProvider, loggerProvider log.LoggerProvider, failure error) error {
			middleware, err := otelspeech.NewMiddleware(otelspeech.MiddlewareConfig{Provider: "provider", TracerProvider: tracerProvider, LoggerProvider: loggerProvider})
			if err != nil {
				return err
			}
			model := corespeech.ModelFunc(func(context.Context, *corespeech.Request) (*corespeech.Response, error) { return nil, failure })
			wrapped, err := middleware.Wrap(model)
			if err != nil {
				return err
			}
			_, err = wrapped.Call(ctx, nil)
			return err
		}},
		{name: "transcription", call: func(ctx context.Context, tracerProvider trace.TracerProvider, loggerProvider log.LoggerProvider, failure error) error {
			middleware, err := oteltranscription.NewMiddleware(oteltranscription.MiddlewareConfig{Provider: "provider", TracerProvider: tracerProvider, LoggerProvider: loggerProvider})
			if err != nil {
				return err
			}
			model := coretranscription.ModelFunc(func(context.Context, *coretranscription.Request) (*coretranscription.Response, error) {
				return nil, failure
			})
			wrapped, err := middleware.Wrap(model)
			if err != nil {
				return err
			}
			_, err = wrapped.Call(ctx, nil)
			return err
		}},
		{name: "chat stream", call: func(ctx context.Context, tracerProvider trace.TracerProvider, loggerProvider log.LoggerProvider, failure error) error {
			middleware, err := otelchat.NewMiddleware(otelchat.MiddlewareConfig{Provider: "provider", TracerProvider: tracerProvider, LoggerProvider: loggerProvider})
			if err != nil {
				return err
			}
			streamer := corechat.StreamerFunc(func(context.Context, *corechat.Request) iter.Seq2[*corechat.ResponseDelta, error] {
				return func(yield func(*corechat.ResponseDelta, error) bool) { yield(nil, failure) }
			})
			wrapped := middleware.Stream(streamer)
			for _, streamErr := range wrapped.Stream(ctx, nil) {
				if streamErr != nil {
					return streamErr
				}
			}
			return nil
		}},
		{name: "speech stream", call: func(ctx context.Context, tracerProvider trace.TracerProvider, loggerProvider log.LoggerProvider, failure error) error {
			middleware, err := otelspeech.NewMiddleware(otelspeech.MiddlewareConfig{Provider: "provider", TracerProvider: tracerProvider, LoggerProvider: loggerProvider})
			if err != nil {
				return err
			}
			streamer := corespeech.StreamerFunc(func(context.Context, *corespeech.Request) iter.Seq2[*corespeech.Response, error] {
				return func(yield func(*corespeech.Response, error) bool) { yield(nil, failure) }
			})
			wrapped, err := middleware.WrapStream(streamer)
			if err != nil {
				return err
			}
			for _, streamErr := range wrapped.Stream(ctx, nil) {
				if streamErr != nil {
					return streamErr
				}
			}
			return nil
		}},
	}
	for _, call := range calls {
		t.Run(call.name, func(t *testing.T) {
			for _, failure := range []struct {
				name           string
				err            error
				classification string
			}{
				{name: "provider", err: errors.New("private-provider-payload"), classification: "*errors.errorString"},
				{name: "canceled", err: fmt.Errorf("private-provider-payload: %w", context.Canceled), classification: "context.canceled"},
			} {
				t.Run(failure.name, func(t *testing.T) {
					spans := tracetest.NewSpanRecorder()
					tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
					exporter := new(exceptionLogExporter)
					loggerProvider := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(exporter)))
					t.Cleanup(func() {
						if err := tracerProvider.Shutdown(context.Background()); err != nil {
							t.Error(err)
						}
						if err := loggerProvider.Shutdown(context.Background()); err != nil {
							t.Error(err)
						}
					})
					ctx, cancel := context.WithCancel(t.Context())
					defer cancel()
					if errors.Is(failure.err, context.Canceled) {
						cancel()
					}
					if err := call.call(ctx, tracerProvider, loggerProvider, failure.err); !errors.Is(err, failure.err) {
						t.Fatalf("call error = %v, want original provider error", err)
					}
					ended := spans.Ended()
					if len(ended) != 1 {
						t.Fatalf("spans = %d, want 1", len(ended))
					}
					span := ended[0]
					if strings.Contains(span.Status().Description, "private-provider-payload") {
						t.Error("raw error leaked through span status")
					}
					attributes := attribute.NewSet(span.Attributes()...)
					value, found := attributes.Value("error.type")
					if !found || value.AsString() != failure.classification {
						t.Errorf("span error.type = %v, want %s", value, failure.classification)
					}
					for _, event := range span.Events() {
						if event.Time.Before(span.StartTime()) || event.Time.After(span.EndTime()) {
							t.Error("exception event timestamp is outside the observed call")
						}
						for _, entry := range event.Attributes {
							if entry.Key == "exception.message" || strings.Contains(entry.Value.String(), "private-provider-payload") {
								t.Errorf("raw error exported in event attribute %s", entry.Key)
							}
						}
					}
					if len(exporter.records) != 1 {
						t.Fatalf("exception logs = %d, want 1", len(exporter.records))
					}
					record := exporter.records[0]
					if record.EventName() != "gen_ai.client.operation.exception" || record.Severity() != log.SeverityWarn {
						t.Errorf("event/severity = %q/%v", record.EventName(), record.Severity())
					}
					if record.TraceID() != span.SpanContext().TraceID() || record.SpanID() != span.SpanContext().SpanID() {
						t.Error("exception log is not correlated with the failed model span")
					}
					if record.Timestamp().Before(span.StartTime()) || record.Timestamp().After(span.EndTime()) {
						t.Error("exception log timestamp is outside the observed call")
					}
					if record.Body().Type() != attribute.EMPTY {
						t.Error("exception record contains unclassified content")
					}
					var logAttributes []attribute.KeyValue
					record.WalkAttributes(func(value attribute.KeyValue) bool { logAttributes = append(logAttributes, value); return true })
					if len(logAttributes) != 1 || logAttributes[0].Key != "exception.type" || logAttributes[0].Value.AsString() != failure.classification {
						t.Errorf("exception attributes = %v", logAttributes)
					}
				})
			}
		})
	}
}

type exceptionLogExporter struct{ records []sdklog.Record }

func (e *exceptionLogExporter) Export(ctx context.Context, records []sdklog.Record) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, record := range records {
		e.records = append(e.records, record.Clone())
	}
	return nil
}
func (*exceptionLogExporter) Shutdown(context.Context) error   { return nil }
func (*exceptionLogExporter) ForceFlush(context.Context) error { return nil }
