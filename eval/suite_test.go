package eval_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"testing/synctest"

	"github.com/Tangerg/scope/eval"
)

func TestSuiteCollectPreservesIndependentSuccessAndFailure(t *testing.T) {
	failed := errors.New("judge unavailable")
	for _, concurrency := range []int{1, 2} {
		assessments := []eval.Assessment[string]{
			{ID: "quality", Evaluator: eval.EvaluatorFunc[string](func(context.Context, string) (eval.Report, error) {
				return scoredReport("quality", eval.VerdictPass, 1), nil
			})},
			{ID: "judge", Evaluator: eval.EvaluatorFunc[string](func(context.Context, string) (eval.Report, error) {
				return scoredReport("quality", eval.VerdictPass, 1), failed
			})},
			{ID: "independent", Evaluator: eval.EvaluatorFunc[string](func(ctx context.Context, _ string) (eval.Report, error) {
				if err := ctx.Err(); err != nil {
					return eval.Report{}, err
				}
				return scoredReport("quality", eval.VerdictFail, 0), nil
			})},
		}
		suite, err := eval.NewSuite(eval.SuiteConfig[string]{Assessments: assessments, MaxConcurrency: concurrency})
		if err != nil {
			t.Fatal(err)
		}
		assessments[0].ID = "mutated"
		result, err := suite.Run(t.Context(), "input")
		if err != nil || result.Validate() != nil || result.Complete() || result.Verdict() != eval.VerdictUnspecified || !errors.Is(result.Err(), failed) {
			t.Fatalf("collect result = %#v, %v", result, err)
		}
		for index, id := range []eval.AssessmentID{"quality", "judge", "independent"} {
			if result.Results[index].ID != id {
				t.Fatalf("assessment[%d] identity = %q", index, result.Results[index].ID)
			}
		}
		if result.Results[0].Status != eval.AssessmentCompleted || result.Results[0].Report == nil ||
			result.Results[1].Status != eval.AssessmentFailed || result.Results[1].Report != nil ||
			result.Results[2].Status != eval.AssessmentCompleted || result.Results[2].Report == nil {
			t.Fatalf("partial results were lost or an error report was consumed: %#v", result.Results)
		}
		report, err := eval.NewExperimentReport("fixed-input", []eval.CaseResult{{ID: "case", Result: result}})
		if err != nil {
			t.Fatal(err)
		}
		summary := report.Summary()
		if summary.Total != 1 || summary.Evaluated != 0 || summary.Errors != 1 || summary.Partial != 1 || len(summary.Metrics) != 2 {
			t.Fatalf("partial summary = %#v", summary)
		}
		if summary.Metrics[0].AssessmentID != "quality" || summary.Metrics[0].Scores.Mean != 1 || summary.Metrics[1].AssessmentID != "independent" || summary.Metrics[1].Scores.Mean != 0 {
			t.Fatalf("same metric from distinct assessments was merged: %#v", summary.Metrics)
		}
		want := []eval.AssessmentSummary{{ID: "quality", Completed: 1}, {ID: "judge", Failed: 1}, {ID: "independent", Completed: 1}}
		if !reflect.DeepEqual(summary.Assessments, want) {
			t.Fatalf("assessment coverage = %#v, want %#v", summary.Assessments, want)
		}
	}
}

func TestSuiteCollectDoesNotCancelAnInFlightIndependentAssessment(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		failed := errors.New("judge unavailable")
		started := make(chan struct{})
		release := make(chan struct{})
		suite, err := eval.NewSuite(eval.SuiteConfig[string]{MaxConcurrency: 2, Assessments: []eval.Assessment[string]{
			{ID: "failure", Evaluator: eval.EvaluatorFunc[string](func(context.Context, string) (eval.Report, error) {
				<-started
				return eval.Report{}, failed
			})},
			{ID: "independent", Evaluator: eval.EvaluatorFunc[string](func(ctx context.Context, _ string) (eval.Report, error) {
				close(started)
				<-release
				if ctx.Err() != nil {
					return eval.Report{}, ctx.Err()
				}
				return scoredReport("quality", eval.VerdictPass, 1), nil
			})},
		}})
		if err != nil {
			t.Fatal(err)
		}
		var result eval.SuiteResult
		var runErr error
		done := make(chan struct{})
		go func() {
			result, runErr = suite.Run(t.Context(), "input")
			close(done)
		}()
		synctest.Wait()
		close(release)
		<-done
		if runErr != nil || result.Results[1].Status != eval.AssessmentCompleted || result.Results[1].Report == nil {
			t.Fatalf("independent assessment canceled: %#v, %v", result, runErr)
		}
	})
}

func TestSuiteFailFastAndCancellationKeepAllPlannedIdentities(t *testing.T) {
	failed := errors.New("judge unavailable")
	for _, canceled := range []bool{false, true} {
		ctx, cancel := context.WithCancel(t.Context())
		suite, err := eval.NewSuite(eval.SuiteConfig[string]{ErrorPolicy: eval.ErrorFailFast, Assessments: []eval.Assessment[string]{
			{ID: "first", Evaluator: eval.EvaluatorFunc[string](func(context.Context, string) (eval.Report, error) {
				if canceled {
					cancel()
					return eval.Report{}, context.Canceled
				}
				return eval.Report{}, failed
			})},
			{ID: "later", Evaluator: eval.EvaluatorFunc[string](func(context.Context, string) (eval.Report, error) {
				t.Error("assessment started after fail-fast/cancellation")
				return scoredReport("quality", eval.VerdictPass, 1), nil
			})},
		}})
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		result, err := suite.Run(ctx, "input")
		cancel()
		wantStatus, wantError := eval.AssessmentFailed, failed
		if canceled {
			wantStatus, wantError = eval.AssessmentCanceled, context.Canceled
		}
		if !errors.Is(err, wantError) || result.Validate() != nil || len(result.Results) != 2 || result.Results[0].ID != "first" || result.Results[0].Status != wantStatus || result.Results[1].ID != "later" || result.Results[1].Status != eval.AssessmentNotEvaluated {
			t.Fatalf("fail-fast result = %#v, %v", result, err)
		}
	}
}

func TestSuiteValidatesIdentityAndRejectsInvalidEvaluatorReports(t *testing.T) {
	var typedNil *nilEvaluator
	for _, config := range []eval.SuiteConfig[int]{
		{},
		{Assessments: []eval.Assessment[int]{{ID: "", Evaluator: validIntEvaluator()}}},
		{Assessments: []eval.Assessment[int]{{ID: " spaced", Evaluator: validIntEvaluator()}}},
		{Assessments: []eval.Assessment[int]{{ID: "a", Evaluator: typedNil}}},
		{Assessments: []eval.Assessment[int]{{ID: "a", Evaluator: validIntEvaluator()}, {ID: "a", Evaluator: validIntEvaluator()}}},
		{Assessments: []eval.Assessment[int]{{ID: "a", Evaluator: validIntEvaluator()}}, MaxConcurrency: -1},
		{Assessments: []eval.Assessment[int]{{ID: "a", Evaluator: validIntEvaluator()}}, ErrorPolicy: "unknown"},
	} {
		if _, err := eval.NewSuite(config); !errors.Is(err, eval.ErrInvalidEvaluatorConfig) {
			t.Fatalf("invalid suite accepted: %#v, %v", config, err)
		}
	}
	suite := testSuite(t, eval.EvaluatorFunc[int](func(context.Context, int) (eval.Report, error) { return eval.Report{}, nil }))
	result, err := suite.Run(t.Context(), 1)
	if err != nil || result.Results[0].ID != "evaluation" || result.Results[0].Status != eval.AssessmentFailed || result.Results[0].Report != nil || !errors.Is(result.Err(), eval.ErrInvalidReport) {
		t.Fatalf("invalid evaluator report accepted: %#v, %v", result, err)
	}
}

func TestExperimentDetailsAreEvidenceWithoutStatisticalWeight(t *testing.T) {
	parent := scoredReport("quality", eval.VerdictPass, 0.8)
	parent.Details = []eval.Report{scoredReport("quality", eval.VerdictFail, 0), scoredReport("support", eval.VerdictPass, 1)}
	suite := testSuite(t, eval.EvaluatorFunc[string](func(context.Context, string) (eval.Report, error) { return parent, nil }))
	result, err := suite.Run(t.Context(), "input")
	if err != nil {
		t.Fatal(err)
	}
	report, err := eval.NewExperimentReport("fixed-input", []eval.CaseResult{{ID: "case", Result: result}})
	if err != nil {
		t.Fatal(err)
	}
	*result.Results[0].Report.Score = 0
	result.Results[0].Report.Details = nil
	summary := report.Summary()
	if len(summary.Metrics) != 1 || summary.Metrics[0].Scores.Count != 1 || summary.Metrics[0].Scores.Mean != 0.8 || summary.Passed != 1 {
		t.Fatalf("details changed sample weighting: %#v", summary)
	}
	if len(report.Cases()[0].Result.Results[0].Report.Details) != 2 {
		t.Fatal("experiment record lost owned supporting evidence")
	}
}
