package judge_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/eval"
	"github.com/Tangerg/scope/eval/judge"
)

type toolDecision struct {
	Expected string
	Actual   string
}

func TestEvaluatorSupportsNonTextSubjectsAndMedianSampling(t *testing.T) {
	model := &fakeModel{replies: []string{
		"{\"score\":0.2,\"feedback\":\"low\"}",
		"{\"score\":0.9,\"feedback\":\"high\"}",
		"{\"score\":0.7,\"feedback\":\"middle\"}",
	}}
	metric, err := eval.NewMetric(eval.MetricConfig{Namespace: "agent", Name: "tool_selection"})
	if err != nil {
		t.Fatal(err)
	}
	threshold := eval.Score(0.5)
	evaluator, err := judge.NewEvaluator(judge.Config[toolDecision]{
		ModelID: "test-model-v1", RubricID: "test-rubric-v1", Model: model, Metric: metric, Threshold: &threshold, Samples: 3,
		Prompt: func(subject toolDecision) (chat.Message, error) {
			return chat.NewUserMessage(chat.NewTextPart(fmt.Sprintf(
				"expected=%s actual=%s", subject.Expected, subject.Actual,
			))), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err := evaluator.Evaluate(t.Context(), toolDecision{Expected: "search", Actual: "search"})
	if err != nil {
		t.Fatal(err)
	}
	if report.Verdict != eval.VerdictPass || report.Score == nil || *report.Score != 0.7 || report.Feedback != "middle" {
		t.Fatalf("report = %#v", report)
	}
	configuration, found, err := report.Metric.Parameters().Decode[struct {
		Aggregation string     `json:"aggregation"`
		Samples     int        `json:"samples"`
		Threshold   eval.Score `json:"threshold"`
	}]("judge")
	if err != nil || !found || configuration.Aggregation != "median" || configuration.Samples != 3 || configuration.Threshold != threshold {
		t.Fatalf("judge metric configuration = (%#v, %v, %v)", configuration, found, err)
	}
	scores, found, err := report.Metadata.Decode[[]eval.Score]("sample_scores")
	if err != nil || !found || len(scores) != 3 || scores[0] != 0.2 || scores[2] != 0.9 {
		t.Fatalf("sample scores = (%v, %v, %v)", scores, found, err)
	}
}

func TestEvaluatorDoesNotInventVerdictWithoutThreshold(t *testing.T) {
	model := &fakeModel{replies: []string{"{\"score\":0.9}"}}
	metric, err := eval.NewMetric(eval.MetricConfig{Name: "quality"})
	if err != nil {
		t.Fatal(err)
	}
	evaluator, err := judge.NewEvaluator(judge.Config[string]{
		ModelID: "test-model-v1", RubricID: "test-rubric-v1", Model: model, Metric: metric, Prompt: validPrompt,
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err := evaluator.Evaluate(t.Context(), "subject")
	if err != nil {
		t.Fatal(err)
	}
	if report.Verdict != eval.VerdictUnspecified || report.Score == nil || *report.Score != 0.9 {
		t.Fatalf("report = %#v", report)
	}
}

func TestEvaluatorValidatesGenericJudgeConfiguration(t *testing.T) {
	metric, err := eval.NewMetric(eval.MetricConfig{Name: "quality"})
	if err != nil {
		t.Fatal(err)
	}
	model := &fakeModel{replies: []string{"{\"score\":0.5}"}}
	for _, config := range []judge.Config[string]{
		{Metric: metric, Prompt: func(string) (chat.Message, error) { return chat.Message{}, nil }},
		{ModelID: "test-model-v1", RubricID: "test-rubric-v1", Model: model, Metric: metric},
		{ModelID: "test-model-v1", RubricID: "test-rubric-v1", Model: model, Metric: metric, Samples: -1, Prompt: validPrompt},
		{ModelID: "test-model-v1", RubricID: "test-rubric-v1", Model: model, Prompt: validPrompt},
		{Model: model, Metric: metric, Prompt: validPrompt, RubricID: "rubric-v1"},
		{Model: model, Metric: metric, Prompt: validPrompt, ModelID: "model-v1"},
		{Model: model, Metric: metric, Prompt: validPrompt, ModelID: " model-v1", RubricID: "rubric-v1"},
		{Model: model, Metric: metric, Prompt: validPrompt, ModelID: "model-v1", RubricID: "rubric-v1 "},
	} {
		if _, err := judge.NewEvaluator(config); !errors.Is(err, eval.ErrInvalidEvaluatorConfig) {
			t.Fatalf("NewEvaluator(%#v) error = %v", config, err)
		}
	}
}

func TestJudgeMetricIdentityIncludesModelRubricAndGenerationOptions(t *testing.T) {
	metric, err := eval.NewMetric(eval.MetricConfig{Name: "quality"})
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool)
	for _, config := range []judge.Config[string]{
		{ModelID: "model-v1", RubricID: "rubric-v1"},
		{ModelID: "model-v2", RubricID: "rubric-v1"},
		{ModelID: "model-v1", RubricID: "rubric-v2"},
		{ModelID: "model-v1", RubricID: "rubric-v1", Options: chat.Options{Temperature: new(0.1)}},
	} {
		config.Model = &fakeModel{replies: []string{`{"score":0.5}`}}
		config.Metric = metric
		config.Prompt = validPrompt
		evaluator, err := judge.NewEvaluator(config)
		if err != nil {
			t.Fatal(err)
		}
		report, err := evaluator.Evaluate(t.Context(), "same subject")
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(report.Metric)
		if err != nil {
			t.Fatal(err)
		}
		if seen[string(encoded)] {
			t.Fatalf("changed scoring configuration retained metric identity: %s", encoded)
		}
		seen[string(encoded)] = true
	}
}

func validPrompt(string) (chat.Message, error) {
	return chat.NewUserMessage(chat.NewTextPart("evaluate")), nil
}

type fakeModel struct {
	mu      sync.Mutex
	replies []string
	calls   int
}

func (f *fakeModel) Call(_ context.Context, _ *chat.Request) (*chat.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls >= len(f.replies) {
		return nil, errors.New("unexpected model call")
	}
	reply := f.replies[f.calls]
	f.calls++
	message := chat.NewAssistantMessage(chat.NewTextPart(reply))
	return &chat.Response{Output: &chat.Output{Message: &message, FinishReason: chat.FinishReasonStop}}, nil
}
