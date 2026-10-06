package eval_test

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"

	"github.com/Tangerg/scope/eval"
)

func TestMetricRejectsUnencodableIdentityAtConstruction(t *testing.T) {
	for _, config := range []eval.MetricConfig{
		{Namespace: "\xff", Name: "metric"},
		{Name: "\xff"},
		{Name: "metric", Unit: "\xff"},
	} {
		metric, err := eval.NewMetric(config)
		if !errors.Is(err, eval.ErrInvalidMetric) || metric.Name() != "" {
			t.Fatalf("NewMetric(%+v) = %v, %v", config, metric, err)
		}
	}
}

func TestEvaluationRejectsUnencodableOwnedText(t *testing.T) {
	metric, err := eval.NewMetric(eval.MetricConfig{Name: "metric"})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		validate func() error
		want     error
	}{
		{"feedback", eval.Report{Metric: metric, Feedback: "\xff"}.Validate, eval.ErrInvalidReport},
		{"detail feedback", eval.Report{Metric: metric, Details: []eval.Report{{Metric: metric, Feedback: "\xff"}}}.Validate, eval.ErrInvalidReport},
		{"policy", eval.Decision{Policy: "\xff", Verdict: eval.VerdictPass}.Validate, eval.ErrInvalidReport},
		{"reason", eval.Execution[int]{Status: eval.ExecutionFailed, Reason: "\xff"}.Validate, eval.ErrInvalidExecution},
		{"case identity", eval.CaseID("\xff").Validate, eval.ErrInvalidCase},
		{"assessment identity", eval.AssessmentID("\xff").Validate, eval.ErrInvalidAssessment},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if validationErr := test.validate(); !errors.Is(validationErr, test.want) {
				t.Fatalf("Validate() = %v, want %v", validationErr, test.want)
			}
		})
	}
}

func TestSuiteRejectsUnencodableReportBeforeCompletion(t *testing.T) {
	metric, err := eval.NewMetric(eval.MetricConfig{Name: "metric"})
	if err != nil {
		t.Fatal(err)
	}
	suite, err := eval.NewSuite(eval.SuiteConfig[int]{Assessments: []eval.Assessment[int]{
		{ID: "assessment", Evaluator: eval.EvaluatorFunc[int](func(context.Context, int) (eval.Report, error) {
			return eval.Report{Metric: metric, Feedback: "\xff"}, nil
		})},
	}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := suite.Run(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if result.Complete() || result.Results[0].Report != nil || result.Results[0].Status() != eval.AssessmentFailed || !errors.Is(result.Err(), eval.ErrInvalidReport) {
		t.Fatalf("invalid report was published as completed: %+v", result)
	}
}

func TestEvaluationPreservesValidUnicodeAndNUL(t *testing.T) {
	text := "评估\x00é"
	metric, err := eval.NewMetric(eval.MetricConfig{Namespace: text, Name: eval.MetricName(text), Unit: text})
	if err != nil {
		t.Fatal(err)
	}
	report := eval.Report{Metric: metric, Feedback: text, Decision: &eval.Decision{Policy: text, Verdict: eval.VerdictPass}}
	encoded, err := jsonv2.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var decoded eval.Report
	if decodeErr := jsonv2.Unmarshal(encoded, &decoded); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if decoded.Feedback != text || decoded.Decision.Policy != text || decoded.Metric.Unit() != text || decoded.Metric.Name() != eval.MetricName(text) || decoded.Metric.Namespace() != text {
		t.Fatal("evaluation text changed during round trip")
	}
	execution := eval.Execution[int]{Status: eval.ExecutionFailed, Reason: text}
	encoded, err = jsonv2.Marshal(execution)
	if err != nil {
		t.Fatal(err)
	}
	var receipt eval.Execution[int]
	if decodeErr := jsonv2.Unmarshal(encoded, &receipt); decodeErr != nil || receipt.Reason != text {
		t.Fatalf("execution round trip = %+v, %v", receipt, decodeErr)
	}
	if validationErr := eval.CaseID(text).Validate(); validationErr != nil {
		t.Fatal(validationErr)
	}
	if validationErr := eval.AssessmentID(text).Validate(); validationErr != nil {
		t.Fatal(validationErr)
	}
}
