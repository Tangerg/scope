package trajectory

import (
	"fmt"
	"strings"
	"time"

	agent "github.com/Tangerg/scope/agent"
)

// ToolSequence makes an exact ordered Tool-call assertion explicit. A nil
// *ToolSequence skips the assertion; a non-nil empty sequence asserts no calls.
type ToolSequence struct {
	Calls []ToolExpectation `json:"calls"`
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
