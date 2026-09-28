package text

import (
	"context"

	"github.com/Tangerg/scope/eval"
)

const MetricAnswerRelevance eval.MetricName = "answer_relevance"

const answerRelevancePrompt = `Evaluate how directly and completely the output addresses the input.

Score relevance from 0.0 to 1.0 and provide concise feedback.

Input:
{{.Input}}

Output:
{{.Output}}

Evaluation:`

// AnswerRelevanceEvaluator scores whether generated output addresses its
// originating input. Groundedness is intentionally evaluated separately.
type AnswerRelevanceEvaluator struct {
	evaluator eval.Evaluator[AnswerRelevanceSample]
}

type answerRelevanceVariables struct {
	Input  string
	Output string
}

func NewAnswerRelevanceEvaluator(config ModelEvaluatorConfig) (*AnswerRelevanceEvaluator, error) {
	metric, err := eval.NewMetric(eval.MetricConfig{Namespace: "text", Name: MetricAnswerRelevance})
	if err != nil {
		return nil, err
	}
	evaluator, err := newModelEvaluator(
		config,
		metric,
		answerRelevancePrompt,
		func(sample AnswerRelevanceSample) answerRelevanceVariables {
			return answerRelevanceVariables(sample)
		},
		templateInputName,
		templateOutputName,
	)
	if err != nil {
		return nil, err
	}
	return &AnswerRelevanceEvaluator{evaluator: evaluator}, nil
}

func (a *AnswerRelevanceEvaluator) Evaluate(ctx context.Context, sample AnswerRelevanceSample) (eval.Report, error) {
	if err := sample.Validate(); err != nil {
		return eval.Report{}, err
	}
	return a.evaluator.Evaluate(ctx, sample)
}

var _ eval.Evaluator[AnswerRelevanceSample] = (*AnswerRelevanceEvaluator)(nil)
