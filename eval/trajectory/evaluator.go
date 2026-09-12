package trajectory

import (
	"context"
	"encoding/json"
	"fmt"

	agent "github.com/Tangerg/scope/agent"

	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/eval"
)

// These metric names are constants because a report is aggregated by exact
// metric identity. A renamed or restated metric silently splits a series that
// a reader would compare as one.
const (
	metricNamespace                       = "agent"
	metricMaximumKey                      = "maximum"
	metricUnitCount                       = "count"
	metricUnitToken                       = "token"
	metricUnitSecond                      = "s"
	MetricTrajectory      eval.MetricName = "trajectory"
	MetricExpectedOutcome eval.MetricName = "expected_outcome"
	MetricToolCalls       eval.MetricName = "tool_calls"
	MetricConsistency     eval.MetricName = "consistency"
	MetricCommittedSteps  eval.MetricName = "committed_steps"
	MetricPreparedEffects eval.MetricName = "prepared_effects"
	MetricAcceptedSignals eval.MetricName = "accepted_signals"
	MetricDroppedDeltas   eval.MetricName = "dropped_deltas"
	MetricTotalTokens     eval.MetricName = "total_tokens"
	MetricElapsed         eval.MetricName = "recording_elapsed"
)

// Evaluator deterministically checks the expected terminal outcome, Tool behavior,
// replay consistency, and configured resource regressions for one Sample.
// OutputProjection is required only when comparing a replay baseline.
// Resource counts cover the whole tree; unknown evidence returns an error.
type Evaluator struct {
	// OutputProjection selects the business output used by optional replay comparison.
	OutputProjection eval.Projection[agent.Output, json.RawMessage]
}

func (e Evaluator) Evaluate(ctx context.Context, sample Sample) (eval.Report, error) {
	if checkErr := ctx.Err(); checkErr != nil {
		return eval.Report{}, checkErr
	}
	if checkErr := sample.Validate(); checkErr != nil {
		return eval.Report{}, checkErr
	}
	details := make([]eval.Report, 0, 9)
	task, err := sample.outcomeReport()
	if err != nil {
		return eval.Report{}, err
	}
	details = append(details, task)
	if sample.Expected.Tools != nil {
		if checkErr := sample.Actual.validateCoverage(); checkErr != nil {
			return eval.Report{}, checkErr
		}
		tools, toolErr := sample.Expected.Tools.report(sample.Actual.toolCalls)
		if toolErr != nil {
			return eval.Report{}, toolErr
		}
		details = append(details, tools)
	}
	if sample.Expected.Baseline != nil {
		consistency, consistencyErr := sample.Actual.consistencyReport(*sample.Expected.Baseline, e.OutputProjection)
		if consistencyErr != nil {
			return eval.Report{}, consistencyErr
		}
		details = append(details, consistency)
	}
	resourceReports, err := sample.Expected.Limits.reports(sample.Actual)
	if err != nil {
		return eval.Report{}, err
	}
	details = append(details, resourceReports...)
	metric, err := eval.NewMetric(eval.MetricConfig{Namespace: metricNamespace, Name: MetricTrajectory})
	if err != nil {
		return eval.Report{}, err
	}
	verdict := eval.VerdictPass
	for _, detail := range details {
		if detail.Verdict == eval.VerdictFail {
			verdict = eval.VerdictFail
			break
		}
	}
	report := eval.Report{Metric: metric, Verdict: verdict, Details: details}
	if checkErr := report.Validate(); checkErr != nil {
		return eval.Report{}, checkErr
	}
	return report, nil
}

func binaryReport(name eval.MetricName, passed bool, feedback string) (eval.Report, error) {
	metric, err := eval.NewMetric(eval.MetricConfig{Namespace: metricNamespace, Name: name})
	if err != nil {
		return eval.Report{}, err
	}
	score := eval.Score(0)
	verdict := eval.VerdictFail
	if passed {
		score = 1
		verdict = eval.VerdictPass
	}
	report := eval.Report{Metric: metric, Verdict: verdict, Score: &score, Feedback: feedback}
	if checkErr := report.Validate(); checkErr != nil {
		return eval.Report{}, checkErr
	}
	return report, nil
}

func measurementReport[Maximum uint64 | int64 | float64](
	name eval.MetricName,
	unit string,
	measurement float64,
	passed bool,
	maximum Maximum,
) (eval.Report, error) {
	parameters := metadata.Map{}
	if checkErr := parameters.Set(metricMaximumKey, maximum); checkErr != nil {
		return eval.Report{}, fmt.Errorf("eval/trajectory: metric maximum: %w", checkErr)
	}
	metric, err := eval.NewMetric(eval.MetricConfig{
		Namespace: metricNamespace, Name: name, Unit: unit,
		Direction: eval.DirectionLowerIsBetter, Parameters: parameters,
	})
	if err != nil {
		return eval.Report{}, err
	}
	verdict := eval.VerdictFail
	if passed {
		verdict = eval.VerdictPass
	}
	report := eval.Report{Metric: metric, Verdict: verdict, Measurement: &measurement}
	if checkErr := report.Validate(); checkErr != nil {
		return eval.Report{}, checkErr
	}
	return report, nil
}

var _ eval.Evaluator[Sample] = Evaluator{}
