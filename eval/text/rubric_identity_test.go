package text_test

import (
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/chatclient"
	"github.com/Tangerg/scope/eval"
	texteval "github.com/Tangerg/scope/eval/text"
)

func TestComparisonRequiresSameRubric(t *testing.T) {
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
	if _, err := b.Compare(c); !errors.Is(err, eval.ErrInvalidComparison) {
		t.Fatalf("comparison between scoring rubrics error = %v, want ErrInvalidComparison", err)
	}
}
