package planning

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/Tangerg/scope/agent/internal/strategy/child"
	"github.com/Tangerg/scope/agent/run"
)

type phase string

const (
	phaseReadySense     phase = "ready_sense"
	phaseAwaitingSense  phase = "awaiting_sense"
	phaseAwaitingAction phase = "awaiting_action"
)

func (p phase) valid() bool {
	switch p {
	case phaseReadySense, phaseAwaitingSense, phaseAwaitingAction:
		return true
	default:
		return false
	}
}

type executionState struct {
	Phase             phase             `json:"phase"`
	Input             json.RawMessage   `json:"input"`
	WorldState        WorldState        `json:"world_state" jsonwire:"required"`
	Attempts          []Attempt         `json:"attempts,omitempty"`
	CurrentActionName string            `json:"current_action_name,omitempty"`
	Child             *child.Invocation `json:"child,omitzero"`
	PendingEffect     *run.EffectID     `json:"pending_effect,omitzero"`
}

// awaitsChild reports whether the awaited Action runs as a child Process
// rather than a dispatcher Effect; the Action's binding owns that choice.
func (e executionState) awaitsChild(definition *definition) bool {
	binding, found := definition.binding(e.CurrentActionName)
	return e.Phase == phaseAwaitingAction && found && binding.delegatesToChild()
}

func (e executionState) validate(ctx context.Context, definition *definition) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !definition.valid() {
		return fmt.Errorf("%w: valid definition is required", ErrInvalidExecutionState)
	}
	if !e.Phase.valid() {
		return fmt.Errorf("%w: unknown phase %q", ErrInvalidExecutionState, e.Phase)
	}
	input, err := run.ParsePayload(e.Input)
	if err != nil {
		return fmt.Errorf("%w: Input: %w", ErrInvalidExecutionState, err)
	}
	if err := definition.descriptor.ValidateInput(input); err != nil {
		return fmt.Errorf("%w: input schema: %w", ErrInvalidExecutionState, err)
	}
	if err := e.validateAttemptHistory(ctx, definition); err != nil {
		return err
	}
	if err := e.validateCurrentAction(definition); err != nil {
		return err
	}
	if err := e.validateProgress(); err != nil {
		return err
	}
	return e.validatePhase()
}

func (e executionState) validateAttemptHistory(ctx context.Context, definition *definition) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := definition.validateActionHistory(ctx, e.Attempts); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidExecutionState, err)
	}
	if !definition.maxActionAttempts.Allows(e.attemptCount()) {
		return fmt.Errorf("%w: action attempt count exceeds configured limit", ErrInvalidExecutionState)
	}
	return ctx.Err()
}

// needsPlan reports whether the goal is unmet and another Action attempt is
// still admitted, so the Planner must be asked for one.
func (e executionState) needsPlan(definition *definition) bool {
	return !definition.goal.SatisfiedBy(e.WorldState) && definition.maxActionAttempts.Allows(e.attemptCount(), 1)
}

func (e executionState) attemptCount() uint64 {
	count := uint64(len(e.Attempts))
	if e.CurrentActionName != "" {
		count++
	}
	return count
}

func (e executionState) validateCurrentAction(definition *definition) error {
	if e.CurrentActionName == "" {
		return nil
	}
	binding, found := definition.binding(e.CurrentActionName)
	if !found {
		return fmt.Errorf("%w: unknown current Action %q", ErrInvalidExecutionState, e.CurrentActionName)
	}
	if e.actionExcluded(e.CurrentActionName) {
		return fmt.Errorf("%w: current Action is excluded", ErrInvalidExecutionState)
	}
	if !binding.action.Applicable(e.WorldState) {
		return fmt.Errorf("%w: current Action was not applicable to its observed WorldState", ErrInvalidExecutionState)
	}
	if binding.delegatesToChild() && e.Phase == phaseAwaitingAction && (e.Child == nil || !e.Child.Valid()) {
		return ErrInvalidExecutionState
	}
	if e.Child != nil {
		if e.Child.Done() {
			return ErrInvalidExecutionState
		}
		attempt := uint64(len(e.Attempts)) + 1
		if _, err := (child.Batch{*e.Child}).Parent(func(int) (run.ChildKey, error) { return planningChildKey(e.CurrentActionName, attempt) }); err != nil {
			return fmt.Errorf("%w: child: %w", ErrInvalidExecutionState, err)
		}
	}
	if !binding.delegatesToChild() && e.Child != nil {
		return fmt.Errorf("%w: dispatcher Action retains child progress", ErrInvalidExecutionState)
	}
	return nil
}

func (e executionState) validateProgress() error {
	if e.Phase == phaseReadySense && len(e.Attempts) != 0 {
		return fmt.Errorf("%w: ready_sense requires zero attempts", ErrInvalidExecutionState)
	}
	return nil
}

func (e executionState) validatePhase() error {
	pending := e.Phase == phaseAwaitingSense || e.Phase == phaseAwaitingAction && e.Child == nil
	if pending != (e.PendingEffect != nil) || e.PendingEffect != nil && !e.PendingEffect.Valid() {
		return ErrInvalidExecutionState
	}
	if e.Child != nil && e.Phase != phaseAwaitingAction {
		return fmt.Errorf("%w: child state disagrees with execution phase", ErrInvalidExecutionState)
	}
	hasAction := e.CurrentActionName != ""
	switch e.Phase {
	case phaseReadySense:
		if hasAction {
			return fmt.Errorf("%w: phase %q cannot have a current Action", ErrInvalidExecutionState, e.Phase)
		}
	case phaseAwaitingAction:
		if !hasAction {
			return fmt.Errorf("%w: phase %q requires a current Action", ErrInvalidExecutionState, e.Phase)
		}
	}
	return nil
}

func (e executionState) awaitingConfirmation() bool {
	return e.Phase == phaseAwaitingSense && e.CurrentActionName != ""
}

func (e *executionState) confirmAction(action Action) {
	attempt := Attempt{ActionName: run.QualifiedName(e.CurrentActionName), Status: AttemptSucceeded}
	if !e.WorldState.Satisfies(action.effects...) {
		attempt.Status = AttemptUnconfirmed
	}
	e.Attempts = append(e.Attempts, attempt)
	e.CurrentActionName = ""
}

func (e *executionState) recordFailedAction(reason string) {
	e.Attempts = append(e.Attempts, Attempt{
		ActionName: run.QualifiedName(e.CurrentActionName), Status: AttemptFailed, Diagnostic: run.NormalizeDiagnostic(reason),
	})
	e.CurrentActionName = ""
}

func (e *executionState) complete(ctx context.Context, definition *definition) (Output, error) {
	if err := e.validate(ctx, definition); err != nil {
		return Output{}, err
	}
	output := e.output(definition)
	if err := output.Validate(); err != nil {
		return Output{}, fmt.Errorf("%w: completion: %w", ErrInvalidExecutionState, err)
	}
	output.Attempts = slices.Clone(output.Attempts)
	if output.Attempts == nil {
		output.Attempts = []Attempt{}
	}
	return output, nil
}

// output classifies completion: a completion that still needed a plan
// followed a planning pass that found none, otherwise the attempt limit
// admitted no further pass.
func (e executionState) output(definition *definition) Output {
	outcome := OutcomeExhausted
	switch {
	case definition.goal.SatisfiedBy(e.WorldState):
		outcome = OutcomeAchieved
	case len(e.Attempts) == 0:
		outcome = OutcomeUnreachable
	case e.needsPlan(definition):
		outcome = OutcomeStuck
	}
	return Output{Outcome: outcome, WorldState: e.WorldState, Attempts: e.Attempts}
}

func (e executionState) input() (run.Payload, error) {
	// ParsePayload normalizes into an independently owned copy, so the raw Input
	// needs no separate clone here.
	return run.ParsePayload(e.Input)
}

func (e executionState) snapshot() (run.ExecutionState, error) {
	return run.EncodeExecutionState(executionStateKind, e)
}

func (e executionState) actionExcluded(name string) bool {
	for _, attempt := range e.Attempts {
		if string(attempt.ActionName) == name && attempt.excluded() {
			return true
		}
	}
	return false
}
