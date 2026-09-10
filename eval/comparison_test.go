package eval_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/eval"
)

func comparisonReport(t *testing.T, parameters string, fail bool) eval.ExperimentReport {
	t.Helper()
	metric, err := eval.NewMetric(eval.MetricConfig{
		Name: "quality", Parameters: metadata.Map{"rule": json.RawMessage(parameters)},
	})
	if err != nil {
		t.Fatal(err)
	}
	dataset, err := eval.NewDataset(eval.Case[int]{ID: "same", Subject: 1})
	if err != nil {
		t.Fatal(err)
	}
	experiment, err := eval.NewExperiment(eval.ExperimentConfig[int]{
		Dataset: dataset,
		Evaluator: eval.EvaluatorFunc[int](func(context.Context, int) (eval.Report, error) {
			if fail {
				return eval.Report{}, errors.New("evaluation failed")
			}
			score := eval.Score(1)
			return eval.Report{Metric: metric, Score: &score, Verdict: eval.VerdictPass}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err := experiment.Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func TestComparisonRetainsExecutionFailuresAndMissingMetrics(t *testing.T) {
	success := comparisonReport(t, `{}`, false)
	failure := comparisonReport(t, `{}`, true)
	for _, test := range []struct {
		name                string
		baseline, candidate eval.ExperimentReport
		errors              int
		baselinePresent     bool
	}{
		{name: "regression", baseline: success, candidate: failure, errors: 1, baselinePresent: true},
		{name: "recovery", baseline: failure, candidate: success, errors: -1},
	} {
		t.Run(test.name, func(t *testing.T) {
			comparison, err := test.baseline.Compare(test.candidate)
			if err != nil {
				t.Fatal(err)
			}
			if comparison.ErrorDelta != test.errors || comparison.EvaluatedDelta != -test.errors || len(comparison.Metrics) != 1 {
				t.Fatalf("comparison = %#v", comparison)
			}
			metric := comparison.Metrics[0]
			if (metric.Baseline != nil) != test.baselinePresent || (metric.Candidate != nil) == test.baselinePresent ||
				metric.ScoreDelta.Present || metric.MeasurementDelta.Present || metric.EvaluatedDelta != -test.errors {
				t.Fatalf("missing metric comparison = %#v", metric)
			}
		})
	}
}

func TestMetricIdentityIgnoresJSONPresentationWithoutRoundingNumbers(t *testing.T) {
	baseline := comparisonReport(t, `{"a":[{"x":"<","y":9007199254740993}],"b":1.00000000000000001}`, false)
	for _, test := range []struct {
		name, parameters string
		shared           bool
	}{
		{name: "presentation", parameters: ` { "b": 1.00000000000000001, "a": [{"y":9007199254740993,"x":"\u003c"}] } `, shared: true},
		{name: "integer precision", parameters: `{"a":[{"x":"<","y":9007199254740992}],"b":1.00000000000000001}`},
		{name: "decimal precision", parameters: `{"a":[{"x":"<","y":9007199254740993}],"b":1.00000000000000002}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			comparison, err := baseline.Compare(comparisonReport(t, test.parameters, false))
			if err != nil {
				t.Fatal(err)
			}
			if test.shared {
				if len(comparison.Metrics) != 1 || !comparison.Metrics[0].ScoreDelta.Present || comparison.Metrics[0].ScoreDelta.Mean != 0 {
					t.Fatalf("equivalent metric identity split: %#v", comparison.Metrics)
				}
			} else if len(comparison.Metrics) != 2 || comparison.Metrics[0].Candidate != nil || comparison.Metrics[1].Baseline != nil {
				t.Fatalf("different exact values merged: %#v", comparison.Metrics)
			}
		})
	}
}
