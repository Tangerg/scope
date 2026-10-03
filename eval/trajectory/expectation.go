package trajectory

import (
	"bytes"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"strings"
	"time"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/eval"
)

// ToolSequence makes an exact ordered Tool-call assertion explicit. A nil
// *ToolSequence skips the assertion; a non-nil empty sequence asserts no calls.
// Order follows semantic process paths; Interaction children use their model
// call position and Tool index, and repeated calls retain process-local dispatch
// order. This is not cross-process causal order. Full history is required.
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
	parameters := metadata.Map{}
	if err := parameters.Set("expected", t); err != nil {
		return eval.Report{}, err
	}
	return binaryReport(MetricToolCalls, passed, feedback, parameters)
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

// ToolExpectation asserts one Tool call. Omitted Arguments or Outcome leave that
// part unasserted, so unrelated prompt or model changes do not break the sample.
// An unknown actual outcome cannot decide an expected definite outcome and
// returns ErrIncompleteRecording; a name or argument mismatch still fails.
type ToolExpectation struct {
	Name      string         `json:"name"`
	Arguments *ToolArguments `json:"arguments,omitzero"`
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
	if actual.Outcome() == ToolOutcomeUnknown && t.Outcome != ToolOutcomeInvalid && t.Outcome != ToolOutcomeUnknown {
		return false, fmt.Errorf("%w: Tool outcome is unknown; expected %s", ErrIncompleteRecording, t.Outcome)
	}
	return t.Outcome == ToolOutcomeInvalid || actual.Outcome() == t.Outcome, nil
}

// Limits defines optional upper bounds. Pointers distinguish an asserted zero
// from a dimension the case does not evaluate.
type Limits struct {
	CommittedSteps  *uint64        `json:"committed_steps,omitzero"`
	PreparedEffects *uint64        `json:"prepared_effects,omitzero"`
	AcceptedSignals *uint64        `json:"accepted_signals,omitzero"`
	DroppedDeltas   *uint64        `json:"dropped_deltas,omitzero"`
	TotalTokens     *int64         `json:"total_tokens,omitzero"`
	Elapsed         *time.Duration `json:"elapsed_ns,omitzero"`
}

type limitsWire struct {
	CommittedSteps  *uint64 `json:"committed_steps,omitzero"`
	PreparedEffects *uint64 `json:"prepared_effects,omitzero"`
	AcceptedSignals *uint64 `json:"accepted_signals,omitzero"`
	DroppedDeltas   *uint64 `json:"dropped_deltas,omitzero"`
	TotalTokens     *int64  `json:"total_tokens,omitzero"`
	Elapsed         *int64  `json:"elapsed_ns,omitzero"`
}

func (l Limits) MarshalJSON() ([]byte, error) {
	return jsonv2.Marshal(limitsWire{l.CommittedSteps, l.PreparedEffects, l.AcceptedSignals, l.DroppedDeltas, l.TotalTokens, (*int64)(l.Elapsed)})
}

func (l *Limits) UnmarshalJSON(data []byte) error {
	var wire limitsWire
	if err := jsonv2.Unmarshal(data, &wire, jsonv2.RejectUnknownMembers(true)); err != nil {
		return err
	}
	*l = Limits{wire.CommittedSteps, wire.PreparedEffects, wire.AcceptedSignals, wire.DroppedDeltas, wire.TotalTokens, (*time.Duration)(wire.Elapsed)}
	return nil
}

func (l Limits) Validate() error {
	if l.TotalTokens != nil && *l.TotalTokens < 0 {
		return fmt.Errorf("%w: total token limit must not be negative", ErrInvalidSample)
	}
	if l.Elapsed != nil && *l.Elapsed < 0 {
		return fmt.Errorf("%w: duration limit must not be negative", ErrInvalidSample)
	}
	return nil
}

func (l Limits) reports(actual Trajectory) ([]eval.Report, error) {
	reports, err := l.usageReports(actual)
	if err != nil {
		return nil, err
	}
	if l.TotalTokens != nil {
		report, err := l.tokenReport(actual)
		if err != nil {
			return nil, err
		}
		reports = append(reports, report)
	}
	if l.Elapsed != nil {
		report, err := l.elapsedReport(actual)
		if err != nil {
			return nil, err
		}
		reports = append(reports, report)
	}
	return reports, nil
}

// usageReports ignores unknown tree usage when no usage bound needs it.
func (l Limits) usageReports(actual Trajectory) ([]eval.Report, error) {
	if l.CommittedSteps == nil && l.PreparedEffects == nil && l.AcceptedSignals == nil && l.DroppedDeltas == nil {
		return nil, nil
	}
	usage, err := actual.TreeUsage()
	if err != nil {
		return nil, err
	}
	var reports []eval.Report
	for _, bound := range [...]struct {
		metric eval.MetricName
		value  uint64
		limit  *uint64
	}{
		{MetricCommittedSteps, usage.CommittedSteps, l.CommittedSteps},
		{MetricPreparedEffects, usage.PreparedEffects, l.PreparedEffects},
		{MetricAcceptedSignals, usage.AcceptedSignals, l.AcceptedSignals},
		{MetricDroppedDeltas, usage.DroppedDeltas, l.DroppedDeltas},
	} {
		if bound.limit == nil {
			continue
		}
		report, err := measurementReport(
			bound.metric, metricUnitCount,
			float64(bound.value), bound.value <= *bound.limit, *bound.limit,
		)
		if err != nil {
			return nil, err
		}
		reports = append(reports, report)
	}
	return reports, nil
}

func (l Limits) tokenReport(actual Trajectory) (eval.Report, error) {
	tokens, err := actual.TotalTokens()
	if err != nil {
		return eval.Report{}, err
	}
	return measurementReport(
		MetricTotalTokens, metricUnitToken,
		float64(tokens), tokens <= *l.TotalTokens, *l.TotalTokens,
	)
}

func (l Limits) elapsedReport(actual Trajectory) (eval.Report, error) {
	if actual.elapsed == nil {
		return eval.Report{}, fmt.Errorf("%w: recording elapsed time is unknown", ErrIncompleteRecording)
	}
	return measurementReport(
		MetricElapsed, metricUnitSecond,
		actual.elapsed.Seconds(), *actual.elapsed <= *l.Elapsed,
		l.Elapsed.Seconds(),
	)
}

// Expectation describes case-specific success without contaminating Metric
// identity. Baseline is optional and enables deterministic replay comparison.
// A zero Output omits the assertion; a valid JSON null asserts a null output.
type Expectation struct {
	Status   agent.Status  `json:"status"`
	Output   agent.Payload `json:"output,omitzero"`
	Tools    *ToolSequence `json:"tools,omitzero"`
	Baseline *Trajectory   `json:"baseline,omitzero"`
	Limits   Limits        `json:"limits,omitzero"`
}

func (e Expectation) Validate() error {
	if !e.Status.Terminal() {
		return fmt.Errorf("%w: expected status must be terminal", ErrInvalidSample)
	}
	if e.Status != agent.StatusCompleted && !e.Output.IsZero() {
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

func (s Sample) reports(project eval.Projection[agent.Payload, json.RawMessage]) ([]eval.Report, error) {
	outcome, err := s.outcomeReport()
	if err != nil {
		return nil, err
	}
	reports := []eval.Report{outcome}
	if s.Expected.Tools != nil {
		tools, toolErr := s.toolReport()
		if toolErr != nil {
			return nil, toolErr
		}
		reports = append(reports, tools)
	}
	if s.Expected.Baseline != nil {
		consistency, consistencyErr := s.Actual.consistencyReport(*s.Expected.Baseline, project)
		if consistencyErr != nil {
			return nil, consistencyErr
		}
		reports = append(reports, consistency)
	}
	limits, err := s.Expected.Limits.reports(s.Actual)
	if err != nil {
		return nil, err
	}
	return append(reports, limits...), nil
}

func (s Sample) toolReport() (eval.Report, error) {
	calls, err := s.Actual.semanticToolCalls()
	if err != nil {
		return eval.Report{}, err
	}
	return s.Expected.Tools.report(calls)
}

func (s Sample) outcomeReport() (eval.Report, error) {
	if !s.Actual.termination.Valid() {
		return eval.Report{}, fmt.Errorf("%w: root result is unknown", ErrIncompleteRecording)
	}
	passed := s.Actual.termination.Status() == s.Expected.Status
	feedback := "terminal status matched"
	if !passed {
		feedback = fmt.Sprintf(
			"terminal status was %s, expected %s",
			s.Actual.termination.Status(), s.Expected.Status,
		)
	}
	if passed && !s.Expected.Output.IsZero() {
		passed = !s.Actual.output.IsZero() &&
			bytes.Equal(s.Actual.output.JSON(), s.Expected.Output.JSON())
		if passed {
			feedback = "terminal status and output matched"
		} else {
			feedback = "terminal output differed from the expected value"
		}
	}
	parameters := metadata.Map{}
	if err := parameters.Set("status", s.Expected.Status); err != nil {
		return eval.Report{}, err
	}
	if !s.Expected.Output.IsZero() {
		if err := parameters.Set("output", s.Expected.Output); err != nil {
			return eval.Report{}, err
		}
	}
	return binaryReport(MetricExpectedOutcome, passed, feedback, parameters)
}
