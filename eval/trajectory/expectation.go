package trajectory

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/eval"
)

// ToolSequence makes an exact ordered Tool-call assertion explicit. A nil
// *ToolSequence skips the assertion; a non-nil empty sequence asserts no calls.
type ToolSequence struct {
	Calls []ToolExpectation `json:"calls"`
}

func (t ToolSequence) report(actual []ToolCall) (eval.Report, error) {
	passed := len(actual) == len(t.Calls)
	feedback := fmt.Sprintf("observed the expected %d Tool calls", len(t.Calls))
	if !passed {
		feedback = fmt.Sprintf("observed %d Tool calls, expected %d", len(actual), len(t.Calls))
	}
	for index := 0; passed && index < len(t.Calls); index++ {
		matches, err := t.Calls[index].matches(actual[index])
		if err != nil {
			return eval.Report{}, err
		}
		if !matches {
			passed = false
			feedback = fmt.Sprintf("Tool call %d did not match its expected name, arguments, or outcome", index)
		}
	}
	return binaryReport(MetricToolCalls, passed, feedback)
}

// ToolArguments is one exact semantic JSON argument assertion. Its empty value
// matches a Tool call that supplied no argument text. Non-empty values must be
// strict RFC 7493 JSON; number spelling and precision remain intact.
type ToolArguments string

func (t ToolArguments) Validate() error {
	_, err := canonicalArguments(string(t))
	if err != nil {
		return fmt.Errorf("%w: tool arguments: %w", ErrInvalidSample, err)
	}
	return nil
}

// ToolExpectation asserts that a tool was called, and optionally how. Arguments
// and Outcome are omissible so a sample can pin the part of the behavior it
// cares about without freezing the rest; an expectation that had to state every
// field would break on unrelated prompt or model changes and stop being run.
type ToolExpectation struct {
	Name      string         `json:"name"`
	Arguments *ToolArguments `json:"arguments,omitempty"`
	Outcome   ToolOutcome    `json:"outcome,omitempty"`
}

func (t ToolExpectation) Validate() error {
	if t.Name == "" || t.Name != strings.TrimSpace(t.Name) {
		return fmt.Errorf("%w: tool name must be non-empty without surrounding whitespace", ErrInvalidSample)
	}
	if t.Arguments != nil {
		if err := t.Arguments.Validate(); err != nil {
			return err
		}
	}
	if t.Outcome != ToolOutcomeInvalid && !t.Outcome.Valid() {
		return fmt.Errorf("%w: tool outcome is invalid", ErrInvalidSample)
	}
	return nil
}

func (t ToolExpectation) matches(actual ToolCall) (bool, error) {
	if actual.Call.Name != t.Name {
		return false, nil
	}
	if t.Arguments != nil {
		actualArguments, err := canonicalArguments(actual.Call.Arguments)
		if err != nil {
			return false, err
		}
		expectedArguments, err := canonicalArguments(string(*t.Arguments))
		if err != nil {
			return false, err
		}
		if !bytes.Equal(actualArguments, expectedArguments) {
			return false, nil
		}
	}
	return t.Outcome == ToolOutcomeInvalid || actual.Outcome == t.Outcome, nil
}

// Limits defines optional upper bounds. Pointers distinguish an asserted zero
// from a dimension the case does not evaluate.
type Limits struct {
	CommittedSteps  *uint64        `json:"committed_steps,omitempty"`
	PreparedEffects *uint64        `json:"prepared_effects,omitempty"`
	AcceptedSignals *uint64        `json:"accepted_signals,omitempty"`
	DroppedDeltas   *uint64        `json:"dropped_deltas,omitempty"`
	TotalTokens     *int64         `json:"total_tokens,omitempty"`
	Duration        *time.Duration `json:"duration,omitempty"`
}

func (l Limits) Validate() error {
	if l.TotalTokens != nil && *l.TotalTokens < 0 {
		return fmt.Errorf("%w: total token limit must not be negative", ErrInvalidSample)
	}
	if l.Duration != nil && *l.Duration < 0 {
		return fmt.Errorf("%w: duration limit must not be negative", ErrInvalidSample)
	}
	return nil
}

func (l Limits) reports(actual Trajectory) ([]eval.Report, error) {
	reports := make([]eval.Report, 0, 6)
	if l.CommittedSteps != nil {
		report, err := measurementReport(
			MetricCommittedSteps, metricUnitCount,
			float64(actual.usage.CommittedSteps), actual.usage.CommittedSteps <= *l.CommittedSteps,
			*l.CommittedSteps,
		)
		if err != nil {
			return nil, err
		}
		reports = append(reports, report)
	}
	if l.PreparedEffects != nil {
		report, err := measurementReport(
			MetricPreparedEffects, metricUnitCount,
			float64(actual.usage.PreparedEffects), actual.usage.PreparedEffects <= *l.PreparedEffects,
			*l.PreparedEffects,
		)
		if err != nil {
			return nil, err
		}
		reports = append(reports, report)
	}
	if l.AcceptedSignals != nil {
		report, err := measurementReport(
			MetricAcceptedSignals, metricUnitCount,
			float64(actual.usage.AcceptedSignals), actual.usage.AcceptedSignals <= *l.AcceptedSignals,
			*l.AcceptedSignals,
		)
		if err != nil {
			return nil, err
		}
		reports = append(reports, report)
	}
	if l.DroppedDeltas != nil {
		report, err := measurementReport(
			MetricDroppedDeltas, metricUnitCount,
			float64(actual.usage.DroppedDeltas), actual.usage.DroppedDeltas <= *l.DroppedDeltas,
			*l.DroppedDeltas,
		)
		if err != nil {
			return nil, err
		}
		reports = append(reports, report)
	}
	if l.TotalTokens != nil {
		tokens, err := actual.TotalTokens()
		if err != nil {
			return nil, err
		}
		report, err := measurementReport(
			MetricTotalTokens, metricUnitToken,
			float64(tokens), tokens <= *l.TotalTokens, *l.TotalTokens,
		)
		if err != nil {
			return nil, err
		}
		reports = append(reports, report)
	}
	if l.Duration != nil {
		report, err := measurementReport(
			MetricDuration, metricUnitSecond,
			actual.duration.Seconds(), actual.duration <= *l.Duration,
			l.Duration.Seconds(),
		)
		if err != nil {
			return nil, err
		}
		reports = append(reports, report)
	}
	return reports, nil
}

// Expectation describes case-specific success without contaminating Metric
// identity. Baseline is optional and enables deterministic replay comparison.
type Expectation struct {
	Status   agent.Status  `json:"status"`
	Output   *agent.Output `json:"output,omitempty"`
	Tools    *ToolSequence `json:"tools,omitempty"`
	Baseline *Trajectory   `json:"baseline,omitempty"`
	Limits   Limits        `json:"limits,omitzero"`
}

func (e Expectation) Validate() error {
	if !e.Status.Terminal() {
		return fmt.Errorf("%w: expected status must be terminal", ErrInvalidSample)
	}
	if e.Output != nil && !e.Output.Valid() {
		return fmt.Errorf("%w: expected output is invalid", ErrInvalidSample)
	}
	if e.Status != agent.StatusCompleted && e.Output != nil {
		return fmt.Errorf("%w: only completed status can expect output", ErrInvalidSample)
	}
	if e.Tools != nil {
		for index, call := range e.Tools.Calls {
			if err := call.Validate(); err != nil {
				return fmt.Errorf("%w: tools[%d]: %w", ErrInvalidSample, index, err)
			}
		}
	}
	if e.Baseline != nil {
		if err := e.Baseline.Validate(); err != nil {
			return fmt.Errorf("%w: baseline: %w", ErrInvalidSample, err)
		}
	}
	return e.Limits.Validate()
}

// Sample is the typed subject consumed by Evaluator.
type Sample struct {
	Actual   Trajectory  `json:"actual"`
	Expected Expectation `json:"expected"`
}

func (s Sample) Validate() error {
	if err := s.Actual.Validate(); err != nil {
		return fmt.Errorf("%w: actual: %w", ErrInvalidSample, err)
	}
	if err := s.Expected.Validate(); err != nil {
		return err
	}
	return nil
}

func (s Sample) taskReport() (eval.Report, error) {
	passed := s.Actual.termination.Status() == s.Expected.Status
	feedback := "terminal status matched"
	if !passed {
		feedback = fmt.Sprintf(
			"terminal status was %s, expected %s",
			s.Actual.termination.Status(), s.Expected.Status,
		)
	}
	if passed && s.Expected.Output != nil {
		passed = s.Actual.output != nil &&
			bytes.Equal(s.Actual.output.JSON(), s.Expected.Output.JSON())
		if passed {
			feedback = "terminal status and output matched"
		} else {
			feedback = "terminal output differed from the expected value"
		}
	}
	return binaryReport(MetricTaskSuccess, passed, feedback)
}
