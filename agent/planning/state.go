package planning

import (
	"context"
	"fmt"

	"github.com/Tangerg/scope/agent/internal/strategy/child"
	"github.com/Tangerg/scope/agent/run"
)

// executionState awaits at most one operation. Awaiting none, it has yet to
// request its initial observation; otherwise the awaited variant alone decides
// what the next Step accepts, so no separate phase can disagree with it.
type executionState struct {
	Input      run.Payload    `json:"input" jsonwire:"required"`
	WorldState WorldState     `json:"world_state" jsonwire:"required"`
	Attempts   []Attempt      `json:"attempts,omitempty"`
	Sense      *awaitedSense  `json:"sense,omitzero"`
	Action     *awaitedAction `json:"action,omitzero"`
}

// awaitedSense is an observation in flight. Confirms names the completed
// Action whose effects it checks; it is empty for the initial observation and
// after a failed attempt.
type awaitedSense struct {
	Effect   run.EffectID `json:"effect"`
	Confirms string       `json:"confirms,omitempty"`
}

// awaitedAction is an Action in flight. Its binding decides whether it runs as
// a dispatcher Effect or as a child Process, and exactly that one is recorded.
type awaitedAction struct {
	Name   string            `json:"name"`
	Effect run.EffectID      `json:"effect,omitzero"`
	Child  *child.Invocation `json:"child,omitzero"`
}

// currentAction names the attempt still unrecorded: an Action in flight or one
// awaiting confirmation.
func (e executionState) currentAction() string {
	switch {
	case e.Action != nil:
		return e.Action.Name
	case e.Sense != nil:
		return e.Sense.Confirms
	default:
		return ""
	}
}

func (e executionState) validate(ctx context.Context, definition *definition) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := definition.descriptor.ValidateInput(e.Input); err != nil {
		return fmt.Errorf("%w: input schema: %w", ErrInvalidExecutionState, err)
	}
	if err := e.validateAttemptHistory(ctx, definition); err != nil {
		return err
	}
	if err := e.validateAwaited(definition); err != nil {
		return err
	}
	return e.validateCurrentAction(definition)
}

func (e executionState) validateAwaited(definition *definition) error {
	switch {
	case e.Sense != nil && e.Action != nil:
		return fmt.Errorf("%w: an observation and an Action are awaited at once", ErrInvalidExecutionState)
	case e.Sense != nil:
		if !e.Sense.Effect.Valid() {
			return fmt.Errorf("%w: observation Effect is invalid", ErrInvalidExecutionState)
		}
	case e.Action != nil:
		return e.Action.validate(definition, uint64(len(e.Attempts))+1)
	case len(e.Attempts) != 0:
		return fmt.Errorf("%w: only the initial observation awaits nothing", ErrInvalidExecutionState)
	}
	return nil
}

func (a awaitedAction) validate(definition *definition, attempt uint64) error {
	binding, found := definition.binding(a.Name)
	if !found {
		return fmt.Errorf("%w: unknown current Action %q", ErrInvalidExecutionState, a.Name)
	}
	if !binding.delegatesToChild() {
		if !a.Effect.Valid() || a.Child != nil {
			return fmt.Errorf("%w: dispatcher Action requires exactly its Effect", ErrInvalidExecutionState)
		}
		return nil
	}
	if a.Effect.Valid() || a.Child == nil || a.Child.Done() {
		return fmt.Errorf("%w: child Action requires exactly its running child", ErrInvalidExecutionState)
	}
	if _, err := (child.Batch{*a.Child}).Parent(func(int) (run.ChildKey, error) { return planningChildKey(a.Name, attempt) }); err != nil {
		return fmt.Errorf("%w: child: %w", ErrInvalidExecutionState, err)
	}
	return nil
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
	if e.currentAction() != "" {
		count++
	}
	return count
}

func (e executionState) validateCurrentAction(definition *definition) error {
	name := e.currentAction()
	if name == "" {
		return nil
	}
	binding, found := definition.binding(name)
	if !found {
		return fmt.Errorf("%w: unknown current Action %q", ErrInvalidExecutionState, name)
	}
	if e.actionExcluded(name) {
		return fmt.Errorf("%w: current Action is excluded", ErrInvalidExecutionState)
	}
	if !binding.action.Applicable(e.WorldState) {
		return fmt.Errorf("%w: current Action was not applicable to its observed WorldState", ErrInvalidExecutionState)
	}
	return nil
}

// confirmAction records a completed Action as succeeded only when the observed
// WorldState satisfies its declared effects.
func (e *executionState) confirmAction(name string, action Action) {
	attempt := Attempt{ActionName: run.QualifiedName(name), Status: AttemptSucceeded}
	if !e.WorldState.Satisfies(action.effects...) {
		attempt.Status = AttemptUnconfirmed
	}
	e.Attempts = append(e.Attempts, attempt)
}

func (e *executionState) recordFailedAction(name, reason string) {
	e.Attempts = append(e.Attempts, Attempt{
		ActionName: run.QualifiedName(name), Status: AttemptFailed, Diagnostic: run.NormalizeDiagnostic(reason),
	})
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
