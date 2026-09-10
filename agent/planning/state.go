package planning

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	agent "github.com/Tangerg/scope/agent"
)

type phase string

const (
	phaseReadySense            phase = "ready_sense"
	phaseAwaitingSense         phase = "awaiting_sense"
	phaseAwaitingAction        phase = "awaiting_action"
	phaseAwaitingChildStart    phase = "awaiting_child_start"
	phaseAwaitingChildWaitOpen phase = "awaiting_child_wait_open"
	phaseWaitingChild          phase = "waiting_child"
	phaseCompleted             phase = "completed"
)

func (p phase) valid() bool {
	switch p {
	case phaseReadySense, phaseAwaitingSense, phaseAwaitingAction,
		phaseAwaitingChildStart, phaseAwaitingChildWaitOpen, phaseWaitingChild, phaseCompleted:
		return true
	default:
		return false
	}
}

type executionState struct {
	Phase             phase            `json:"phase"`
	Input             json.RawMessage  `json:"input"`
	WorldState        WorldState       `json:"world_state"`
	PlanningPasses    uint32           `json:"planning_passes"`
	Attempts          []Attempt        `json:"attempts,omitempty"`
	CurrentActionName string           `json:"current_action_name,omitempty"`
	ChildKey          *agent.ChildKey  `json:"child_key,omitempty"`
	ChildProcessID    *agent.ProcessID `json:"child_process_id,omitempty"`
	WaitID            *agent.WaitID    `json:"wait_id,omitempty"`
}

type phaseField uint8

const (
	phaseFieldAbsent phaseField = iota
	phaseFieldOptional
	phaseFieldRequired
)

type phaseShape struct {
	action         phaseField
	childKey       phaseField
	childProcessID phaseField
	waitID         phaseField
	initial        bool
}

func (p phase) shape() (phaseShape, bool) {
	switch p {
	case phaseReadySense:
		return phaseShape{initial: true}, true
	case phaseAwaitingSense:
		return phaseShape{action: phaseFieldOptional}, true
	case phaseAwaitingAction:
		return phaseShape{action: phaseFieldRequired}, true
	case phaseAwaitingChildStart:
		return phaseShape{action: phaseFieldRequired, childKey: phaseFieldRequired}, true
	case phaseAwaitingChildWaitOpen:
		return phaseShape{
			action: phaseFieldRequired, childKey: phaseFieldRequired,
			childProcessID: phaseFieldRequired,
		}, true
	case phaseWaitingChild:
		return phaseShape{
			action: phaseFieldRequired, childKey: phaseFieldRequired,
			childProcessID: phaseFieldRequired, waitID: phaseFieldRequired,
		}, true
	case phaseCompleted:
		return phaseShape{}, true
	default:
		return phaseShape{}, false
	}
}

func (e executionState) validate(definition *Definition) error {
	if !e.Phase.valid() || !definition.valid() || !e.WorldState.Valid() {
		return ErrInvalidExecutionState
	}
	input, err := agent.ParseInput(e.Input)
	if err != nil || definition.descriptor.ValidateInput(input) != nil {
		return fmt.Errorf("%w: Input", ErrInvalidExecutionState)
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
	for _, attempt := range e.Attempts {
		if _, found := definition.binding(attempt.ActionName); !found {
			return fmt.Errorf("%w: attempt references unknown Action %q", ErrInvalidExecutionState, attempt.ActionName)
		}
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
	if e.actionExcluded(e.CurrentActionName) {
		return fmt.Errorf("%w: current Action is excluded", ErrInvalidExecutionState)
	}
	if e.Phase == phaseAwaitingAction && binding.target != bindingTargetDispatcher ||
		(e.Phase == phaseAwaitingChildStart || e.Phase == phaseAwaitingChildWaitOpen ||
			e.Phase == phaseWaitingChild) && binding.target != bindingTargetChild {
		return fmt.Errorf("%w: current Action does not match the execution phase", ErrInvalidExecutionState)
	}
	if binding.target != bindingTargetChild || e.ChildKey == nil {
		return nil
	}
	wantKey, err := planningChildKey(e.CurrentActionName, uint32(len(e.Attempts)+1))
	if err != nil || *e.ChildKey != wantKey {
		return fmt.Errorf("%w: child key does not match the Action attempt", ErrInvalidExecutionState)
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
	if err := validateAttempts(e.Attempts); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidExecutionState, err)
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
	case phaseAwaitingAction, phaseAwaitingChildStart, phaseAwaitingChildWaitOpen, phaseWaitingChild:
		if passes != attempts+1 {
			return fmt.Errorf("%w: active Action counters are inconsistent", ErrInvalidExecutionState)
		}
	}
	return nil
}

func (e executionState) validatePhase() error {
	shape, found := e.Phase.shape()
	hasAction := e.CurrentActionName != ""
	if !found || !shape.action.matches(hasAction, true) ||
		!shape.childKey.matches(e.ChildKey != nil, e.ChildKey != nil && e.ChildKey.Valid()) ||
		!shape.childProcessID.matches(
			e.ChildProcessID != nil,
			e.ChildProcessID != nil && e.ChildProcessID.Valid(),
		) ||
		!shape.waitID.matches(e.WaitID != nil, e.WaitID != nil && e.WaitID.Valid()) ||
		shape.initial && (e.PlanningPasses != 0 || len(e.Attempts) != 0) {
		return ErrInvalidExecutionState
	}
	return nil
}

func (p phaseField) matches(present bool, valid bool) bool {
	switch p {
	case phaseFieldAbsent:
		return !present
	case phaseFieldOptional:
		return !present || valid
	case phaseFieldRequired:
		return present && valid
	default:
		return false
	}
}

func (e executionState) awaitingConfirmation() bool {
	return e.Phase == phaseAwaitingSense && e.CurrentActionName != ""
}

func (e executionState) actionExcluded(name string) bool {
	for _, attempt := range e.Attempts {
		if attempt.ActionName == name && attempt.Status != AttemptSucceeded {
			return true
		}
	}
	return false
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

func (e *executionState) clearChild() {
	e.ChildKey = nil
	e.ChildProcessID = nil
	e.WaitID = nil
}

func (e *executionState) complete(definition *Definition) (Output, error) {
	candidate := *e
	candidate.Phase = phaseCompleted
	candidate.CurrentActionName = ""
	candidate.clearChild()
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
