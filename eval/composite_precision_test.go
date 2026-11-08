package eval_test

import (
	"context"
	"math"
	"testing"

	"github.com/Tangerg/scope/eval"
)

func TestCompositePreservesRepresentableScoreAndThresholdVerdict(t *testing.T) {
	metric, err := eval.NewMetric(eval.MetricConfig{Name: "subnormal"})
	if err != nil {
		t.Fatal(err)
	}
	input := eval.Score(math.SmallestNonzeroFloat64)
	child := eval.EvaluatorFunc[int](func(context.Context, int) (eval.Report, error) {
		return eval.Report{Metric: metric, Score: &input}, nil
	})
	composite, err := eval.NewCompositeEvaluator(eval.CompositeEvaluatorConfig[int]{Components: []eval.Component[int]{{Evaluator: child, Weight: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := composite.Evaluate(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Score == nil || *result.Score != input {
		t.Fatalf("representable score was not retained: %+v", result)
	}
	direct, err := input.Decide(input)
	if err != nil {
		t.Fatal(err)
	}
	combined, err := result.Score.Decide(input)
	if err != nil {
		t.Fatal(err)
	}
	if direct.Verdict != eval.VerdictPass || combined.Verdict != eval.VerdictPass {
		t.Fatal("same threshold changed the decision")
	}
}
