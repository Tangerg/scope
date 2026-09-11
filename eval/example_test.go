package eval_test

import (
	"context"
	"fmt"

	"github.com/Tangerg/scope/eval"
)

func ExampleExperiment_Run() {
	metric, err := eval.NewMetric(eval.MetricConfig{
		Namespace: "example",
		Name:      "non_empty",
	})
	if err != nil {
		panic(err)
	}
	evaluator := eval.EvaluatorFunc[string](func(_ context.Context, subject string) (eval.Report, error) {
		return eval.Report{Metric: metric, Verdict: eval.VerdictPass}, nil
	})
	dataset, err := eval.NewDataset(
		eval.Case[string]{ID: "first", Subject: "answer"},
	)
	if err != nil {
		panic(err)
	}
	experiment, err := eval.NewExperiment(eval.ExperimentConfig[string]{
		Dataset: dataset, Evaluator: evaluator,
	})
	if err != nil {
		panic(err)
	}
	report, err := experiment.Run(context.Background())
	if err != nil {
		panic(err)
	}
	summary := report.Summary()

	fmt.Println(summary.Total, summary.Passed)
	// Output:
	// 1 1
}

func ExampleScore_Verdict() {
	score, err := eval.NewScore(0.82)
	if err != nil {
		panic(err)
	}
	threshold, err := eval.NewScore(0.8)
	if err != nil {
		panic(err)
	}
	verdict, err := score.Verdict(threshold)
	if err != nil {
		panic(err)
	}

	fmt.Println(verdict, score.Float64())
	// Output:
	// pass 0.82
}

func ExampleSuiteEvaluator_Evaluate() {
	qualityMetric, err := eval.NewMetric(eval.MetricConfig{Namespace: "example", Name: "quality"})
	if err != nil {
		panic(err)
	}
	safetyMetric, err := eval.NewMetric(eval.MetricConfig{Namespace: "example", Name: "safety"})
	if err != nil {
		panic(err)
	}
	quality := eval.EvaluatorFunc[string](func(context.Context, string) (eval.Report, error) {
		score, scoreErr := eval.NewScore(0.9)
		return eval.Report{Metric: qualityMetric, Score: &score, Verdict: eval.VerdictPass}, scoreErr
	})
	safety := eval.EvaluatorFunc[string](func(context.Context, string) (eval.Report, error) {
		return eval.Report{Metric: safetyMetric, Verdict: eval.VerdictFail, Feedback: "Answer requires review."}, nil
	})
	scored, err := eval.NewCompositeEvaluator(eval.CompositeConfig[string]{
		Components: []eval.Component[string]{{Evaluator: quality}},
	})
	if err != nil {
		panic(err)
	}
	// A gate determines acceptance without contributing a score. The Suite
	// preserves the quality Composite's score under its own metric identity.
	suite, err := eval.NewSuiteEvaluator(eval.SuiteConfig[string]{
		Evaluators: []eval.Evaluator[string]{scored, safety},
	})
	if err != nil {
		panic(err)
	}
	report, err := suite.Evaluate(context.Background(), "answer")
	if err != nil {
		panic(err)
	}
	fmt.Println("acceptance:", report.Verdict)
	fmt.Println("quality:", report.Details[0].Score.Float64())
	fmt.Println("gate has score:", report.Details[1].Score != nil)
	// Output:
	// acceptance: fail
	// quality: 0.9
	// gate has score: false
}
