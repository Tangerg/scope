package trajectory_test

import (
	"context"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/eval"
	"github.com/Tangerg/scope/eval/trajectory"
)

// Every resource limit uses one policy with only a maximum, so the constrained
// Metric decides whether two trajectory decisions observe the same thing.
func TestChangedLimitMetricIsAnotherObservation(t *testing.T) {
	recorder := &trajectory.Recorder{}
	process, _ := startRecordedInteraction(t, recorder, recorder, fixtureWeatherTool{}, 2)
	recorded, err := recorder.Take(t.Context(), process, nil)
	if err != nil {
		t.Fatal(err)
	}
	zero := uint64(0)
	run := func(limits trajectory.Limits) eval.ExperimentReport {
		t.Helper()
		dataset, datasetErr := eval.NewDataset("trajectory-fixture", eval.Case[trajectory.Trajectory]{ID: "case", Subject: recorded})
		if datasetErr != nil {
			t.Fatal(datasetErr)
		}
		suite, suiteErr := eval.NewSuite(eval.SuiteConfig[trajectory.Trajectory]{Assessments: []eval.Assessment[trajectory.Trajectory]{{
			ID: "trajectory",
			Evaluator: eval.EvaluatorFunc[trajectory.Trajectory](func(ctx context.Context, actual trajectory.Trajectory) (eval.Report, error) {
				return trajectory.Evaluator{}.Evaluate(ctx, trajectory.Sample{
					Actual: actual, Expected: trajectory.Expectation{Status: agent.StatusCompleted, Limits: limits},
				})
			}),
		}}})
		if suiteErr != nil {
			t.Fatal(suiteErr)
		}
		experiment, experimentErr := eval.NewExperiment(eval.ExperimentConfig[trajectory.Trajectory]{Dataset: dataset, Suite: suite})
		if experimentErr != nil {
			t.Fatal(experimentErr)
		}
		report, runErr := experiment.Run(t.Context())
		if runErr != nil {
			t.Fatal(runErr)
		}
		return report
	}
	steps := run(trajectory.Limits{CommittedSteps: &zero})
	same, err := steps.Compare(run(trajectory.Limits{CommittedSteps: &zero}))
	if err != nil || len(same.Metrics) != 1 || same.Metrics[0].DecisionDelta != (eval.DecisionDelta{Matched: 1}) {
		t.Fatalf("same limit comparison = %+v, %v", same, err)
	}
	other, err := steps.Compare(run(trajectory.Limits{PreparedEffects: &zero}))
	if err != nil || len(other.Metrics) != 2 ||
		other.Metrics[0].DecisionDelta != (eval.DecisionDelta{BaselineOnly: 1}) ||
		other.Metrics[1].DecisionDelta != (eval.DecisionDelta{CandidateOnly: 1}) {
		t.Fatalf("another limit metric comparison = %+v, %v", other, err)
	}
}
