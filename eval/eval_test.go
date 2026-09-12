package eval_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"testing"

	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/eval"
)

func testMetric(name string) eval.Metric {
	metric, err := eval.NewMetric(eval.MetricConfig{Name: eval.MetricName(name)})
	if err != nil {
		panic(err)
	}
	return metric
}

func scoredReport(name string, verdict eval.Verdict, score eval.Score) eval.Report {
	return eval.Report{Metric: testMetric(name), Verdict: verdict, Score: &score}
}

func TestReportBoundsRecursiveDetails(t *testing.T) {
	withinLimit := nestedReport(eval.MaxReportDepth)
	if err := withinLimit.Validate(); err != nil {
		t.Fatalf("Validate() within limit error = %v", err)
	}
	clone, err := withinLimit.Clone()
	if err != nil {
		t.Fatalf("Clone() within limit error = %v", err)
	}
	clone.Details[0].Feedback = "changed"
	if withinLimit.Details[0].Feedback == "changed" {
		t.Fatal("Clone() aliases detail tree")
	}

	overLimit := nestedReport(eval.MaxReportDepth + 1)
	if err := overLimit.Validate(); !errors.Is(err, eval.ErrInvalidReport) {
		t.Fatalf("Validate() over limit error = %v, want ErrInvalidReport", err)
	}
	if _, err := overLimit.Clone(); !errors.Is(err, eval.ErrInvalidReport) {
		t.Fatalf("Clone() over limit error = %v, want ErrInvalidReport", err)
	}
	if _, err := json.Marshal(overLimit); !errors.Is(err, eval.ErrInvalidReport) {
		t.Fatalf("Marshal() over limit error = %v, want ErrInvalidReport", err)
	}
}

func nestedReport(depth int) eval.Report {
	report := eval.Report{Metric: testMetric("nested"), Verdict: eval.VerdictPass}
	for range depth - 1 {
		report = eval.Report{Metric: testMetric("nested"), Details: []eval.Report{report}}
	}
	return report
}

func TestCompositeUsesExplicitWeightsAndPassPolicy(t *testing.T) {
	firstMetadata := metadata.Map{}
	if err := firstMetadata.Set("source", "first"); err != nil {
		t.Fatal(err)
	}
	components := []eval.Component[string]{
		{Evaluator: eval.EvaluatorFunc[string](func(context.Context, string) (eval.Report, error) {
			report := scoredReport("quality", eval.VerdictPass, 1)
			report.Feedback, report.Metadata = "good", firstMetadata.Clone()
			return report, nil
		}), Weight: 3, Required: true},
		{Evaluator: eval.EvaluatorFunc[string](func(context.Context, string) (eval.Report, error) {
			report := scoredReport("style", eval.VerdictFail, 0.5)
			report.Feedback = "weak"
			return report, nil
		}), Weight: 1},
	}
	composite, err := eval.NewCompositeEvaluator(eval.CompositeConfig[string]{
		Components: components, PassPolicy: eval.PassAtLeast, MinimumPassed: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	components[0].Evaluator = nil
	result, err := composite.Evaluate(t.Context(), "subject")
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != eval.VerdictPass || result.Score == nil || *result.Score != 0.875 || result.Feedback != "good\n\nweak" {
		t.Fatalf("result = %#v", result)
	}
	if result.Metric.Name() != eval.MetricNameComposite || len(result.Details) != 2 {
		t.Fatalf("composite identity/details = %#v", result)
	}
	type componentIdentity struct {
		Metric   eval.Metric `json:"metric"`
		Weight   float64     `json:"weight"`
		Required bool        `json:"required"`
	}
	type compositeIdentity struct {
		Components    []componentIdentity `json:"components"`
		PassPolicy    eval.PassPolicy     `json:"pass_policy"`
		MinimumPassed int                 `json:"minimum_passed"`
	}
	identity, found, err := result.Metric.Parameters().Decode[compositeIdentity]("configuration")
	if err != nil || !found || len(identity.Components) != 2 || identity.Components[0].Metric.Name() != "quality" ||
		identity.Components[0].Weight != 3 || !identity.Components[0].Required ||
		identity.PassPolicy != eval.PassAtLeast || identity.MinimumPassed != 1 {
		t.Fatalf("component identity = (%#v, %v, %v)", identity, found, err)
	}
	result.Details[0].Metadata["source"][1] = 'X'
	if string(firstMetadata["source"]) != "\"first\"" {
		t.Fatalf("child metadata was aliased: %s", firstMetadata["source"])
	}
}

func TestCompositeValidatesConfigurationAndPreservesErrors(t *testing.T) {
	if _, err := eval.NewCompositeEvaluator(eval.CompositeConfig[string]{}); !errors.Is(err, eval.ErrInvalidEvaluatorConfig) {
		t.Fatalf("empty composite error = %v", err)
	}
	for _, config := range []eval.CompositeConfig[string]{
		{Components: []eval.Component[string]{{}}},
		{Components: []eval.Component[string]{{Evaluator: validStringEvaluator(), Weight: -1}}},
		{Components: []eval.Component[string]{{Evaluator: validStringEvaluator(), Weight: math.NaN()}}},
		{Components: []eval.Component[string]{{Evaluator: validStringEvaluator(), Weight: math.Inf(1)}}},
		{Components: []eval.Component[string]{{Evaluator: validStringEvaluator()}}, PassPolicy: "unsupported"},
		{Components: []eval.Component[string]{{Evaluator: validStringEvaluator()}}, PassPolicy: eval.PassAtLeast},
		{Components: []eval.Component[string]{{Evaluator: validStringEvaluator()}}, PassPolicy: eval.PassAll, MinimumPassed: 1},
		{Components: []eval.Component[string]{{Evaluator: validStringEvaluator()}}, PassPolicy: eval.PassAny, MinimumPassed: 1},
		{Components: []eval.Component[string]{{Evaluator: validStringEvaluator()}}, MaxConcurrency: -1},
	} {
		if _, err := eval.NewCompositeEvaluator(config); !errors.Is(err, eval.ErrInvalidEvaluatorConfig) {
			t.Fatalf("NewCompositeEvaluator(%#v) error = %v", config, err)
		}
	}

	childErr := errors.New("child failed")
	composite, err := eval.NewCompositeEvaluator(eval.CompositeConfig[string]{
		Components: []eval.Component[string]{{Evaluator: eval.EvaluatorFunc[string](
			func(context.Context, string) (eval.Report, error) { return eval.Report{}, childErr },
		)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := composite.Evaluate(t.Context(), "subject"); !errors.Is(err, childErr) {
		t.Fatalf("child error = %v", err)
	}
}

func TestCompositeRunsIndependentComponentsConcurrently(t *testing.T) {
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	evaluator := func(name string) eval.Evaluator[string] {
		return eval.EvaluatorFunc[string](func(context.Context, string) (eval.Report, error) {
			started <- struct{}{}
			<-release
			return scoredReport(name, eval.VerdictPass, 1), nil
		})
	}
	composite, err := eval.NewCompositeEvaluator(eval.CompositeConfig[string]{
		Components:     []eval.Component[string]{{Evaluator: evaluator("one")}, {Evaluator: evaluator("two")}},
		MaxConcurrency: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, evaluateErr := composite.Evaluate(t.Context(), "subject")
		done <- evaluateErr
	}()
	<-started
	<-started
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestProjectionAdaptsAggregateCases(t *testing.T) {
	type aggregate struct{ Value int }
	projected, err := eval.NewProjectionEvaluator(
		eval.EvaluatorFunc[int](func(_ context.Context, value int) (eval.Report, error) {
			verdict := eval.VerdictFail
			if value > 0 {
				verdict = eval.VerdictPass
			}
			return scoredReport("positive", verdict, 1), nil
		}),
		func(value aggregate) (int, error) { return value.Value, nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	report, err := projected.Evaluate(t.Context(), aggregate{Value: 3})
	if err != nil || report.Verdict != eval.VerdictPass {
		t.Fatalf("projected report = %#v, %v", report, err)
	}
}

func TestExperimentCollectsCasesAndBuildsDistribution(t *testing.T) {
	evaluator := eval.EvaluatorFunc[float64](func(_ context.Context, value float64) (eval.Report, error) {
		if value < 0 {
			return eval.Report{}, errors.New("invalid subject")
		}
		score, err := eval.NewScore(value)
		if err != nil {
			return eval.Report{}, err
		}
		verdict, err := score.Verdict(0.5)
		if err != nil {
			return eval.Report{}, err
		}
		return eval.Report{Metric: testMetric("quality"), Verdict: verdict, Score: &score}, nil
	})
	cases := []eval.Case[float64]{
		{ID: "good", Subject: 1},
		{ID: "bad", Subject: 0},
		{ID: "error", Subject: -1},
	}
	dataset, err := eval.NewDataset("test-fixture", cases...)
	if err != nil {
		t.Fatal(err)
	}
	experiment, err := eval.NewExperiment(eval.ExperimentConfig[float64]{
		Dataset: dataset, Evaluator: evaluator, MaxConcurrency: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err := experiment.Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := eval.ExperimentSummary{
		Total: 3, Evaluated: 2, Passed: 1, Failed: 1, Errors: 1,
		Metrics: []eval.MetricSummary{{
			Metric: testMetric("quality"), Evaluated: 2, Passed: 1, Failed: 1,
			Scores: eval.Distribution{Count: 2, Mean: 0.5, Minimum: 0, P10: 0, P50: 0, P90: 1, Maximum: 1},
		}},
	}
	summary := report.Summary()
	if !reflect.DeepEqual(summary, want) {
		t.Fatalf("summary = %#v, want %#v", summary, want)
	}
	results := report.Cases()
	if results[0].ID != "good" || results[2].Err == nil {
		t.Fatalf("case order/results = %#v", results)
	}

	duplicate := append(cases, eval.Case[float64]{ID: "good"})
	if _, err := eval.NewDataset("test-fixture", duplicate...); !errors.Is(err, eval.ErrInvalidDataset) {
		t.Fatalf("duplicate dataset error = %v", err)
	}
}

func TestExperimentFailFastPreservesCaseIdentityAndStopsScheduling(t *testing.T) {
	firstErr := errors.New("first failed")
	calls := 0
	evaluator := eval.EvaluatorFunc[int](func(_ context.Context, value int) (eval.Report, error) {
		calls++
		if value == 1 {
			return eval.Report{}, firstErr
		}
		return scoredReport("quality", eval.VerdictPass, 1), nil
	})
	dataset, err := eval.NewDataset("test-fixture",
		eval.Case[int]{ID: "first", Subject: 1},
		eval.Case[int]{ID: "second", Subject: 2},
		eval.Case[int]{ID: "third", Subject: 3},
	)
	if err != nil {
		t.Fatal(err)
	}
	experiment, err := eval.NewExperiment(eval.ExperimentConfig[int]{
		Dataset: dataset, Evaluator: evaluator,
		MaxConcurrency: 1, ErrorPolicy: eval.ErrorFailFast,
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err := experiment.Run(t.Context())
	if !errors.Is(err, firstErr) {
		t.Fatalf("Run error = %v, want first failure", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
	results := report.Cases()
	for index, id := range []string{"first", "second", "third"} {
		if results[index].ID != eval.CaseID(id) || results[index].Err == nil {
			t.Fatalf("cases[%d] = %#v", index, results[index])
		}
	}
	if !errors.Is(results[1].Err, eval.ErrCaseNotEvaluated) || !errors.Is(results[2].Err, eval.ErrCaseNotEvaluated) {
		t.Fatalf("pending case errors = (%v, %v)", results[1].Err, results[2].Err)
	}
	summary := report.Summary()
	if summary.Total != 3 || summary.Errors != 3 || summary.Evaluated != 0 {
		t.Fatalf("summary = %#v", summary)
	}
}

func TestExperimentDefaultConcurrencyIsBounded(t *testing.T) {
	started := make(chan struct{}, eval.DefaultMaxConcurrency+1)
	release := make(chan struct{})
	evaluator := eval.EvaluatorFunc[int](func(_ context.Context, _ int) (eval.Report, error) {
		started <- struct{}{}
		<-release
		return scoredReport("quality", eval.VerdictPass, 1), nil
	})
	cases := make([]eval.Case[int], eval.DefaultMaxConcurrency+1)
	for index := range cases {
		cases[index] = eval.Case[int]{ID: eval.CaseID(fmt.Sprintf("case-%d", index))}
	}
	dataset, err := eval.NewDataset("test-fixture", cases...)
	if err != nil {
		t.Fatal(err)
	}
	experiment, err := eval.NewExperiment(eval.ExperimentConfig[int]{
		Dataset: dataset, Evaluator: evaluator,
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, runErr := experiment.Run(t.Context())
		done <- runErr
	}()
	for range eval.DefaultMaxConcurrency {
		<-started
	}
	select {
	case <-started:
		t.Fatal("default concurrency exceeded its fixed bound")
	default:
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSuitePreservesHeterogeneousResultsAndExperimentSummarizesEachMetric(t *testing.T) {
	latencyMetric, err := eval.NewMetric(eval.MetricConfig{
		Namespace: "runtime", Name: "latency", Unit: "ms", Direction: eval.DirectionLowerIsBetter,
	})
	if err != nil {
		t.Fatal(err)
	}
	suite, err := eval.NewSuiteEvaluator(eval.SuiteConfig[string]{
		Evaluators: []eval.Evaluator[string]{
			eval.EvaluatorFunc[string](func(context.Context, string) (eval.Report, error) {
				return scoredReport("quality", eval.VerdictPass, 0.8), nil
			}),
			eval.EvaluatorFunc[string](func(context.Context, string) (eval.Report, error) {
				latency := 125.0
				return eval.Report{Metric: latencyMetric, Measurement: &latency}, nil
			}),
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	report, err := suite.Evaluate(t.Context(), "subject")
	if err != nil {
		t.Fatal(err)
	}
	if report.Verdict != eval.VerdictPass || report.Score != nil || len(report.Details) != 2 {
		t.Fatalf("suite report = %#v", report)
	}
	type suiteIdentity struct {
		Metrics []eval.Metric `json:"metrics"`
	}
	identity, found, err := report.Metric.Parameters().Decode[suiteIdentity]("configuration")
	if err != nil || !found || len(identity.Metrics) != 2 || identity.Metrics[0].Name() != "quality" || identity.Metrics[1].Name() != "latency" {
		t.Fatalf("suite metric identity = (%#v, %v, %v)", identity, found, err)
	}

	dataset, err := eval.NewDataset("test-fixture", eval.Case[string]{ID: "case", Subject: "subject"})
	if err != nil {
		t.Fatal(err)
	}
	experiment, err := eval.NewExperiment(eval.ExperimentConfig[string]{
		Dataset: dataset, Evaluator: suite,
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := experiment.Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	summary := run.Summary()
	if summary.Total != 1 || summary.Evaluated != 1 || summary.Passed != 1 || len(summary.Metrics) != 3 {
		t.Fatalf("summary = %#v", summary)
	}
	quality := summary.Metrics[1]
	latency := summary.Metrics[2]
	if quality.Metric.Name() != "quality" || quality.Scores.Count != 1 || quality.Scores.Mean != 0.8 {
		t.Fatalf("quality summary = %#v", quality)
	}
	if !reflect.DeepEqual(latency.Metric, latencyMetric) || latency.Unjudged != 1 || latency.Measurements.Count != 1 || latency.Measurements.Mean != 125 {
		t.Fatalf("latency summary = %#v", latency)
	}
}

func TestCompositeRejectsReportsThatCannotBeMeaningfullyAggregated(t *testing.T) {
	metric, err := eval.NewMetric(eval.MetricConfig{Name: "latency", Unit: "ms", Direction: eval.DirectionLowerIsBetter})
	if err != nil {
		t.Fatal(err)
	}
	composite, err := eval.NewCompositeEvaluator(eval.CompositeConfig[string]{
		Components: []eval.Component[string]{{Evaluator: eval.EvaluatorFunc[string](func(context.Context, string) (eval.Report, error) {
			measurement := 10.0
			return eval.Report{Metric: metric, Measurement: &measurement}, nil
		})}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := composite.Evaluate(t.Context(), "subject"); !errors.Is(err, eval.ErrInvalidReport) {
		t.Fatalf("measurement-only component error = %v", err)
	}
}

func TestScoreMetricAndReportValidation(t *testing.T) {
	parameters := metadata.Map{}
	if err := parameters.Set("rubric", "strict"); err != nil {
		t.Fatal(err)
	}
	metric, err := eval.NewMetric(eval.MetricConfig{
		Namespace: "custom", Name: "quality", Parameters: parameters,
	})
	if err != nil {
		t.Fatal(err)
	}
	parameters["rubric"][1] = 'X'
	if metric.String() != "custom/quality" || string(metric.Parameters()["rubric"]) != `"strict"` {
		t.Fatalf("metric = %#v", metric)
	}
	for _, score := range []float64{-0.1, 1.1, math.NaN(), math.Inf(1)} {
		value := eval.Score(score)
		if err := (eval.Report{Metric: testMetric("quality"), Score: &value}).Validate(); !errors.Is(err, eval.ErrInvalidReport) {
			t.Fatalf("score %v error = %v", score, err)
		}
	}
	value := eval.Score(0.5)
	if err := (eval.Report{Score: &value}).Validate(); !errors.Is(err, eval.ErrInvalidMetric) {
		t.Fatalf("missing metric error = %v", err)
	}
	if _, err := eval.NewMetric(eval.MetricConfig{Namespace: "bad/name", Name: "quality"}); !errors.Is(err, eval.ErrInvalidMetric) {
		t.Fatalf("invalid metric error = %v", err)
	}
	measurement := math.NaN()
	if err := (eval.Report{Metric: testMetric("latency"), Measurement: &measurement}).Validate(); !errors.Is(err, eval.ErrInvalidReport) {
		t.Fatalf("invalid measurement error = %v", err)
	}
	if err := (eval.Report{Metric: testMetric("quality"), Verdict: "maybe"}).Validate(); !errors.Is(err, eval.ErrInvalidReport) {
		t.Fatalf("invalid verdict error = %v", err)
	}
	metadataOnly := metadata.Map{}
	if err := metadataOnly.Set("trace", "present"); err != nil {
		t.Fatal(err)
	}
	if err := (eval.Report{Metric: testMetric("quality"), Metadata: metadataOnly}).Validate(); !errors.Is(err, eval.ErrInvalidReport) {
		t.Fatalf("metadata-only report error = %v, want ErrInvalidReport", err)
	}
	if err := (eval.Report{Metric: testMetric("empty")}).Validate(); !errors.Is(err, eval.ErrInvalidReport) {
		t.Fatalf("empty report error = %v", err)
	}
}

func TestDatasetAndExperimentOwnTheirMetadata(t *testing.T) {
	caseMetadata := metadata.Map{}
	if err := caseMetadata.Set("source", "original"); err != nil {
		t.Fatal(err)
	}
	dataset, err := eval.NewDataset("test-fixture", eval.Case[string]{
		ID: "owned", Subject: "value", Metadata: caseMetadata,
	})
	if err != nil {
		t.Fatal(err)
	}
	caseMetadata["source"][1] = 'X'
	cases := dataset.Cases()
	cases[0].Metadata["source"][1] = 'Y'
	if got := string(dataset.Cases()[0].Metadata["source"]); got != `"original"` {
		t.Fatalf("Dataset metadata = %s", got)
	}

	experiment, err := eval.NewExperiment(eval.ExperimentConfig[string]{
		Dataset: dataset, Evaluator: validStringEvaluator(),
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err := experiment.Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	results := report.Cases()
	results[0].Metadata["source"][1] = 'Z'
	if got := string(report.Cases()[0].Metadata["source"]); got != `"original"` {
		t.Fatalf("ExperimentReport metadata = %s", got)
	}
}

func TestExperimentValidatesDatasetAndRuntimePolicy(t *testing.T) {
	if _, err := eval.NewDataset("test-fixture",
		eval.Case[int]{ID: "duplicate"},
		eval.Case[int]{ID: "duplicate"},
	); !errors.Is(err, eval.ErrInvalidDataset) {
		t.Fatalf("duplicate Dataset error = %v", err)
	}
	dataset, err := eval.NewDataset("test-fixture", eval.Case[int]{ID: "case"})
	if err != nil {
		t.Fatal(err)
	}
	var typedNil *nilEvaluator
	for _, config := range []eval.ExperimentConfig[int]{
		{Evaluator: validIntEvaluator()},
		{Dataset: dataset},
		{Dataset: dataset, Evaluator: typedNil},
		{Dataset: dataset, Evaluator: validIntEvaluator(), MaxConcurrency: -1},
		{Dataset: dataset, Evaluator: validIntEvaluator(), ErrorPolicy: "unknown"},
	} {
		if _, err := eval.NewExperiment(config); !errors.Is(err, eval.ErrInvalidExperiment) {
			t.Fatalf("NewExperiment(%#v) error = %v", config, err)
		}
	}
}

func TestExperimentCancellationPreservesCaseIdentity(t *testing.T) {
	dataset, err := eval.NewDataset("test-fixture",
		eval.Case[int]{ID: "first"},
		eval.Case[int]{ID: "second"},
	)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	experiment, err := eval.NewExperiment(eval.ExperimentConfig[int]{
		Dataset: dataset,
		Evaluator: eval.EvaluatorFunc[int](func(context.Context, int) (eval.Report, error) {
			calls++
			return scoredReport("quality", eval.VerdictPass, 1), nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	report, err := experiment.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context cancellation", err)
	}
	if calls != 0 {
		t.Fatalf("evaluator calls = %d, want 0", calls)
	}
	for index, result := range report.Cases() {
		if !errors.Is(result.Err, context.Canceled) {
			t.Fatalf("cases[%d] error = %v", index, result.Err)
		}
	}
}

func TestExperimentReportDoesNotExposeOwnedMetadata(t *testing.T) {
	parameters := metadata.Map{}
	if err := parameters.Set("rubric", "strict"); err != nil {
		t.Fatal(err)
	}
	metric, err := eval.NewMetric(eval.MetricConfig{Name: "quality", Parameters: parameters})
	if err != nil {
		t.Fatal(err)
	}
	dataset, err := eval.NewDataset("test-fixture", eval.Case[int]{ID: "case"})
	if err != nil {
		t.Fatal(err)
	}
	experiment, err := eval.NewExperiment(eval.ExperimentConfig[int]{
		Dataset: dataset,
		Evaluator: eval.EvaluatorFunc[int](func(context.Context, int) (eval.Report, error) {
			score := eval.Score(1)
			return eval.Report{Metric: metric, Score: &score}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err := experiment.Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	cases := report.Cases()
	caseParameters := cases[0].Report.Metric.Parameters()
	caseParameters["rubric"][1] = 'X'
	*cases[0].Report.Score = 0
	summary := report.Summary()
	summaryParameters := summary.Metrics[0].Metric.Parameters()
	summaryParameters["rubric"][1] = 'Y'
	summary.Metrics[0].Scores.Mean = 0
	if got := string(report.Cases()[0].Report.Metric.Parameters()["rubric"]); got != `"strict"` {
		t.Fatalf("case metric parameters = %s", got)
	}
	if got := string(report.Summary().Metrics[0].Metric.Parameters()["rubric"]); got != `"strict"` {
		t.Fatalf("summary metric parameters = %s", got)
	}
	if got := *report.Cases()[0].Report.Score; got != 1 {
		t.Fatalf("case score = %g, want 1", got)
	}
	comparison, err := report.Compare(report)
	if err != nil {
		t.Fatal(err)
	}
	comparison.Baseline.Metrics[0].Scores.Mean = 0
	comparison.Candidate.Metrics[0].Scores.Mean = 0
	if got := report.Summary().Metrics[0].Scores.Mean; got != 1 {
		t.Fatalf("summary score mean = %g, want 1", got)
	}
}

func TestCompareReportsExactDeltasWithoutInventingSignificance(t *testing.T) {
	dataset, err := eval.NewDataset("test-fixture",
		eval.Case[float64]{ID: "low", Subject: 0.2},
		eval.Case[float64]{ID: "high", Subject: 0.6},
	)
	if err != nil {
		t.Fatal(err)
	}
	metric := testMetric("quality")
	evaluator := func(increment float64) eval.Evaluator[float64] {
		return eval.EvaluatorFunc[float64](func(_ context.Context, subject float64) (eval.Report, error) {
			score := eval.Score(subject + increment)
			verdict, verdictErr := score.Verdict(0.5)
			if verdictErr != nil {
				return eval.Report{}, verdictErr
			}
			return eval.Report{Metric: metric, Verdict: verdict, Score: &score}, nil
		})
	}
	baselineExperiment, err := eval.NewExperiment(eval.ExperimentConfig[float64]{
		Dataset: dataset, Evaluator: evaluator(0),
	})
	if err != nil {
		t.Fatal(err)
	}
	candidateExperiment, err := eval.NewExperiment(eval.ExperimentConfig[float64]{
		Dataset: dataset, Evaluator: evaluator(0.4),
	})
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := baselineExperiment.Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := candidateExperiment.Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	comparison, err := baseline.Compare(candidate)
	if err != nil {
		t.Fatal(err)
	}
	if comparison.PassedDelta != 1 || comparison.FailedDelta != -1 || len(comparison.Metrics) != 1 {
		t.Fatalf("Comparison = %#v", comparison)
	}
	delta := comparison.Metrics[0].ScoreDelta
	if !delta.Present || math.Abs(delta.Mean-0.4) > 1e-12 || comparison.Metrics[0].MeasurementDelta.Present {
		t.Fatalf("Metric delta = %#v", comparison.Metrics[0])
	}

	otherDataset, err := eval.NewDataset("test-fixture", eval.Case[float64]{ID: "other", Subject: 0.2})
	if err != nil {
		t.Fatal(err)
	}
	otherExperiment, err := eval.NewExperiment(eval.ExperimentConfig[float64]{
		Dataset: otherDataset, Evaluator: evaluator(0),
	})
	if err != nil {
		t.Fatal(err)
	}
	other, err := otherExperiment.Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := baseline.Compare(other); !errors.Is(err, eval.ErrInvalidComparison) {
		t.Fatalf("mismatched Dataset comparison error = %v", err)
	}
}

type nilEvaluator struct{}

func (*nilEvaluator) Evaluate(context.Context, int) (eval.Report, error) {
	return eval.Report{}, nil
}

func validIntEvaluator() eval.Evaluator[int] {
	return eval.EvaluatorFunc[int](func(context.Context, int) (eval.Report, error) {
		return scoredReport("valid", eval.VerdictPass, 1), nil
	})
}

func validStringEvaluator() eval.Evaluator[string] {
	return eval.EvaluatorFunc[string](func(context.Context, string) (eval.Report, error) {
		return scoredReport("valid", eval.VerdictPass, 1), nil
	})
}
