package eval_test

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
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
	dataset, err := eval.NewDataset("test-fixture", eval.Case[int]{ID: "same", Subject: 1})
	if err != nil {
		t.Fatal(err)
	}
	experiment, err := eval.NewExperiment(eval.ExperimentConfig[int]{
		Dataset: dataset,
		Suite: testSuite(t, eval.EvaluatorFunc[int](func(context.Context, int) (eval.Report, error) {
			if fail {
				return eval.Report{}, errors.New("evaluation failed")
			}
			score := eval.Score(1)
			return eval.Report{Metric: metric, Score: &score, Decision: &eval.Decision{Policy: "test", Verdict: eval.VerdictPass}}, nil
		})),
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
			missingScore := eval.DistributionDelta{CandidateOnly: 1}
			missingDecision := eval.DecisionDelta{CandidateOnly: 1}
			if test.baselinePresent {
				missingScore = eval.DistributionDelta{BaselineOnly: 1}
				missingDecision = eval.DecisionDelta{BaselineOnly: 1}
			}
			if metric.ScoreDelta != missingScore || metric.DecisionDelta != missingDecision {
				t.Fatalf("missing observations = (%+v, %+v), want (%+v, %+v)", metric.ScoreDelta, metric.DecisionDelta, missingScore, missingDecision)
			}
		})
	}
}

func TestComparisonRetainsUnjudgedMetricsWithoutNumericObservations(t *testing.T) {
	metric, err := eval.NewMetric(eval.MetricConfig{Name: "review"})
	if err != nil {
		t.Fatal(err)
	}
	dataset, err := eval.NewDataset("test-fixture", eval.Case[int]{ID: "same", Subject: 1})
	if err != nil {
		t.Fatal(err)
	}
	experiment, err := eval.NewExperiment(eval.ExperimentConfig[int]{
		Dataset: dataset,
		Suite: testSuite(t, eval.EvaluatorFunc[int](func(context.Context, int) (eval.Report, error) {
			return eval.Report{Metric: metric, Feedback: "insufficient evidence for a verdict"}, nil
		})),
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err := experiment.Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	comparison, err := report.Compare(report)
	if err != nil {
		t.Fatal(err)
	}
	if len(comparison.Metrics) != 1 {
		t.Fatalf("metric comparisons = %v", comparison.Metrics)
	}
	compared := comparison.Metrics[0]
	if compared.Baseline == nil || compared.Candidate == nil ||
		compared.Baseline.Unjudged != 1 || compared.Candidate.Unjudged != 1 ||
		compared.ScoreDelta.Present || compared.MeasurementDelta.Present {
		t.Fatalf("unjudged metric = %#v", compared)
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

func TestComparisonRequiresTheSameFixture(t *testing.T) {
	metric, err := eval.NewMetric(eval.MetricConfig{Name: "fixture_quality"})
	if err != nil {
		t.Fatal(err)
	}
	run := func(fixture string, subject int) eval.ExperimentReport {
		t.Helper()
		dataset, datasetErr := eval.NewDataset(fixture, eval.Case[int]{ID: "same", Subject: subject})
		if datasetErr != nil {
			t.Fatal(datasetErr)
		}
		experiment, experimentErr := eval.NewExperiment(eval.ExperimentConfig[int]{Dataset: dataset, Suite: testSuite(t, eval.EvaluatorFunc[int](func(context.Context, int) (eval.Report, error) {
			return eval.Report{Metric: metric, Decision: &eval.Decision{Policy: "test", Verdict: eval.VerdictPass}}, nil
		}))})
		if experimentErr != nil {
			t.Fatal(experimentErr)
		}
		report, runErr := experiment.Run(t.Context())
		if runErr != nil {
			t.Fatal(runErr)
		}
		return report
	}
	baseline := run("original-fixture", 1)
	changed := run("changed-fixture", 2)
	if _, compareErr := baseline.Compare(changed); !errors.Is(compareErr, eval.ErrInvalidComparison) {
		t.Fatalf("changed fixture accepted: %v", compareErr)
	}
	if _, compareErr := baseline.Compare(run("original-fixture", 1)); compareErr != nil {
		t.Fatal(compareErr)
	}
}

func TestComparisonPairsCasesWhenSuccessfulCoverageChanges(t *testing.T) {
	evaluationError := errors.New("evaluation unavailable")
	metric := testMetric("quality")
	run := func(t *testing.T, failedCase eval.CaseID, reverse bool) eval.ExperimentReport {
		t.Helper()
		cases := []eval.Case[eval.CaseID]{{ID: "A", Subject: "A"}, {ID: "B", Subject: "B"}}
		if reverse {
			cases[0], cases[1] = cases[1], cases[0]
		}
		dataset, datasetErr := eval.NewDataset("fixed-pairing-fixture", cases...)
		if datasetErr != nil {
			t.Fatal(datasetErr)
		}
		suite := testSuite(t, eval.EvaluatorFunc[eval.CaseID](func(_ context.Context, id eval.CaseID) (eval.Report, error) {
			if id == failedCase {
				return eval.Report{}, evaluationError
			}
			score := eval.Score(0)
			if id == "B" {
				score = 1
			}
			measurement := score.Float64() * 10
			decision, err := score.Decide(0.5)
			if err != nil {
				return eval.Report{}, err
			}
			return eval.Report{Metric: metric, Score: &score, Measurement: &measurement, Decision: &decision}, nil
		}))
		experiment, err := eval.NewExperiment(eval.ExperimentConfig[eval.CaseID]{Dataset: dataset, Suite: suite})
		if err != nil {
			t.Fatal(err)
		}
		report, err := experiment.Run(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		return report
	}
	for _, test := range []struct {
		name                              string
		baselineFailure, candidateFailure eval.CaseID
		baselineMean, candidateMean       float64
		baselineCount, candidateCount     int
		errorDelta                        int
		delta                             eval.DistributionDelta
		decisions                         eval.DecisionDelta
	}{
		{
			name: "coverage loss", candidateFailure: "A", baselineMean: 0.5, candidateMean: 1,
			baselineCount: 2, candidateCount: 1, errorDelta: 1,
			delta:     eval.DistributionDelta{Present: true, Matched: 1, BaselineOnly: 1},
			decisions: eval.DecisionDelta{Matched: 1, BaselineOnly: 1},
		},
		{
			name: "coverage recovery", baselineFailure: "A", baselineMean: 1, candidateMean: 0.5,
			baselineCount: 1, candidateCount: 2, errorDelta: -1,
			delta:     eval.DistributionDelta{Present: true, Matched: 1, CandidateOnly: 1},
			decisions: eval.DecisionDelta{Matched: 1, CandidateOnly: 1},
		},
		{
			name: "disjoint successes", baselineFailure: "B", candidateFailure: "A", candidateMean: 1,
			baselineCount: 1, candidateCount: 1,
			delta:     eval.DistributionDelta{BaselineOnly: 1, CandidateOnly: 1},
			decisions: eval.DecisionDelta{BaselineOnly: 1, CandidateOnly: 1},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			baseline := run(t, test.baselineFailure, false)
			candidate := run(t, test.candidateFailure, true)
			comparison, err := baseline.Compare(candidate)
			if err != nil {
				t.Fatal(err)
			}
			if comparison.Baseline.Total != 2 || comparison.Candidate.Total != 2 || comparison.ErrorDelta != test.errorDelta ||
				comparison.EvaluatedDelta != -test.errorDelta || len(comparison.Metrics) != 1 {
				t.Fatalf("execution coverage = %+v", comparison)
			}
			compared := comparison.Metrics[0]
			if compared.AssessmentID != "evaluation" || compared.Baseline == nil || compared.Candidate == nil ||
				compared.Baseline.Scores.Count != test.baselineCount || compared.Candidate.Scores.Count != test.candidateCount ||
				compared.Baseline.Scores.Mean != test.baselineMean || compared.Candidate.Scores.Mean != test.candidateMean ||
				compared.Baseline.Measurements.Mean != test.baselineMean*10 || compared.Candidate.Measurements.Mean != test.candidateMean*10 {
				t.Fatalf("independent distributions = %+v", compared)
			}
			if compared.ScoreDelta != test.delta || compared.MeasurementDelta != test.delta || compared.DecisionDelta != test.decisions {
				t.Fatalf("paired observations = (%+v, %+v, %+v), want (%+v, %+v, %+v)",
					compared.ScoreDelta, compared.MeasurementDelta, compared.DecisionDelta, test.delta, test.delta, test.decisions)
			}
		})
	}
}

func TestComparisonSeparatesDecisionPolicyFromNumericMeasurement(t *testing.T) {
	dataset, datasetErr := eval.NewDataset("fixed-threshold-fixture", eval.Case[string]{ID: "same", Subject: "answer"})
	if datasetErr != nil {
		t.Fatal(datasetErr)
	}
	metric := testMetric("quality")
	run := func(threshold eval.Score) eval.ExperimentReport {
		t.Helper()
		suite := testSuite(t, eval.EvaluatorFunc[string](func(context.Context, string) (eval.Report, error) {
			score := eval.Score(0.5)
			decision, err := score.Decide(threshold)
			if err != nil {
				return eval.Report{}, err
			}
			return eval.Report{Metric: metric, Score: &score, Decision: &decision}, nil
		}))
		experiment, err := eval.NewExperiment(eval.ExperimentConfig[string]{Dataset: dataset, Suite: suite})
		if err != nil {
			t.Fatal(err)
		}
		report, err := experiment.Run(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		return report
	}
	comparison, err := run(0.4).Compare(run(0.6))
	if err != nil {
		t.Fatal(err)
	}
	if len(comparison.Metrics) != 1 || comparison.Baseline.Passed != 1 || comparison.Candidate.Failed != 1 {
		t.Fatalf("changed threshold erased original outcomes: %+v", comparison)
	}
	compared := comparison.Metrics[0]
	if compared.ScoreDelta != (eval.DistributionDelta{Present: true, Matched: 1}) ||
		compared.DecisionDelta != (eval.DecisionDelta{Incompatible: 1}) {
		t.Fatalf("threshold comparison = %+v", compared)
	}
}

func TestFixtureIdentityAdmission(t *testing.T) {
	for _, fixtureID := range []string{"", " padded", "\xff"} {
		if _, err := eval.NewDataset(fixtureID, eval.Case[int]{ID: "case", Subject: 1}); !errors.Is(err, eval.ErrInvalidDataset) {
			t.Errorf("NewDataset(%q) error = %v, want ErrInvalidDataset", fixtureID, err)
		}
		if _, err := eval.NewExperimentReport(fixtureID, nil); !errors.Is(err, eval.ErrInvalidExperiment) {
			t.Errorf("NewExperimentReport(%q) error = %v, want ErrInvalidExperiment", fixtureID, err)
		}
	}
}

func reportExperiment(t *testing.T, report eval.Report) eval.ExperimentReport {
	t.Helper()
	dataset, err := eval.NewDataset("test-fixture", eval.Case[int]{ID: "same", Subject: 1})
	if err != nil {
		t.Fatal(err)
	}
	experiment, err := eval.NewExperiment(eval.ExperimentConfig[int]{
		Dataset: dataset,
		Suite: testSuite(t, eval.EvaluatorFunc[int](func(context.Context, int) (eval.Report, error) {
			return report.Clone()
		})),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := experiment.Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return result
}

// A summary Decision's verdict follows from its Details, so their Metrics and
// rules decide whether two summaries are the same rule.
func TestDecisionIdentityIncludesDetailRules(t *testing.T) {
	maximum := func(name eval.MetricName, limit int, verdict eval.Verdict) eval.Report {
		t.Helper()
		metric, err := eval.NewMetric(eval.MetricConfig{Name: name})
		if err != nil {
			t.Fatal(err)
		}
		return eval.Report{Metric: metric, Decision: &eval.Decision{
			Policy: "maximum", Parameters: metadata.Map{"maximum": json.RawMessage(strconv.Itoa(limit))}, Verdict: verdict,
		}}
	}
	summary := func(detail eval.Report) eval.ExperimentReport {
		t.Helper()
		metric, err := eval.NewMetric(eval.MetricConfig{Name: "all"})
		if err != nil {
			t.Fatal(err)
		}
		return reportExperiment(t, eval.Report{
			Metric: metric, Decision: &eval.Decision{Policy: "all", Verdict: detail.Verdict()}, Details: []eval.Report{detail},
		})
	}
	baseline := summary(maximum("steps", 0, eval.VerdictFail))
	for _, test := range []struct {
		name      string
		candidate eval.ExperimentReport
		want      eval.DecisionDelta
	}{
		{"same rule", summary(maximum("steps", 0, eval.VerdictFail)), eval.DecisionDelta{Matched: 1}},
		{"changed threshold", summary(maximum("steps", 1, eval.VerdictPass)), eval.DecisionDelta{Incompatible: 1}},
		{"changed metric", summary(maximum("effects", 0, eval.VerdictPass)), eval.DecisionDelta{Incompatible: 1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			comparison, err := baseline.Compare(test.candidate)
			if err != nil || len(comparison.Metrics) != 1 || comparison.Metrics[0].DecisionDelta != test.want {
				t.Fatalf("comparison = %+v, %v; want %+v", comparison, err, test.want)
			}
		})
	}
}
