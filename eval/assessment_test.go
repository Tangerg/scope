package eval_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/eval"
)

func TestAssessmentOutcomeHasOneOwner(t *testing.T) {
	report := scoredReport("quality", eval.VerdictPass, 1)
	for _, test := range []struct {
		name   string
		result eval.AssessmentResult
		status eval.AssessmentStatus
	}{
		{"completed", eval.AssessmentResult{ID: "quality", Report: &report}, eval.AssessmentCompleted},
		{"failed", eval.AssessmentResult{ID: "quality", Err: errors.New("judge unavailable")}, eval.AssessmentFailed},
		{"canceled", eval.AssessmentResult{ID: "quality", Err: context.Canceled}, eval.AssessmentCanceled},
		{"deadline", eval.AssessmentResult{ID: "quality", Err: context.DeadlineExceeded}, eval.AssessmentCanceled},
		{"not started", eval.AssessmentResult{ID: "quality", Err: eval.ErrNotEvaluated}, eval.AssessmentNotEvaluated},
		{"not started before cancellation", eval.AssessmentResult{ID: "quality", Err: errors.Join(eval.ErrNotEvaluated, context.Canceled)}, eval.AssessmentNotEvaluated},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.result.Validate(); err != nil {
				t.Fatal(err)
			}
			if got := test.result.Status(); got != test.status {
				t.Fatalf("status = %q, want %q", got, test.status)
			}
		})
	}
	for _, result := range []eval.AssessmentResult{
		{ID: "quality"},
		{ID: "quality", Report: &report, Err: context.Canceled},
	} {
		if result.Status() != "" {
			t.Fatalf("incomplete or competing assessment facts became a status: %+v", result)
		}
		if !errors.Is(result.Validate(), eval.ErrInvalidAssessment) {
			t.Fatalf("accepted incomplete or competing assessment facts: %+v", result)
		}
	}
	result := eval.SuiteResult{Results: []eval.AssessmentResult{{ID: "quality"}}}
	if result.Complete() || result.Verdict() != eval.VerdictUnspecified {
		t.Fatal("a missing report manufactured completion or a verdict")
	}
}

func TestCanceledSuitePreservesUnstartedCause(t *testing.T) {
	suite := testSuite(t, eval.EvaluatorFunc[int](func(context.Context, int) (eval.Report, error) {
		t.Fatal("assessment started after cancellation")
		return eval.Report{}, nil
	}))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err := suite.Run(ctx, 1)
	if !errors.Is(err, context.Canceled) || result.Validate() != nil {
		t.Fatalf("canceled run = %+v, %v", result, err)
	}
	assessment := result.Results[0]
	if assessment.Status() != eval.AssessmentNotEvaluated || !errors.Is(assessment.Err, eval.ErrNotEvaluated) || !errors.Is(assessment.Err, context.Canceled) {
		t.Fatalf("unstarted assessment lost its cause: %+v", assessment)
	}
}
