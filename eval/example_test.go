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
		verdict := eval.VerdictFail
		if subject != "" {
			verdict = eval.VerdictPass
		}
		return eval.Report{Metric: metric, Decision: &eval.Decision{Policy: "non_empty", Verdict: verdict}}, nil
	})
	suite, err := eval.NewSuite(eval.SuiteConfig[string]{Assessments: []eval.Assessment[string]{{ID: "non_empty", Evaluator: evaluator}}})
	if err != nil {
		panic(err)
	}
	dataset, err := eval.NewDataset("test-fixture",
		eval.Case[string]{ID: "first", Subject: "answer"},
	)
	if err != nil {
		panic(err)
	}
	experiment, err := eval.NewExperiment(eval.ExperimentConfig[string]{
		Dataset: dataset, Suite: suite,
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

func ExampleScore_Decide() {
	score, err := eval.NewScore(0.82)
	if err != nil {
		panic(err)
	}
	threshold, err := eval.NewScore(0.8)
	if err != nil {
		panic(err)
	}
	decision, err := score.Decide(threshold)
	if err != nil {
		panic(err)
	}

	fmt.Println(decision.Verdict, score.Float64())
	// Output:
	// pass 0.82
}

func ExampleSuite_Run() {
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
		return eval.Report{Metric: qualityMetric, Score: &score}, scoreErr
	})
	safety := eval.EvaluatorFunc[string](func(context.Context, string) (eval.Report, error) {
		return eval.Report{Metric: safetyMetric, Decision: &eval.Decision{Policy: "safety", Verdict: eval.VerdictFail}, Feedback: "Answer requires review."}, nil
	})
	scored, err := eval.NewCompositeEvaluator(eval.CompositeConfig[string]{
		Components: []eval.Component[string]{{Evaluator: quality}},
	})
	if err != nil {
		panic(err)
	}
	// A gate determines acceptance without contributing a score. The Suite
	// preserves the quality Composite's score under its own metric identity.
	suite, err := eval.NewSuite(eval.SuiteConfig[string]{
		Assessments: []eval.Assessment[string]{{ID: "quality", Evaluator: scored}, {ID: "safety", Evaluator: safety}},
	})
	if err != nil {
		panic(err)
	}
	report, err := suite.Run(context.Background(), "answer")
	if err != nil {
		panic(err)
	}
	fmt.Println("acceptance:", report.Verdict())
	fmt.Println("quality:", report.Results[0].Report.Score.Float64())
	fmt.Println("gate has score:", report.Results[1].Report.Score != nil)
	// Output:
	// acceptance: fail
	// quality: 0.9
	// gate has score: false
}
