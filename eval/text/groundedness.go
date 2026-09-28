package text

import (
	"context"

	"github.com/Tangerg/scope/eval"
)

const MetricGroundedness eval.MetricName = "groundedness"

const groundednessPrompt = `Evaluate how well the output is supported by the provided evidence.

Score support from 0.0 to 1.0 and provide concise feedback.

Evidence:
{{.Evidence}}

Output:
{{.Output}}

Evaluation:`

type GroundednessEvaluator struct {
	evaluator eval.Evaluator[GroundednessSample]
}

type groundednessVariables struct {
	Output   string
	Evidence string
}

func NewGroundednessEvaluator(config ModelEvaluatorConfig) (*GroundednessEvaluator, error) {
	metric, err := eval.NewMetric(eval.MetricConfig{Namespace: "text", Name: MetricGroundedness})
	if err != nil {
		return nil, err
	}
	evaluator, err := newModelEvaluator(
		config,
		metric,
		groundednessPrompt,
		func(sample GroundednessSample) groundednessVariables {
			return groundednessVariables{Output: sample.Output, Evidence: sample.EvidenceText()}
		},
		templateOutputName,
		templateEvidenceName,
	)
	if err != nil {
		return nil, err
	}
	return &GroundednessEvaluator{evaluator: evaluator}, nil
}

func (g *GroundednessEvaluator) Evaluate(ctx context.Context, sample GroundednessSample) (eval.Report, error) {
	if err := sample.Validate(); err != nil {
		return eval.Report{}, err
	}
	return g.evaluator.Evaluate(ctx, sample)
}

var _ eval.Evaluator[GroundednessSample] = (*GroundednessEvaluator)(nil)
