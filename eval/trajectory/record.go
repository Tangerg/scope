package trajectory

import (
	"fmt"
	"strings"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/chat"
)

// ToolOutcome is the complete host-boundary outcome of one started Tool call.
type ToolOutcome string

// Tool outcomes are a closed vocabulary because a trajectory is compared
// against a recorded baseline. An open set would let two runs describe the
// same result with different words and register as a behavior change.
const (
	ToolOutcomeInvalid       ToolOutcome = ""
	ToolOutcomeSucceeded     ToolOutcome = "succeeded"
	ToolOutcomeError         ToolOutcome = "error"
	ToolOutcomeInputRequired ToolOutcome = "input_required"
	ToolOutcomeFailed        ToolOutcome = "failed"
	ToolOutcomeUnknown       ToolOutcome = "unknown"
)

func (t ToolOutcome) Valid() bool {
	switch t {
	case ToolOutcomeSucceeded, ToolOutcomeError, ToolOutcomeInputRequired,
		ToolOutcomeFailed, ToolOutcomeUnknown:
		return true
	default:
		return false
	}
}

// ModelCall is one settled model boundary attributed to an Agent Process Step.
type ModelCall struct {
	TreeIncarnationID agent.TreeIncarnationID `json:"tree_incarnation_id,omitzero"`
	EffectID          agent.EffectID          `json:"effect_id"`
	ProcessID         agent.ProcessID         `json:"process_id"`
	StepSequence      uint64                  `json:"step_sequence"`
	CallSequence      uint32                  `json:"call_sequence"`
	Response          *chat.Response          `json:"response"`
}

func (m ModelCall) Clone() ModelCall {
	m.Response = m.Response.Clone()
	return m
}

func (m ModelCall) Validate() error {
	if !m.EffectID.Valid() || !m.ProcessID.Valid() || m.StepSequence == 0 || m.CallSequence == 0 || m.Response == nil {
		return fmt.Errorf("%w: model call attribution is incomplete", ErrInvalidTrajectory)
	}
	if err := m.Response.Validate(); err != nil {
		return fmt.Errorf("%w: model response: %w", ErrInvalidTrajectory, err)
	}
	return nil
}

// ToolCall is one settled Tool boundary attributed to an Agent Process Step.
type ToolCall struct {
	TreeIncarnationID agent.TreeIncarnationID `json:"tree_incarnation_id,omitzero"`
	EffectID          agent.EffectID          `json:"effect_id"`
	ProcessID         agent.ProcessID         `json:"process_id"`
	StepSequence      uint64                  `json:"step_sequence"`
	ModelCall         uint32                  `json:"model_call"`
	Index             uint32                  `json:"index"`
	Call              chat.ToolCall           `json:"call"`
	Outcome           ToolOutcome             `json:"outcome"`
	Result            *chat.ToolResult        `json:"result,omitempty"`
	// Failure describes a failed call or diagnoses an unknown outcome without
	// claiming that the external operation definitely failed.
	Failure string `json:"failure,omitempty"`
}

func (t ToolCall) Clone() ToolCall {
	if t.Result != nil {
		result := t.Result.Clone()
		t.Result = &result
	}
	return t
}

func (t ToolCall) Validate() error {
	if !t.EffectID.Valid() || !t.ProcessID.Valid() || t.StepSequence == 0 || t.ModelCall == 0 {
		return fmt.Errorf("%w: tool call attribution is incomplete", ErrInvalidTrajectory)
	}
	if err := t.Call.Validate(); err != nil {
		return fmt.Errorf("%w: tool call: %w", ErrInvalidTrajectory, err)
	}
	if _, err := canonicalArguments(t.Call.Arguments); err != nil {
		return fmt.Errorf("%w: tool call arguments: %w", ErrInvalidTrajectory, err)
	}
	if !t.Outcome.Valid() {
		return fmt.Errorf("%w: tool outcome is invalid", ErrInvalidTrajectory)
	}
	switch t.Outcome {
	case ToolOutcomeSucceeded:
		if t.Result == nil || t.Result.IsError || t.Failure != "" {
			return fmt.Errorf("%w: succeeded tool call requires one non-error result", ErrInvalidTrajectory)
		}
	case ToolOutcomeError:
		if t.Result == nil || !t.Result.IsError || t.Failure != "" {
			return fmt.Errorf("%w: error tool call requires one error result", ErrInvalidTrajectory)
		}
	case ToolOutcomeFailed:
		failure := strings.TrimSpace(t.Failure)
		if t.Result != nil || failure == "" || t.Failure != failure {
			return fmt.Errorf("%w: failed tool call requires one failure", ErrInvalidTrajectory)
		}
	case ToolOutcomeInputRequired:
		if t.Result != nil || t.Failure != "" {
			return fmt.Errorf("%w: %s tool call cannot carry a result or failure", ErrInvalidTrajectory, t.Outcome)
		}
	case ToolOutcomeUnknown:
		if t.Result != nil || t.Failure != strings.TrimSpace(t.Failure) {
			return fmt.Errorf("%w: unknown tool call permits only an optional failure diagnostic", ErrInvalidTrajectory)
		}
	}
	if t.Result != nil {
		if err := t.Result.Validate(); err != nil {
			return fmt.Errorf("%w: tool result: %w", ErrInvalidTrajectory, err)
		}
		if t.Result.ID != t.Call.ID || t.Result.Name != t.Call.Name {
			return fmt.Errorf("%w: tool result does not address its call", ErrInvalidTrajectory)
		}
	}
	return nil
}
