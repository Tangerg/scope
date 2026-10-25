package trajectory

import (
	"fmt"
	"strings"
	"unicode/utf8"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/core/chat"
)

// ToolOutcome classifies the retained settlement of one Tool call.
type ToolOutcome string

const (
	ToolOutcomeInvalid       ToolOutcome = ""
	ToolOutcomeSucceeded     ToolOutcome = "succeeded"
	ToolOutcomeError         ToolOutcome = "error"
	ToolOutcomeInputRequired ToolOutcome = "input_required"
	ToolOutcomeFailed        ToolOutcome = "failed"
	ToolOutcomeUnknown       ToolOutcome = "unknown"
	// ToolOutcomeUnobserved means the settlement callback was not retained.
	// It is missing evidence, not an observed unknown external outcome.
	ToolOutcomeUnobserved ToolOutcome = "unobserved"
)

func (t ToolOutcome) Valid() bool {
	switch t {
	case ToolOutcomeSucceeded, ToolOutcomeError, ToolOutcomeInputRequired,
		ToolOutcomeFailed, ToolOutcomeUnknown, ToolOutcomeUnobserved:
		return true
	default:
		return false
	}
}

// ModelCall is one physical model boundary attributed to an Agent Process Step.
type ModelCall struct {
	TreeIncarnationID agent.TreeIncarnationID  `json:"tree_incarnation_id,omitzero"`
	EffectID          agent.EffectID           `json:"effect_id"`
	AttemptID         agent.EffectAttemptID    `json:"attempt_id"`
	ProcessID         agent.ProcessID          `json:"process_id"`
	StepSequence      uint64                   `json:"step_sequence"`
	CallSequence      uint64                   `json:"call_sequence"`
	Request           *chat.Request            `json:"request"`
	Response          *chat.Response           `json:"response,omitzero"`
	Outcome           interaction.ModelOutcome `json:"outcome"`
	Failure           string                   `json:"failure,omitempty"`
}

func (m ModelCall) Clone() ModelCall {
	m.Request = m.Request.Clone()
	m.Response = m.Response.Clone()
	return m
}

func (m ModelCall) Validate() error {
	if !m.attempt().valid() || m.StepSequence == 0 || m.CallSequence == 0 || m.Request == nil {
		return fmt.Errorf("%w: model call attribution is incomplete", ErrInvalidTrajectory)
	}
	if err := m.Request.Validate(); err != nil {
		return fmt.Errorf("%w: model request: %w", ErrInvalidTrajectory, err)
	}
	return m.validateSettlement()
}

func (m ModelCall) validateSettlement() error {
	if m.Outcome == interaction.ModelOutcomeUnobserved {
		if m.Response != nil || m.Failure != "" {
			return fmt.Errorf("%w: unobserved model call cannot carry settlement evidence", ErrInvalidTrajectory)
		}
		return nil
	}
	settlement := interaction.ModelSettlement{Outcome: m.Outcome, Response: m.Response, Failure: m.Failure}
	if err := settlement.Validate(); err != nil {
		return fmt.Errorf("%w: model settlement: %w", ErrInvalidTrajectory, err)
	}
	return nil
}

func (m ModelCall) attempt() attemptIdentity {
	return attemptIdentity{m.ProcessID, m.TreeIncarnationID, m.EffectID, m.AttemptID}
}

func (m ModelCall) toolDecisions() []chat.ToolCall {
	if m.Response == nil || m.Response.Output.Message == nil {
		return nil
	}
	var decisions []chat.ToolCall
	for _, part := range m.Response.Output.Message.Parts {
		if part.Kind == chat.PartToolCall {
			decisions = append(decisions, *part.ToolCall)
		}
	}
	return decisions
}

// ToolCall records a Tool boundary attributed to an Agent Process Step.
// Outcome distinguishes its retained settlement from a missing observation.
// The model call that requested it is the Tool child's ChildKey, recorded once
// in Events, so the record does not restate it.
type ToolCall struct {
	TreeIncarnationID agent.TreeIncarnationID `json:"tree_incarnation_id,omitzero"`
	EffectID          agent.EffectID          `json:"effect_id"`
	AttemptID         agent.EffectAttemptID   `json:"attempt_id"`
	ProcessID         agent.ProcessID         `json:"process_id"`
	StepSequence      uint64                  `json:"step_sequence"`
	Call              chat.ToolCall           `json:"call"`
	Result            *chat.ToolResult        `json:"result,omitzero"`
	InputRequired     bool                    `json:"input_required,omitzero"`
	Unknown           bool                    `json:"unknown,omitzero"`
	// Evidence is non-final output for Unknown. It is never a ToolResult.
	Evidence *chat.ToolOutput `json:"evidence,omitzero"`
	// Failure describes a failed call or diagnoses an unknown outcome without
	// claiming that the external operation definitely failed.
	Failure string `json:"failure,omitempty"`
}

// Outcome projects the retained settlement. Result alone owns its success or
// error classification; uncertainty is distinct from a missing observation.
// Competing settlement facts have an invalid outcome.
func (t ToolCall) Outcome() ToolOutcome {
	modes := 0
	for _, selected := range [...]bool{t.Result != nil, t.InputRequired, t.Unknown, t.Failure != "" && !t.Unknown} {
		if selected {
			modes++
		}
	}
	if modes > 1 {
		return ToolOutcomeInvalid
	}
	switch {
	case t.Result != nil:
		if t.Result.IsError {
			return ToolOutcomeError
		}
		return ToolOutcomeSucceeded
	case t.Unknown:
		return ToolOutcomeUnknown
	case t.InputRequired:
		return ToolOutcomeInputRequired
	case t.Failure != "":
		return ToolOutcomeFailed
	default:
		return ToolOutcomeUnobserved
	}
}

func (t ToolCall) Clone() ToolCall {
	if t.Evidence != nil {
		t.Evidence = new(t.Evidence.Clone())
	}
	if t.Result != nil {
		result := t.Result.Clone()
		t.Result = &result
	}
	return t
}

func (t ToolCall) Validate() error {
	if !t.attempt().valid() || t.StepSequence == 0 {
		return fmt.Errorf("%w: tool call attribution is incomplete", ErrInvalidTrajectory)
	}
	if err := t.Call.Validate(); err != nil {
		return fmt.Errorf("%w: tool call: %w", ErrInvalidTrajectory, err)
	}
	if _, err := canonicalArguments(t.Call.Arguments); err != nil {
		return fmt.Errorf("%w: tool call arguments: %w", ErrInvalidTrajectory, err)
	}
	if err := t.validateEvidence(); err != nil {
		return err
	}
	if err := t.validateSettlement(); err != nil {
		return err
	}
	return t.validateResult()
}

func (t ToolCall) validateEvidence() error {
	if t.Evidence == nil {
		return nil
	}
	if t.Outcome() != ToolOutcomeUnknown {
		return fmt.Errorf("%w: only unknown tool calls may retain non-final evidence", ErrInvalidTrajectory)
	}
	if err := t.Evidence.Validate(); err != nil {
		return fmt.Errorf("%w: tool evidence: %w", ErrInvalidTrajectory, err)
	}
	return nil
}

func (t ToolCall) validateSettlement() error {
	if !utf8.ValidString(t.Failure) {
		return fmt.Errorf("%w: tool failure must be valid UTF-8", ErrInvalidTrajectory)
	}
	if t.Outcome() == ToolOutcomeInvalid || t.Failure != strings.TrimSpace(t.Failure) {
		return fmt.Errorf("%w: tool call carries competing settlements or an invalid failure diagnostic", ErrInvalidTrajectory)
	}
	return nil
}

func (t ToolCall) validateResult() error {
	if t.Result == nil {
		return nil
	}
	if err := t.Result.Validate(); err != nil {
		return fmt.Errorf("%w: tool result: %w", ErrInvalidTrajectory, err)
	}
	if t.Result.ID != t.Call.ID || t.Result.Name != t.Call.Name {
		return fmt.Errorf("%w: tool result does not address its call", ErrInvalidTrajectory)
	}
	return nil
}

func (t ToolCall) attempt() attemptIdentity {
	return attemptIdentity{t.ProcessID, t.TreeIncarnationID, t.EffectID, t.AttemptID}
}

// attemptIdentity names one physical dispatcher attempt. EffectAttemptID alone
// is not trusted as a key: validation must detect one ID claimed by two owners.
type attemptIdentity struct {
	processID   agent.ProcessID
	incarnation agent.TreeIncarnationID
	effectID    agent.EffectID
	attemptID   agent.EffectAttemptID
}

func dispatcherAttempt(event agent.Event) (attemptIdentity, bool) {
	fact, ok := event.EffectStarted()
	if !ok || fact.Target() != agent.EffectTargetDispatcher {
		return attemptIdentity{}, false
	}
	effect, _ := event.EffectID()
	return attemptIdentity{event.Relation().ProcessID(), eventIncarnation(event), effect, fact.AttemptID()}, true
}

func (a attemptIdentity) valid() bool {
	return a.processID.Valid() && a.incarnation.Valid() && a.effectID.Valid() && a.attemptID.Valid()
}

func (a attemptIdentity) effect() EffectReference {
	return EffectReference{ProcessID: a.processID, TreeIncarnationID: a.incarnation, EffectID: a.effectID}
}
