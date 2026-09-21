package moderation_test

import (
	"context"
	"errors"
	"testing"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	coremoderation "github.com/Tangerg/scope/core/moderation"
	otelmoderation "github.com/Tangerg/scope/otel/moderation"
)

func TestMiddlewarePreservesCallAndDoesNotObserveContent(t *testing.T) {
	spans := tracetest.NewSpanRecorder()
	traces := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()), sdktrace.WithSpanProcessor(spans))
	meters := sdkmetric.NewMeterProvider()
	t.Cleanup(func() {
		_ = traces.Shutdown(context.Background())
		_ = meters.Shutdown(context.Background())
	})
	middleware, err := otelmoderation.NewMiddleware(otelmoderation.MiddlewareConfig{
		Provider: "provider", TracerProvider: traces, MeterProvider: meters,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := &coremoderation.Response{Metadata: &coremoderation.ResponseMetadata{Model: "served"}}
	model, err := middleware.Wrap(coremoderation.ModelFunc(func(context.Context, *coremoderation.Request) (*coremoderation.Response, error) {
		return want, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	request, _ := coremoderation.NewRequest([]string{"sensitive input"})
	request.Options.Model = "requested"
	got, err := model.Call(t.Context(), request)
	if err != nil || got != want {
		t.Fatalf("Call = (%p, %v), want (%p, nil)", got, err, want)
	}
	if len(spans.Ended()) == 0 {
		t.Fatal("expected a recorded span")
	}
	span := spans.Ended()[0]
	for _, value := range span.Attributes() {
		if value.Value.AsString() == request.Texts[0] {
			t.Fatal("moderation input leaked into telemetry")
		}
	}
}

func TestMiddlewareRejectsInvalidConstruction(t *testing.T) {
	if _, err := otelmoderation.NewMiddleware(otelmoderation.MiddlewareConfig{}); !errors.Is(err, otelmoderation.ErrInvalidConfig) {
		t.Fatalf("NewMiddleware error = %v", err)
	}
	middleware, err := otelmoderation.NewMiddleware(otelmoderation.MiddlewareConfig{Provider: "provider"})
	if err != nil {
		t.Fatal(err)
	}
	var model coremoderation.ModelFunc
	if _, err := middleware.Wrap(model); !errors.Is(err, otelmoderation.ErrInvalidModel) {
		t.Fatalf("Wrap error = %v", err)
	}
}
