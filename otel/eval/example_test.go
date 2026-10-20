package eval_test

import (
	"context"
	"fmt"

	"github.com/samber/lo"

	coreeval "github.com/Tangerg/scope/eval"
	oteleval "github.com/Tangerg/scope/otel/eval"
)

// ExampleMiddleware_Wrap instruments an Assessment without changing its verdict.
// The subject value never enters telemetry; only the metric identity, verdict,
// and score become span and metric attributes.
func ExampleMiddleware_Wrap() {
	middleware, err := oteleval.NewMiddleware[string](oteleval.MiddlewareConfig{})
	if err != nil {
		panic(err)
	}
	metric, err := coreeval.NewMetric(coreeval.MetricConfig{Namespace: "text", Name: "correctness"})
	if err != nil {
		panic(err)
	}
	score, err := coreeval.NewScore(0.9)
	if err != nil {
		panic(err)
	}
	report := coreeval.Report{
		Metric:   metric,
		Decision: &coreeval.Decision{Policy: "correctness", Verdict: coreeval.VerdictPass},
		Score:    &score,
	}
	assessment, err := middleware.Wrap(lo.Must(coreeval.NewAssessment(coreeval.AssessmentConfig[string]{
		ID: "correctness",
		Evaluator: coreeval.EvaluatorFunc[string](func(context.Context, string) (coreeval.Report, error) {
			return report, nil
		}),
	})))
	if err != nil {
		panic(err)
	}
	got, err := assessment.Evaluate(context.Background(), "the answer under test")
	if err != nil {
		panic(err)
	}
	fmt.Println(got.Decision.Verdict == coreeval.VerdictPass)
	// Output:
	// true
}
