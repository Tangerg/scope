package eval_test

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/Tangerg/scope/core/metadata"
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
	want := coreeval.Report{Metric: metric, Decision: &coreeval.Decision{Policy: "correctness", Verdict: coreeval.VerdictPass}, Score: &score}
	assessment, err := middleware.Wrap(coreeval.Assessment[string]{
		ID: "correctness",
		Evaluator: coreeval.EvaluatorFunc[string](func(context.Context, string) (coreeval.Report, error) {
			return want, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	const subject = "sensitive evaluation subject"
	got, err := assessment.Evaluator.Evaluate(t.Context(), subject)
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
	if _, err := zero.Wrap(coreeval.Assessment[int]{ID: "check", Evaluator: coreeval.EvaluatorFunc[int](func(context.Context, int) (coreeval.Report, error) {
		return coreeval.Report{}, nil
	})}); !errors.Is(err, oteleval.ErrInvalidConfig) {
		t.Fatalf("zero Wrap error = %v", err)
	}
	middleware, err := oteleval.NewMiddleware[int](oteleval.MiddlewareConfig{})
	if err != nil {
		t.Fatal(err)
	}
	var evaluator coreeval.EvaluatorFunc[int]
	if _, err := middleware.Wrap(coreeval.Assessment[int]{ID: "check", Evaluator: evaluator}); !errors.Is(err, oteleval.ErrInvalidAssessment) {
		t.Fatalf("Wrap error = %v", err)
	}
	if _, err := middleware.Wrap(coreeval.Assessment[int]{Evaluator: coreeval.EvaluatorFunc[int](func(context.Context, int) (coreeval.Report, error) {
		return coreeval.Report{}, nil
	})}); !errors.Is(err, oteleval.ErrInvalidAssessment) {
		t.Fatalf("missing identity Wrap error = %v", err)
	}
}

func TestAssessmentIdentityPrecedesEveryOutcome(t *testing.T) {
	for _, outcome := range []string{"pass", "fail", "error", "panic", "invalid_report"} {
		t.Run(outcome, func(t *testing.T) {
			spans := tracetest.NewSpanRecorder()
			traces := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()), sdktrace.WithSpanProcessor(spans))
			reader := sdkmetric.NewManualReader()
			meters := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
			t.Cleanup(func() {
				_ = traces.Shutdown(context.Background())
				_ = meters.Shutdown(context.Background())
			})
			middleware, err := oteleval.NewMiddleware[string](oteleval.MiddlewareConfig{TracerProvider: traces, MeterProvider: meters})
			if err != nil {
				t.Fatal(err)
			}
			metricValue, err := coreeval.NewMetric(coreeval.MetricConfig{Namespace: "quality", Name: "match"})
			if err != nil {
				t.Fatal(err)
			}
			const secret = "private-assessment-content"
			failure := errors.New(secret)
			var parameters metadata.Map
			if parameterErr := parameters.Set("reference", secret); parameterErr != nil {
				t.Fatal(parameterErr)
			}
			decision := coreeval.Decision{Policy: "quality.match", Verdict: coreeval.VerdictPass, Parameters: parameters}
			if outcome == "fail" {
				decision.Verdict = coreeval.VerdictFail
			}
			want := coreeval.Report{Metric: metricValue, Decision: &decision, Feedback: secret}
			if outcome == "invalid_report" {
				invalidScore := coreeval.Score(math.NaN())
				want.Score = &invalidScore
			}
			assessment, err := middleware.Wrap(coreeval.Assessment[string]{
				ID: "quality.check",
				Evaluator: coreeval.EvaluatorFunc[string](func(_ context.Context, subject string) (coreeval.Report, error) {
					if subject != secret || len(spans.Started()) != 1 {
						t.Fatal("assessment subject changed or span started late")
					}
					attributes := attribute.NewSet(spans.Started()[0].Attributes()...)
					identity, _ := attributes.Value("eval.assessment.id")
					if identity.AsString() != "quality.check" {
						t.Fatal("assessment identity missing before delegate invocation")
					}
					if outcome == "panic" {
						panic(failure)
					}
					if outcome == "error" {
						return want, failure
					}
					return want, nil
				}),
			})
			if err != nil || assessment.ID != "quality.check" {
				t.Fatalf("Wrap = (%v, %v)", assessment.ID, err)
			}
			var got coreeval.Report
			var callErr error
			var panicked any
			func() {
				defer func() { panicked = recover() }()
				got, callErr = assessment.Evaluator.Evaluate(t.Context(), secret)
			}()
			if outcome == "panic" {
				if panicked != any(failure) || callErr != nil {
					t.Fatalf("panic = %v, want original value", panicked)
				}
			} else if outcome == "error" {
				if any(callErr) != any(failure) || !reflect.DeepEqual(got, coreeval.Report{}) {
					t.Fatalf("error = %v, want original error", callErr)
				}
			} else if outcome == "invalid_report" {
				if !errors.Is(callErr, coreeval.ErrInvalidReport) || !reflect.DeepEqual(got, coreeval.Report{}) {
					t.Fatalf("invalid report = (%#v, %v), want empty report and ErrInvalidReport", got, callErr)
				}
			} else if callErr != nil || panicked != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("assessment outcome changed: (%#v, %v, %v)", got, callErr, panicked)
			}
			if len(spans.Ended()) != 1 {
				t.Fatalf("ended spans = %d, want 1", len(spans.Ended()))
			}
			span := spans.Ended()[0]
			failed := outcome == "error" || outcome == "panic" || outcome == "invalid_report"
			if (span.Status().Code == codes.Error) != failed {
				t.Fatalf("span status = %v, evaluator execution failed = %v", span.Status(), failed)
			}
			attributes := attribute.NewSet(span.Attributes()...)
			verdict, hasVerdict := attributes.Value("eval.verdict")
			if hasVerdict == failed || (!failed && verdict.AsString() != string(want.Verdict())) {
				t.Fatalf("verdict = %v (present %v)", verdict, hasVerdict)
			}
			if outcome == "invalid_report" {
				classification, _ := attributes.Value("error.type")
				if classification.AsString() != "eval.invalid_report" || attributes.HasValue("eval.score") || attributes.HasValue("eval.metric.name") {
					t.Fatalf("invalid report polluted result telemetry: %v", span.Attributes())
				}
			}
			for _, entry := range span.Attributes() {
				if strings.Contains(entry.Value.String(), secret) {
					t.Fatalf("assessment content leaked into %s", entry.Key)
				}
			}
			var metrics metricdata.ResourceMetrics
			if err := reader.Collect(t.Context(), &metrics); err != nil {
				t.Fatal(err)
			}
			if len(metrics.ScopeMetrics) != 1 || len(metrics.ScopeMetrics[0].Metrics) != 1 {
				t.Fatalf("metrics = %#v", metrics)
			}
			duration := metrics.ScopeMetrics[0].Metrics[0].Data.(metricdata.Histogram[float64])
			if len(duration.DataPoints) != 1 || duration.DataPoints[0].Count != 1 {
				t.Fatalf("duration points = %#v", duration.DataPoints)
			}
			identity, _ := duration.DataPoints[0].Attributes.Value("eval.assessment.id")
			if identity.AsString() != "quality.check" {
				t.Fatalf("duration assessment identity = %q", identity.AsString())
			}
		})
	}
}
