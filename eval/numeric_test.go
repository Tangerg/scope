package eval_test

import (
	"context"
	"math"
	"testing"

	"github.com/Tangerg/scope/eval"
)

func TestCompositeIsInvariantToWeightScale(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		weight float64
	}{
		{name: "ordinary", weight: 1},
		{name: "largest_finite", weight: math.MaxFloat64},
		{name: "smallest_positive", weight: math.SmallestNonzeroFloat64},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			for _, scoreCase := range []struct {
				name  string
				score eval.Score
			}{
				{name: "half", score: 0.5},
				{name: "perfect", score: 1},
			} {
				t.Run(scoreCase.name, func(t *testing.T) {
					evaluator := eval.EvaluatorFunc[int](func(context.Context, int) (eval.Report, error) {
						return scoredReport("quality", eval.VerdictPass, scoreCase.score), nil
					})
					composite, err := eval.NewCompositeEvaluator(eval.CompositeConfig[int]{
						Components: []eval.Component[int]{
							{Evaluator: evaluator, Weight: testCase.weight},
							{Evaluator: evaluator, Weight: testCase.weight},
						},
					})
					if err != nil {
						t.Fatal(err)
					}
					report, err := composite.Evaluate(t.Context(), 0)
					if err != nil {
						t.Fatal(err)
					}
					if report.Score == nil {
						t.Fatal("composite report has no score")
					}
					if got := *report.Score; got != scoreCase.score {
						t.Fatalf("score = %g, want %g", got, scoreCase.score)
					}
				})
			}
		})
	}
}

func TestExperimentSummarizesFiniteMeasurements(t *testing.T) {
	for _, testCase := range []struct {
		name          string
		first, second float64
		mean          float64
	}{
		{name: "ordinary", first: 2, second: 4, mean: 3},
		{name: "zero"},
		{name: "largest_finite", first: math.MaxFloat64, second: math.MaxFloat64, mean: math.MaxFloat64},
		{name: "smallest_positive", first: math.SmallestNonzeroFloat64, second: math.SmallestNonzeroFloat64, mean: math.SmallestNonzeroFloat64},
		{name: "negative", first: -math.MaxFloat64, second: -math.MaxFloat64, mean: -math.MaxFloat64},
		{name: "opposite", first: -math.MaxFloat64, second: math.MaxFloat64},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			dataset, err := eval.NewDataset(
				eval.Case[float64]{ID: "first", Subject: testCase.first},
				eval.Case[float64]{ID: "second", Subject: testCase.second},
			)
			if err != nil {
				t.Fatal(err)
			}
			experiment, err := eval.NewExperiment(eval.ExperimentConfig[float64]{
				Dataset: dataset,
				Evaluator: eval.EvaluatorFunc[float64](func(_ context.Context, value float64) (eval.Report, error) {
					return eval.Report{Metric: testMetric("measurement"), Measurement: &value}, nil
				}),
			})
			if err != nil {
				t.Fatal(err)
			}
			report, err := experiment.Run(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			summary := report.Summary()
			if len(summary.Metrics) != 1 {
				t.Fatalf("metric count = %d, want 1", len(summary.Metrics))
			}
			if got := summary.Metrics[0].Measurements.Mean; got != testCase.mean {
				t.Fatalf("mean = %g, want %g", got, testCase.mean)
			}
		})
	}
}
