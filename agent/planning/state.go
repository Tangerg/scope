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
	Phase                     phase            `json:"phase"`
	Input                     json.RawMessage  `json:"input"`
	WorldState                WorldState       `json:"world_state"`
	PlanningPasses            uint32           `json:"planning_passes"`
	Attempts                  []Attempt        `json:"attempts,omitempty"`
	ExcludedActionNames       []string         `json:"excluded_action_names,omitempty"`
	CurrentActionName         string           `json:"current_action_name,omitempty"`
	ActionConfirmationPending bool             `json:"action_confirmation_pending,omitempty"`
	ChildKey                  *agent.ChildKey  `json:"child_key,omitempty"`
	ChildProcessID            *agent.ProcessID `json:"child_process_id,omitempty"`
	WaitID                    *agent.WaitID    `json:"wait_id,omitempty"`
	FinalOutput               *Output          `json:"final_output,omitempty"`
}

type phaseField uint8

const (
	phaseFieldAbsent phaseField = iota
	phaseFieldOptional
	phaseFieldRequired
)

type phaseShape struct {
	action                   phaseField
	confirmationTracksAction bool
	childKey                 phaseField
	childProcessID           phaseField
	waitID                   phaseField
	initial                  bool
}

func (p phase) shape() (phaseShape, bool) {
	switch p {
	case phaseReadySense:
		return phaseShape{initial: true}, true
	case phaseAwaitingSense:
		return phaseShape{action: phaseFieldOptional, confirmationTracksAction: true}, true
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
	if err := e.validateProgress(); err != nil {
		return err
	}
	if err := e.validatePhase(); err != nil {
		return err
	}
	return e.validateCompletion(definition)
}

func (e executionState) validateAttemptFacts(definition *Definition) error {
	previouslyExcluded := make(map[string]struct{})
	for index, attempt := range e.Attempts {
		if err := attempt.Validate(); err != nil {
			return fmt.Errorf("%w: attempt %d: %w", ErrInvalidExecutionState, index, err)
		}
		if _, found := definition.binding(attempt.ActionName); !found {
			return fmt.Errorf("%w: attempt references unknown Action %q", ErrInvalidExecutionState, attempt.ActionName)
		}
		if _, excluded := previouslyExcluded[attempt.ActionName]; excluded {
			return fmt.Errorf("%w: Action %q was attempted after exclusion", ErrInvalidExecutionState, attempt.ActionName)
		}
		if attempt.Status != AttemptSucceeded {
			previouslyExcluded[attempt.ActionName] = struct{}{}
		}
	}
	if uint64(len(e.Attempts)) > uint64(definition.maxActionAttempts) ||
		!validExcludedActionNames(e.ExcludedActionNames, definition) {
		return ErrInvalidExecutionState
	}
	wantExcluded := make([]string, 0, len(e.Attempts))
	for _, attempt := range e.Attempts {
		if attempt.Status != AttemptSucceeded {
			wantExcluded = append(wantExcluded, attempt.ActionName)
		}
	}
	slices.Sort(wantExcluded)
	wantExcluded = slices.Compact(wantExcluded)
	if !slices.Equal(e.ExcludedActionNames, wantExcluded) {
		return fmt.Errorf("%w: excluded Actions do not match attempt facts", ErrInvalidExecutionState)
	}
	return nil
}

func (e executionState) validateCurrentAction(definition *Definition) error {
	if e.CurrentActionName == "" {
		return nil
	}
	binding, found := definition.binding(e.CurrentActionName)
	if !found {
		return fmt.Errorf("%w: unknown current Action %q", ErrInvalidExecutionState, e.CurrentActionName)
	}
	if slices.Contains(e.ExcludedActionNames, e.CurrentActionName) {
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

func (e executionState) validateCompletion(definition *Definition) error {
	if e.Phase == phaseCompleted {
		if e.FinalOutput == nil || e.FinalOutput.Validate() != nil ||
			e.FinalOutput.WorldState.Key() != e.WorldState.Key() ||
			e.FinalOutput.PlanningPasses != e.PlanningPasses ||
			!slices.Equal(e.FinalOutput.Attempts, e.Attempts) ||
			e.FinalOutput.Outcome == OutcomeAchieved && !definition.goal.SatisfiedBy(e.WorldState) {
			return ErrInvalidExecutionState
		}
	} else if e.FinalOutput != nil {
		return ErrInvalidExecutionState
	}
	return nil
}

func (e executionState) validateProgress() error {
	attempts := uint64(len(e.Attempts))
	passes := uint64(e.PlanningPasses)
	switch e.Phase {
	case phaseReadySense:
		if attempts != 0 || passes != 0 {
			return ErrInvalidExecutionState
		}
	case phaseAwaitingSense:
		if e.ActionConfirmationPending && passes != attempts+1 ||
			!e.ActionConfirmationPending && (attempts == 0 && passes != 0 || attempts > 0 && passes != attempts) {
			return fmt.Errorf("%w: sensing phase counters are inconsistent", ErrInvalidExecutionState)
		}
	case phaseAwaitingAction, phaseAwaitingChildStart, phaseAwaitingChildWaitOpen, phaseWaitingChild:
		if passes != attempts+1 {
			return fmt.Errorf("%w: active Action counters are inconsistent", ErrInvalidExecutionState)
		}
	case phaseCompleted:
		if e.FinalOutput == nil {
			return ErrInvalidExecutionState
		}
		switch e.FinalOutput.Outcome {
		case OutcomeAchieved:
			if passes != attempts {
				return fmt.Errorf("%w: achieved counters are inconsistent", ErrInvalidExecutionState)
			}
		case OutcomeUnreachable:
			if attempts != 0 || passes != 1 {
				return ErrInvalidExecutionState
			}
		case OutcomeStuck:
			if attempts == 0 || passes != attempts && passes != attempts+1 {
				return fmt.Errorf("%w: stuck counters are inconsistent", ErrInvalidExecutionState)
			}
		}
	}
	return nil
}

func (e executionState) validatePhase() error {
	shape, found := e.Phase.shape()
	hasAction := e.CurrentActionName != ""
	if !found || !shape.action.matches(hasAction, true) ||
		e.ActionConfirmationPending != (shape.confirmationTracksAction && hasAction) ||
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

func validExcludedActionNames(actionNames []string, definition *Definition) bool {
	for index, name := range actionNames {
		if !validName(name) || index > 0 && actionNames[index-1] >= name {
			return false
		}
		if _, found := definition.binding(name); !found {
			return false
		}
	}
	return true
}

func (e *executionState) excludeAction(name string) {
	index, found := slices.BinarySearch(e.ExcludedActionNames, name)
	if found {
		return
	}
	e.ExcludedActionNames = slices.Insert(e.ExcludedActionNames, index, name)
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
