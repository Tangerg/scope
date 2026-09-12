package planning

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/internal/childcall"
)

type phase string

const (
	phaseReadySense     phase = "ready_sense"
	phaseAwaitingSense  phase = "awaiting_sense"
	phaseAwaitingAction phase = "awaiting_action"
	phaseChild          phase = "child"
	phaseCompleted      phase = "completed"
)

func (p phase) valid() bool {
	switch p {
	case phaseReadySense, phaseAwaitingSense, phaseAwaitingAction,
		phaseChild, phaseCompleted:
		return true
	default:
		return false
	}
}

type executionState struct {
	Phase             phase             `json:"phase"`
	Input             json.RawMessage   `json:"input"`
	WorldState        WorldState        `json:"world_state"`
	PlanningPasses    uint32            `json:"planning_passes"`
	Attempts          []Attempt         `json:"attempts,omitempty"`
	CurrentActionName string            `json:"current_action_name,omitempty"`
	Child             *childcall.Single `json:"child,omitempty"`
}

func (e executionState) validate(definition *Definition) error {
	if !e.Phase.valid() || !definition.valid() {
		return ErrInvalidExecutionState
	}
	input, err := agent.ParseInput(e.Input)
	if err != nil {
		return fmt.Errorf("%w: Input: %w", ErrInvalidExecutionState, err)
	}
	if err := definition.descriptor.ValidateInput(input); err != nil {
		return fmt.Errorf("%w: input schema: %w", ErrInvalidExecutionState, err)
	}
	if err := e.validateAttemptFacts(definition); err != nil {
		return err
	}
	if err := e.validateCurrentAction(definition); err != nil {
		return err
	}
	if err := e.validateProgress(definition); err != nil {
		return err
	}
	return e.validatePhase()
}

func (e executionState) validateAttemptFacts(definition *Definition) error {
	if err := definition.validateActionHistory(e.Attempts); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidExecutionState, err)
	}
	if e.attemptCount() > uint64(definition.maxActionAttempts) {
		return ErrInvalidExecutionState
	}
	return nil
}

func (e executionState) attemptCount() uint64 {
	count := uint64(len(e.Attempts))
	if e.CurrentActionName != "" {
		count++
	}
	return count
}

func (e executionState) validateCurrentAction(definition *Definition) error {
	if e.CurrentActionName == "" {
		return nil
	}
	binding, found := definition.binding(e.CurrentActionName)
	if !found {
		return fmt.Errorf("%w: unknown current Action %q", ErrInvalidExecutionState, e.CurrentActionName)
	}
	if definition.actionExcluded(e.Attempts, e.CurrentActionName) {
		return fmt.Errorf("%w: current Action is excluded", ErrInvalidExecutionState)
	}
	if e.Phase == phaseAwaitingAction && binding.target != bindingTargetDispatcher ||
		e.Phase == phaseChild && binding.target != bindingTargetChild {
		return fmt.Errorf("%w: current Action does not match the execution phase", ErrInvalidExecutionState)
	}
	return nil
}

func (e executionState) validateProgress(definition *Definition) error {
	if e.Phase == phaseCompleted {
		if err := e.output(definition).Validate(); err != nil {
			return fmt.Errorf("%w: completion: %w", ErrInvalidExecutionState, err)
		}
		return nil
	}
	attempts := uint64(len(e.Attempts))
	passes := uint64(e.PlanningPasses)
	switch e.Phase {
	case phaseReadySense:
		if attempts != 0 || passes != 0 {
			return ErrInvalidExecutionState
		}
	case phaseAwaitingSense:
		if e.awaitingConfirmation() && passes != attempts+1 ||
			!e.awaitingConfirmation() && passes != attempts {
			return fmt.Errorf("%w: sensing phase counters are inconsistent", ErrInvalidExecutionState)
		}
	case phaseAwaitingAction, phaseChild:
		if passes != attempts+1 {
			return fmt.Errorf("%w: active Action counters are inconsistent", ErrInvalidExecutionState)
		}
	}
	return nil
}

func (e executionState) validatePhase() error {
	if (e.Child != nil) != (e.Phase == phaseChild) {
		return ErrInvalidExecutionState
	}
	hasAction := e.CurrentActionName != ""
	switch e.Phase {
	case phaseReadySense, phaseCompleted:
		if hasAction {
			return ErrInvalidExecutionState
		}
	case phaseAwaitingAction, phaseChild:
		if !hasAction {
			return ErrInvalidExecutionState
		}
	}
	return nil
}

func (e executionState) awaitingConfirmation() bool {
	return e.Phase == phaseAwaitingSense && e.CurrentActionName != ""
}

func (e *executionState) confirmAction(action Action) {
	attempt := Attempt{ActionName: e.CurrentActionName, Status: AttemptSucceeded}
	if !e.WorldState.Satisfies(action.effects...) {
		attempt.Status = AttemptUnconfirmed
		attempt.Diagnostic = "Reobservation did not establish the Action's predicted effects"
	}
	e.Attempts = append(e.Attempts, attempt)
	e.CurrentActionName = ""
}

func (e *executionState) recordFailedAction(reason string) {
	e.Attempts = append(e.Attempts, Attempt{
		ActionName: e.CurrentActionName, Status: AttemptFailed, Diagnostic: diagnostic(reason),
	})
	e.CurrentActionName = ""
}

func (e *executionState) complete(definition *Definition) (Output, error) {
	candidate := *e
	candidate.Phase = phaseCompleted
	candidate.CurrentActionName = ""
	candidate.Child = nil
	if err := candidate.validate(definition); err != nil {
		return Output{}, err
	}
	output := candidate.output(definition)
	output.Attempts = slices.Clone(output.Attempts)
	if output.Attempts == nil {
		output.Attempts = []Attempt{}
	}
	*e = candidate
	return output, nil
}

func (e executionState) output(definition *Definition) Output {
	outcome := OutcomeStuck
	switch {
	case definition.goal.SatisfiedBy(e.WorldState):
		outcome = OutcomeAchieved
	case len(e.Attempts) == 0:
		outcome = OutcomeUnreachable
	}
	return Output{
		Outcome: outcome, WorldState: e.WorldState,
		Attempts: e.Attempts, PlanningPasses: e.PlanningPasses,
	}
}

func (e executionState) input() (agent.Input, error) {
	return agent.ParseInput(bytes.Clone(e.Input))
}

func (e executionState) snapshot() (agent.ExecutionState, error) {
	payload, err := json.Marshal(e)
	if err != nil {
		return agent.ExecutionState{}, fmt.Errorf("planning: encode execution state: %w", err)
	}
	return agent.NewExecutionState(executionStateKind, payload)
}

func diagnostic(value string) string {
	value = strings.ToValidUTF8(value, "�")
	value = strings.TrimSpace(value)
	if value == "" {
		return "external operation failed"
	}
	if len(value) <= maxDescriptionBytes {
		return value
	}
	value = value[:maxDescriptionBytes]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "external operation failed"
	}
	return value
}

func validDiagnostic(value string) bool {
	return value != "" && utf8.ValidString(value) && strings.TrimSpace(value) == value &&
		len(value) <= maxDescriptionBytes
}
