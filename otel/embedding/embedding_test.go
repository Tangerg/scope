package embedding_test

import (
	"context"
	"errors"
	"testing"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	coreembedding "github.com/Tangerg/scope/core/embedding"
	otelembedding "github.com/Tangerg/scope/otel/embedding"
)

func TestMiddlewarePreservesCallAndDoesNotObserveContent(t *testing.T) {
	spans := tracetest.NewSpanRecorder()
	traces := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()), sdktrace.WithSpanProcessor(spans))
	meters := sdkmetric.NewMeterProvider()
	t.Cleanup(func() {
		_ = traces.Shutdown(context.Background())
		_ = meters.Shutdown(context.Background())
	})
	middleware, err := otelembedding.NewMiddleware(otelembedding.MiddlewareConfig{
		Provider: " Provider ", TracerProvider: traces, MeterProvider: meters,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := &coreembedding.Response{
		Outputs: []*coreembedding.Output{{Embedding: []float64{0.25}}},
		Metadata: &coreembedding.ResponseMetadata{
			Model: "served", Usage: &coreembedding.Usage{InputTokens: 3},
		},
	}
	model, err := middleware.Wrap(coreembedding.ModelFunc(func(context.Context, *coreembedding.Request) (*coreembedding.Response, error) {
		return want, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	request, _ := coreembedding.NewRequest([]string{"sensitive input"})
	request.Options.Model = "requested"
	got, err := model.Call(t.Context(), request)
	if err != nil || got != want {
		t.Fatalf("Call = (%p, %v), want (%p, nil)", got, err, want)
	}
	if len(spans.Ended()) == 0 {
		t.Fatal("expected a recorded span")
	}
	span := spans.Ended()[0]
	if span.Name() != "embeddings requested" {
		t.Fatalf("span name = %q", span.Name())
	}
	for _, value := range span.Attributes() {
		if value.Value.AsString() == request.Texts[0] {
			t.Fatal("embedding input leaked into telemetry")
		}
	}
}

func TestMiddlewareRejectsInvalidConstruction(t *testing.T) {
	if _, err := otelembedding.NewMiddleware(otelembedding.MiddlewareConfig{}); !errors.Is(err, otelembedding.ErrInvalidConfig) {
		t.Fatalf("NewMiddleware error = %v", err)
	}
	middleware, err := otelembedding.NewMiddleware(otelembedding.MiddlewareConfig{Provider: "provider"})
	if err != nil {
		t.Fatal(err)
	}
	var model coreembedding.ModelFunc
	if _, err := middleware.Wrap(model); !errors.Is(err, otelembedding.ErrInvalidModel) {
		t.Fatalf("Wrap error = %v", err)
	}
}
