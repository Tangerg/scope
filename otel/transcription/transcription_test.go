package transcription_test

import (
	"context"
	"errors"
	"testing"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	coretranscription "github.com/Tangerg/scope/core/transcription"
	oteltranscription "github.com/Tangerg/scope/otel/transcription"
)

func TestMiddlewarePreservesCallAndDoesNotObserveTranscript(t *testing.T) {
	spans := tracetest.NewSpanRecorder()
	traces := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()), sdktrace.WithSpanProcessor(spans))
	meters := sdkmetric.NewMeterProvider()
	t.Cleanup(func() {
		_ = traces.Shutdown(context.Background())
		_ = meters.Shutdown(context.Background())
	})
	middleware, err := oteltranscription.NewMiddleware(oteltranscription.MiddlewareConfig{
		Provider: "provider", TracerProvider: traces, MeterProvider: meters,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := &coretranscription.Response{
		Output:   &coretranscription.Output{Text: "sensitive transcript"},
		Metadata: &coretranscription.ResponseMetadata{Model: "served"},
	}
	model, err := middleware.Wrap(coretranscription.ModelFunc(func(context.Context, *coretranscription.Request) (*coretranscription.Response, error) {
		return want, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	request := &coretranscription.Request{Options: coretranscription.Options{Model: "requested"}}
	got, err := model.Call(t.Context(), request)
	if err != nil || got != want {
		t.Fatalf("Call = (%p, %v), want (%p, nil)", got, err, want)
	}
	if len(spans.Ended()) == 0 {
		t.Fatal("expected a recorded span")
	}
	span := spans.Ended()[0]
	for _, value := range span.Attributes() {
		if value.Value.AsString() == want.Output.Text {
			t.Fatal("transcript leaked into telemetry")
		}
	}
}

func TestMiddlewareRejectsInvalidConstruction(t *testing.T) {
	if _, err := oteltranscription.NewMiddleware(oteltranscription.MiddlewareConfig{}); !errors.Is(err, oteltranscription.ErrInvalidConfig) {
		t.Fatalf("NewMiddleware error = %v", err)
	}
	middleware, err := oteltranscription.NewMiddleware(oteltranscription.MiddlewareConfig{Provider: "provider"})
	if err != nil {
		t.Fatal(err)
	}
	var model coretranscription.ModelFunc
	if _, err := middleware.Wrap(model); !errors.Is(err, oteltranscription.ErrInvalidModel) {
		t.Fatalf("Wrap error = %v", err)
	}
}
