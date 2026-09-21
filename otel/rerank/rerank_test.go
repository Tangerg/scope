package rerank_test

import (
	"context"
	"errors"
	"testing"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	corererank "github.com/Tangerg/scope/core/rerank"
	otelrerank "github.com/Tangerg/scope/otel/rerank"
)

func TestMiddlewarePreservesCallAndDoesNotObserveContent(t *testing.T) {
	spans := tracetest.NewSpanRecorder()
	traces := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()), sdktrace.WithSpanProcessor(spans))
	meters := sdkmetric.NewMeterProvider()
	t.Cleanup(func() {
		_ = traces.Shutdown(context.Background())
		_ = meters.Shutdown(context.Background())
	})
	middleware, err := otelrerank.NewMiddleware(otelrerank.MiddlewareConfig{
		Provider: " Provider ", TracerProvider: traces, MeterProvider: meters,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := &corererank.Response{
		Results: []*corererank.Result{{Index: 1, Score: 0.9}},
		Metadata: &corererank.ResponseMetadata{
			Model: "served", Usage: &corererank.Usage{InputTokens: 3},
		},
	}
	model, err := middleware.Wrap(corererank.ModelFunc(func(context.Context, *corererank.Request) (*corererank.Response, error) {
		return want, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	request, _ := corererank.NewRequest("sensitive query", []string{"sensitive document"})
	request.Options.Model = "requested"
	got, err := model.Call(t.Context(), request)
	if err != nil || got != want {
		t.Fatalf("Call = (%p, %v), want (%p, nil)", got, err, want)
	}
	if len(spans.Ended()) == 0 {
		t.Fatal("expected a recorded span")
	}
	span := spans.Ended()[0]
	if span.Name() != "rerank requested" {
		t.Fatalf("span name = %q", span.Name())
	}
	for _, value := range span.Attributes() {
		observed := value.Value.AsString()
		if observed == request.Query || observed == request.Documents[0] {
			t.Fatal("reranking content leaked into telemetry")
		}
	}
}

func TestMiddlewareRejectsInvalidConstruction(t *testing.T) {
	if _, err := otelrerank.NewMiddleware(otelrerank.MiddlewareConfig{}); !errors.Is(err, otelrerank.ErrInvalidConfig) {
		t.Fatalf("NewMiddleware error = %v", err)
	}
	middleware, err := otelrerank.NewMiddleware(otelrerank.MiddlewareConfig{Provider: "provider"})
	if err != nil {
		t.Fatal(err)
	}
	var model corererank.ModelFunc
	if _, err := middleware.Wrap(model); !errors.Is(err, otelrerank.ErrInvalidModel) {
		t.Fatalf("Wrap error = %v", err)
	}
}
