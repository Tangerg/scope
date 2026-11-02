package planning

import (
	"fmt"

	"github.com/Tangerg/scope/agent/run"
)

// ChildInputFunc derives a child Process input from the parent input and the
// WorldState observed when the Action is selected.
type ChildInputFunc func(processInput run.Payload, worldState WorldState) (run.Payload, error)

// DispatcherBindingConfig binds a predictive Action to the Planning
// Dispatcher. RequiredCapabilities are enforced by Runtime before dispatch.
type DispatcherBindingConfig struct {
	Action               Action
	RequiredCapabilities []run.Capability
}

// ChildBindingConfig binds a predictive Action to one exact child Program.
// Every attempt starts a new child Process with a stable Runtime-derived
// identity, explicit budget, and attenuated capabilities.
type ChildBindingConfig struct {
	Action Action
	// Program is the exact child program; the planning Definition owns it
	// as one of its ChildPrograms.
	Program run.Program[run.Payload, run.Payload]
	// Input deterministically derives child input; nil reuses Process input.
	Input ChildInputFunc
	// Budget is permanently allocated to each child attempt.
	Budget       run.Budget
	Capabilities run.CapabilitySet
}

// ActionBinding is an immutable association between predictive Action
// semantics and exactly one external execution mechanism: a child Program
// when one is bound, and the planning Dispatcher otherwise.
type ActionBinding struct {
	action            Action
	required          run.CapabilitySet
	childDeployment   run.Program[run.Payload, run.Payload]
	childBudget       run.Budget
	childCapabilities run.CapabilitySet
	childInput        ChildInputFunc
}

func NewDispatcherBinding(config DispatcherBindingConfig) (ActionBinding, error) {
	if !config.Action.Valid() {
		return ActionBinding{}, fmt.Errorf("%w: dispatcher binding Action", ErrInvalidAction)
	}
	required, err := run.NewCapabilitySet(config.RequiredCapabilities...)
	if err != nil {
		return ActionBinding{}, fmt.Errorf("%w: required capabilities: %w", ErrInvalidAction, err)
	}
	return ActionBinding{action: config.Action, required: required}, nil
}

func NewChildBinding(config ChildBindingConfig) (ActionBinding, error) {
	if !config.Action.Valid() || !config.Program.Valid() {
		return ActionBinding{}, fmt.Errorf("%w: invalid child binding", ErrInvalidAction)
	}
	return ActionBinding{
		action:          config.Action,
		childDeployment: config.Program, childBudget: config.Budget, childCapabilities: config.Capabilities,
		childInput: config.Input,
	}, nil
}

func (a ActionBinding) Action() Action { return a.action }

func (a ActionBinding) delegatesToChild() bool { return a.childDeployment.Valid() }

func (a ActionBinding) Valid() bool {
	if !a.action.Valid() {
		return false
	}
	if a.delegatesToChild() {
		return len(a.required.Values()) == 0
	}
	return a.childInput == nil
}

func (a ActionBinding) childSpec(key run.ChildKey, input run.Payload) run.ChildSpec {
	return run.ChildSpec{
		Key: key, ProgramRef: a.childDeployment.ProgramRef(), Input: input,
		Budget: a.childBudget, Capabilities: a.childCapabilities,
	}
}
