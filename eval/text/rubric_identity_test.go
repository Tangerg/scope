package text_test

import (
	"testing"

	"github.com/Tangerg/scope/core/chatclient"
	"github.com/Tangerg/scope/eval"
	texteval "github.com/Tangerg/scope/eval/text"
)

func TestComparisonSeparatesDifferentRubrics(t *testing.T) {
	strict, err := chatclient.ParseTemplate("Strict rubric: only fully relevant answers score above zero. Input={{.Input}} Output={{.Output}}")
	if err != nil {
		t.Fatal(err)
	}
	lenient, err := chatclient.ParseTemplate("Lenient rubric: any topical overlap is sufficient for a high score. Input={{.Input}} Output={{.Output}}")
	if err != nil {
		t.Fatal(err)
	}
	left, err := texteval.NewAnswerRelevanceEvaluator(texteval.ModelEvaluatorConfig{ModelID: "test-model-v1", Model: &fakeModel{reply: `{"score":0.2}`}, PromptTemplate: strict})
	if err != nil {
		t.Fatal(err)
	}
	right, err := texteval.NewAnswerRelevanceEvaluator(texteval.ModelEvaluatorConfig{ModelID: "test-model-v1", Model: &fakeModel{reply: `{"score":0.9}`}, PromptTemplate: lenient})
	if err != nil {
		t.Fatal(err)
	}
	dataset, err := eval.NewDataset(eval.Case[texteval.AnswerRelevanceSample]{ID: "same-case", Subject: texteval.AnswerRelevanceSample{Input: "question", Output: "unchanged answer"}})
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := eval.NewExperiment(eval.ExperimentConfig[texteval.AnswerRelevanceSample]{Dataset: dataset, Evaluator: left})
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := eval.NewExperiment(eval.ExperimentConfig[texteval.AnswerRelevanceSample]{Dataset: dataset, Evaluator: right})
	if err != nil {
		t.Fatal(err)
	}
	b, err := baseline.Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	c, err := candidate.Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	comparison, err := b.Compare(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(comparison.Metrics) != 2 || comparison.Metrics[0].Baseline == nil ||
		comparison.Metrics[0].Candidate != nil || comparison.Metrics[1].Baseline != nil ||
		comparison.Metrics[1].Candidate == nil {
		t.Fatalf("different rubrics were paired: %#v", comparison.Metrics)
	}
	for _, metric := range comparison.Metrics {
		if metric.ScoreDelta.Present || metric.MeasurementDelta.Present {
			t.Fatalf("different rubrics produced numeric deltas: %#v", metric)
		}
	}
}
