package trajectory

import (
	"context"
	"fmt"

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
	MetricTaskSuccess     eval.MetricName = "task_success"
	MetricToolCalls       eval.MetricName = "tool_calls"
	MetricConsistency     eval.MetricName = "consistency"
	MetricCommittedSteps  eval.MetricName = "committed_steps"
	MetricPreparedEffects eval.MetricName = "prepared_effects"
	MetricAcceptedSignals eval.MetricName = "accepted_signals"
	MetricDroppedDeltas   eval.MetricName = "dropped_deltas"
	MetricTotalTokens     eval.MetricName = "total_tokens"
	MetricDuration        eval.MetricName = "duration"
)

// Evaluator deterministically checks terminal success, exact Tool behavior,
// replay consistency, and configured resource regressions for one Sample.
// Its zero value is ready to use because all case-specific policy belongs to
// the typed Sample rather than mutable evaluator configuration.
type Evaluator struct{}

func (Evaluator) Evaluate(ctx context.Context, sample Sample) (eval.Report, error) {
	if err := ctx.Err(); err != nil {
		return eval.Report{}, err
	}
	if err := sample.Validate(); err != nil {
		return eval.Report{}, err
	}
	details := make([]eval.Report, 0, 9)
	task, err := sample.taskReport()
	if err != nil {
		return eval.Report{}, err
	}
	details = append(details, task)
	if sample.Expected.Tools != nil {
		tools, toolErr := sample.Expected.Tools.report(sample.Actual.toolCalls)
		if toolErr != nil {
			return eval.Report{}, toolErr
		}
		details = append(details, tools)
	}
	if sample.Expected.Baseline != nil {
		consistency, consistencyErr := sample.Actual.consistencyReport(*sample.Expected.Baseline)
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
	if err := report.Validate(); err != nil {
		return eval.Report{}, err
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
	if err := report.Validate(); err != nil {
		return eval.Report{}, err
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
	if err := parameters.Set(metricMaximumKey, maximum); err != nil {
		return eval.Report{}, fmt.Errorf("eval/trajectory: metric maximum: %w", err)
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
	if err := report.Validate(); err != nil {
		return eval.Report{}, err
	}
	return report, nil
}

var _ eval.Evaluator[Sample] = Evaluator{}
