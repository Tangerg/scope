package eval_test

import (
	"context"
	"errors"
	"testing"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	coreeval "github.com/Tangerg/scope/eval"
	oteleval "github.com/Tangerg/scope/otel/eval"
)

func TestMiddlewareObservesOutcomeWithoutSubject(t *testing.T) {
	spans := tracetest.NewSpanRecorder()
	traces := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()), sdktrace.WithSpanProcessor(spans))
	meters := sdkmetric.NewMeterProvider()
	t.Cleanup(func() {
		_ = traces.Shutdown(context.Background())
		_ = meters.Shutdown(context.Background())
	})
	middleware, err := oteleval.NewMiddleware[string](oteleval.MiddlewareConfig{
		TracerProvider: traces, MeterProvider: meters,
	})
	if err != nil {
		t.Fatal(err)
	}
	metric, _ := coreeval.NewMetric(coreeval.MetricConfig{
		Namespace: "text", Name: "correctness",
	})
	score, _ := coreeval.NewScore(0.9)
	want := coreeval.Report{Metric: metric, Verdict: coreeval.VerdictPass, Score: &score}
	evaluator, err := middleware.Wrap(coreeval.EvaluatorFunc[string](func(context.Context, string) (coreeval.Report, error) {
		return want, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	const subject = "sensitive evaluation subject"
	got, err := evaluator.Evaluate(t.Context(), subject)
	if err != nil || got.Metric.String() != want.Metric.String() {
		t.Fatalf("Evaluate = (%#v, %v)", got, err)
	}
	if len(spans.Ended()) == 0 {
		t.Fatal("expected a recorded span")
	}
	span := spans.Ended()[0]
	if span.Name() != "eval.evaluate" {
		t.Fatalf("span name = %q", span.Name())
	}
	for _, value := range span.Attributes() {
		if value.Value.AsString() == subject {
			t.Fatal("evaluation subject leaked into telemetry")
		}
	}
}

func TestMiddlewareRejectsInvalidConstruction(t *testing.T) {
	var zero oteleval.Middleware[int]
	if _, err := zero.Wrap(coreeval.EvaluatorFunc[int](func(context.Context, int) (coreeval.Report, error) {
		return coreeval.Report{}, nil
	})); !errors.Is(err, oteleval.ErrInvalidConfig) {
		t.Fatalf("zero Wrap error = %v", err)
	}
	middleware, err := oteleval.NewMiddleware[int](oteleval.MiddlewareConfig{})
	if err != nil {
		t.Fatal(err)
	}
	var evaluator coreeval.EvaluatorFunc[int]
	if _, err := middleware.Wrap(evaluator); !errors.Is(err, oteleval.ErrInvalidEvaluator) {
		t.Fatalf("Wrap error = %v", err)
	}
}
