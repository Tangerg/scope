package trajectory

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/eval"
)

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
	OutputProjection eval.Projection[agent.Payload, json.RawMessage]
}

type decisionRule struct {
	Policy     string       `json:"policy"`
	Parameters metadata.Map `json:"parameters,omitzero"`
}

func (e Evaluator) Evaluate(ctx context.Context, sample Sample) (eval.Report, error) {
	if err := ctx.Err(); err != nil {
		return eval.Report{}, err
	}
	if err := sample.Validate(); err != nil {
		return eval.Report{}, err
	}
	details, err := sample.reports(e.OutputProjection)
	if err != nil {
		return eval.Report{}, err
	}
	return allExpectationsReport(details)
}

func allExpectationsReport(details []eval.Report) (eval.Report, error) {
	metric, err := eval.NewMetric(eval.MetricConfig{Namespace: metricNamespace, Name: MetricTrajectory})
	if err != nil {
		return eval.Report{}, err
	}
	verdict := eval.VerdictPass
	if slices.ContainsFunc(details, func(detail eval.Report) bool { return detail.Verdict() == eval.VerdictFail }) {
		verdict = eval.VerdictFail
	}
	parameters := metadata.Map{}
	rules := make([]decisionRule, len(details))
	for index, detail := range details {
		rules[index] = decisionRule{Policy: detail.Decision.Policy, Parameters: detail.Decision.Parameters.Clone()}
	}
	if err := parameters.Set("rules", rules); err != nil {
		return eval.Report{}, err
	}
	report := eval.Report{Metric: metric, Decision: &eval.Decision{Policy: "trajectory.all_expectations", Parameters: parameters, Verdict: verdict}, Details: details}
	if checkErr := report.Validate(); checkErr != nil {
		return eval.Report{}, checkErr
	}
	return report, nil
}

func binaryReport(name eval.MetricName, passed bool, feedback string, parameters metadata.Map) (eval.Report, error) {
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
	report := eval.Report{Metric: metric, Decision: &eval.Decision{Policy: "trajectory." + string(name), Parameters: parameters, Verdict: verdict}, Score: &score, Feedback: feedback}
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
		Direction: eval.DirectionLowerIsBetter,
	})
	if err != nil {
		return eval.Report{}, err
	}
	verdict := eval.VerdictFail
	if passed {
		verdict = eval.VerdictPass
	}
	report := eval.Report{Metric: metric, Decision: &eval.Decision{Policy: "trajectory.maximum", Parameters: parameters, Verdict: verdict}, Measurement: &measurement}
	if checkErr := report.Validate(); checkErr != nil {
		return eval.Report{}, checkErr
	}
	return report, nil
}

var _ eval.Evaluator[Sample] = Evaluator{}
