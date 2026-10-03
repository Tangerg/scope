package eval_test

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Tangerg/scope/eval"
)

func TestTrialPreservesCandidateAndIndependentAssessmentFailure(t *testing.T) {
	graderError := errors.New("grader unavailable")
	suite, err := eval.NewSuite(eval.SuiteConfig[eval.TrialSample[string, string]]{Assessments: []eval.Assessment[eval.TrialSample[string, string]]{
		{ID: "patch", Evaluator: eval.EvaluatorFunc[eval.TrialSample[string, string]](func(_ context.Context, sample eval.TrialSample[string, string]) (eval.Report, error) {
			if sample.Execution.Status != eval.ExecutionBudgetExhausted || sample.Execution.Output == nil || *sample.Execution.Output != "candidate" {
				t.Fatalf("assessment lost execution evidence: %+v", sample.Execution)
			}
			score := eval.Score(1)
			metric, err := eval.NewMetric(eval.MetricConfig{Name: "patch_present"})
			return eval.Report{Metric: metric, Score: &score}, err
		})},
		{ID: "tests", Evaluator: eval.EvaluatorFunc[eval.TrialSample[string, string]](func(context.Context, eval.TrialSample[string, string]) (eval.Report, error) {
			return eval.Report{}, graderError
		})},
	}})
	if err != nil {
		t.Fatal(err)
	}
	targetCalls := 0
	trial, err := eval.NewTrial(eval.TrialConfig[string, string]{Target: eval.TargetFunc[string, string](func(context.Context, string) (eval.Execution[string], error) {
		targetCalls++
		candidate := "candidate"
		return eval.Execution[string]{Status: eval.ExecutionBudgetExhausted, Output: &candidate, Reason: "token limit"}, nil
	}), Suite: suite})
	if err != nil {
		t.Fatal(err)
	}
	caseValue := eval.Case[string]{ID: "task-1", Subject: "fix issue"}
	result, err := trial.Run(context.Background(), caseValue)
	if err != nil || result.Execution == nil || result.ExecutionError != nil {
		t.Fatalf("Run() = %+v, %v", result, err)
	}
	if got := result.Case.Result.Results; len(got) != 2 || got[0].Status() != eval.AssessmentCompleted || got[1].Status() != eval.AssessmentFailed || !errors.Is(got[1].Err, graderError) {
		t.Fatalf("independent assessments = %+v", got)
	}
	if result.Case.Result.Complete() || result.Case.Result.Verdict() != eval.VerdictUnspecified {
		t.Fatal("incomplete assessment collection invented a verdict")
	}
	if _, err := eval.NewExperimentReport("fixed-tasks", []eval.CaseResult{result.Case}); err != nil {
		t.Fatal(err)
	}
	if _, err := suite.Run(context.Background(), eval.TrialSample[string, string]{Case: caseValue, Execution: *result.Execution}); err != nil {
		t.Fatal(err)
	}
	if targetCalls != 1 {
		t.Fatalf("regrading invoked target %d times", targetCalls)
	}
}

func TestTrialDiscardsInvalidTargetReceiptAndKeepsPlannedAssessments(t *testing.T) {
	targetError := errors.New("could not collect candidate")
	for _, runErr := range []error{targetError, nil} {
		suite, err := eval.NewSuite(eval.SuiteConfig[eval.TrialSample[string, string]]{Assessments: []eval.Assessment[eval.TrialSample[string, string]]{{
			ID: "official-tests", Evaluator: eval.EvaluatorFunc[eval.TrialSample[string, string]](func(context.Context, eval.TrialSample[string, string]) (eval.Report, error) {
				t.Error("assessment ran without a valid receipt")
				return eval.Report{}, nil
			}),
		}}})
		if err != nil {
			t.Fatal(err)
		}
		trial, err := eval.NewTrial(eval.TrialConfig[string, string]{Target: eval.TargetFunc[string, string](func(context.Context, string) (eval.Execution[string], error) {
			return eval.Execution[string]{Status: eval.ExecutionCompleted, Reason: "contradictory stop reason"}, runErr
		}), Suite: suite})
		if err != nil {
			t.Fatal(err)
		}
		result, err := trial.Run(context.Background(), eval.Case[string]{ID: "task", Subject: "input"})
		if err == nil || result.Execution != nil || result.ExecutionError == nil {
			t.Fatalf("invalid receipt accepted: %+v, %v", result, err)
		}
		if got := result.Case.Result.Results; len(got) != 1 || got[0].ID != "official-tests" || got[0].Status() != eval.AssessmentNotEvaluated || got[0].Report != nil {
			t.Fatalf("planned assessment lost: %+v", got)
		}
	}
}

func TestTrialPreservesReceiptWhenContextStopsBeforeGrading(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	suite, err := eval.NewSuite(eval.SuiteConfig[eval.TrialSample[string, string]]{Assessments: []eval.Assessment[eval.TrialSample[string, string]]{{ID: "tests", Evaluator: eval.EvaluatorFunc[eval.TrialSample[string, string]](func(context.Context, eval.TrialSample[string, string]) (eval.Report, error) {
		t.Error("grading started after cancellation")
		return eval.Report{}, nil
	})}}})
	if err != nil {
		t.Fatal(err)
	}
	trial, err := eval.NewTrial(eval.TrialConfig[string, string]{Target: eval.TargetFunc[string, string](func(context.Context, string) (eval.Execution[string], error) {
		candidate := ""
		cancel()
		return eval.Execution[string]{Status: eval.ExecutionCanceled, Reason: "caller canceled", Output: &candidate}, nil
	}), Suite: suite})
	if err != nil {
		t.Fatal(err)
	}
	result, err := trial.Run(ctx, eval.Case[string]{ID: "task"})
	if !errors.Is(err, context.Canceled) || result.Execution == nil || result.Execution.Output == nil || *result.Execution.Output != "" {
		t.Fatalf("canceled receipt = %+v, %v", result, err)
	}
	if got := result.Case.Result.Results[0]; got.Status() != eval.AssessmentNotEvaluated || !errors.Is(got.Err, context.Canceled) {
		t.Fatalf("canceled assessment = %+v", got)
	}
}

func TestExecutionJSONPreservesEmptyCandidateAndRejectsInvalidFacts(t *testing.T) {
	empty := ""
	execution := eval.Execution[string]{Status: eval.ExecutionCompleted, Output: &empty}
	data, err := jsonv2.Marshal(execution)
	if err != nil || !strings.Contains(string(data), `"output":""`) {
		t.Fatalf("empty candidate encoding = %s, %v", data, err)
	}
	for _, input := range []string{
		`{"status":"completed","reason":"contradictory stop reason"}`,
		`{"status":"failed","output":"candidate"}`,
		`{"status":"completed","output":"","legacy":true}`,
	} {
		if err := jsonv2.Unmarshal([]byte(input), &execution); err == nil {
			t.Fatalf("accepted invalid execution %s", input)
		}
		if execution.Status != eval.ExecutionCompleted || execution.Output == nil || *execution.Output != "" {
			t.Fatal("failed decode mutated receiver")
		}
	}
}

func TestTrialRetainsCompletedExecutionWithoutCandidate(t *testing.T) {
	noCandidate := errors.New("candidate unavailable")
	assessed := false
	suite, err := eval.NewSuite(eval.SuiteConfig[eval.TrialSample[string, string]]{Assessments: []eval.Assessment[eval.TrialSample[string, string]]{{
		ID: "tests", Evaluator: eval.EvaluatorFunc[eval.TrialSample[string, string]](func(_ context.Context, sample eval.TrialSample[string, string]) (eval.Report, error) {
			assessed = true
			if sample.Execution.Status != eval.ExecutionCompleted || sample.Execution.Output != nil {
				t.Fatal("execution outcome was changed because no candidate exists")
			}
			return eval.Report{}, noCandidate
		}),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	trial, err := eval.NewTrial(eval.TrialConfig[string, string]{Suite: suite, Target: eval.TargetFunc[string, string](func(context.Context, string) (eval.Execution[string], error) {
		return eval.Execution[string]{Status: eval.ExecutionCompleted}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	result, err := trial.Run(context.Background(), eval.Case[string]{ID: "task"})
	if err != nil || !assessed || result.Execution == nil || result.ExecutionError != nil || !errors.Is(result.Case.Result.Err(), noCandidate) {
		t.Fatalf("missing candidate result = %+v, %v", result, err)
	}
}

func TestExecutionJSONDistinguishesAbsentAndNullCandidates(t *testing.T) {
	var null any
	for _, candidate := range []*any{nil, &null} {
		execution := eval.Execution[any]{Status: eval.ExecutionCompleted, Output: candidate}
		data, err := jsonv2.Marshal(execution)
		if err != nil {
			t.Fatal(err)
		}
		var decoded eval.Execution[any]
		if err := jsonv2.Unmarshal(data, &decoded); err != nil {
			t.Fatal(err)
		}
		if (decoded.Output == nil) != (candidate == nil) {
			t.Fatalf("candidate presence changed: %s", data)
		}
		if decoded.Output != nil && *decoded.Output != nil {
			t.Fatalf("null candidate changed: %+v", decoded.Output)
		}
	}
}

func ExampleTrial() {
	suite, err := eval.NewSuite(eval.SuiteConfig[eval.TrialSample[string, string]]{Assessments: []eval.Assessment[eval.TrialSample[string, string]]{{
		ID: "answer", Evaluator: eval.EvaluatorFunc[eval.TrialSample[string, string]](func(_ context.Context, sample eval.TrialSample[string, string]) (eval.Report, error) {
			if sample.Execution.Output == nil {
				return eval.Report{}, errors.New("answer unavailable")
			}
			score := eval.Score(0)
			if *sample.Execution.Output == "42" {
				score = 1
			}
			metric, err := eval.NewMetric(eval.MetricConfig{Name: "exact_answer"})
			if err != nil {
				return eval.Report{}, err
			}
			decision, err := score.Decide(1)
			return eval.Report{Metric: metric, Score: &score, Decision: &decision}, err
		}),
	}}})
	if err != nil {
		panic(err)
	}
	trial, err := eval.NewTrial(eval.TrialConfig[string, string]{Suite: suite, Target: eval.TargetFunc[string, string](func(context.Context, string) (eval.Execution[string], error) {
		answer := "42"
		return eval.Execution[string]{Status: eval.ExecutionCompleted, Output: &answer}, nil
	})})
	if err != nil {
		panic(err)
	}
	result, err := trial.Run(context.Background(), eval.Case[string]{ID: "question-1", Subject: "What is six times seven?"})
	if err != nil {
		panic(err)
	}
	fmt.Println(result.Execution.Status, result.Case.Result.Results[0].Report.Verdict())
	// Output: completed pass
}
