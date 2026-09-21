package image_test

import (
	"context"
	"errors"
	"testing"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	coreimage "github.com/Tangerg/scope/core/image"
	otelimage "github.com/Tangerg/scope/otel/image"
)

func TestMiddlewarePreservesCallAndDoesNotObservePrompts(t *testing.T) {
	spans := tracetest.NewSpanRecorder()
	traces := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()), sdktrace.WithSpanProcessor(spans))
	meters := sdkmetric.NewMeterProvider()
	t.Cleanup(func() {
		_ = traces.Shutdown(context.Background())
		_ = meters.Shutdown(context.Background())
	})
	middleware, err := otelimage.NewMiddleware(otelimage.MiddlewareConfig{
		Provider: "provider", TracerProvider: traces, MeterProvider: meters,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := &coreimage.Response{}
	model, err := middleware.Wrap(coreimage.ModelFunc(func(context.Context, *coreimage.Request) (*coreimage.Response, error) {
		return want, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	request, _ := coreimage.NewRequest("sensitive prompt")
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
		if value.Value.AsString() == request.Prompt {
			t.Fatal("image prompt leaked into telemetry")
		}
	}
}

func TestMiddlewareRejectsInvalidConstruction(t *testing.T) {
	if _, err := otelimage.NewMiddleware(otelimage.MiddlewareConfig{}); !errors.Is(err, otelimage.ErrInvalidConfig) {
		t.Fatalf("NewMiddleware error = %v", err)
	}
	middleware, err := otelimage.NewMiddleware(otelimage.MiddlewareConfig{Provider: "provider"})
	if err != nil {
		t.Fatal(err)
	}
	var model coreimage.ModelFunc
	if _, err := middleware.Wrap(model); !errors.Is(err, otelimage.ErrInvalidModel) {
		t.Fatalf("Wrap error = %v", err)
	}
}
