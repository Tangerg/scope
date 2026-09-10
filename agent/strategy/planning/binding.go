package planning

import (
	"fmt"

	agent "github.com/Tangerg/scope/agent"
)

type bindingTarget uint8

const (
	bindingTargetInvalid bindingTarget = iota
	bindingTargetDispatcher
	bindingTargetChild
)

// ChildInputFunc derives a child Process input from the parent input and the
// current world state, so a plan step can be parameterized by facts discovered
// during execution rather than only by what the plan was started with.
type ChildInputFunc func(processInput agent.Input, worldState WorldState) (agent.Input, error)

// DispatcherBindingConfig binds a predictive Action to the Planning
// Dispatcher. RequiredCapabilities are enforced by Engine before dispatch.
type DispatcherBindingConfig struct {
	// Action is the immutable predictive behavior bound to a dispatcher target.
	Action Action
	// RequiredCapabilities is the authority required before dispatch.
	RequiredCapabilities []agent.Capability
}

// ChildBindingConfig binds a predictive Action to one exact child Deployment.
// Every attempt starts a new child Process with a stable Engine-derived
// identity, explicit budget, and attenuated capabilities.
type ChildBindingConfig struct {
	// Action is the immutable predictive behavior delegated to a child Process.
	Action Action
	// DeploymentRef identifies the exact child behavior binding.
	DeploymentRef agent.DeploymentRef
	// Input deterministically derives child input; nil reuses Process input.
	Input ChildInputFunc
	// Budget is permanently allocated to each child attempt.
	Budget agent.Budget
	// Capabilities is the attenuated authority granted to each child attempt.
	Capabilities agent.CapabilitySet
}

// ActionBinding is an immutable association between predictive Action
// semantics and exactly one external execution mechanism.
type ActionBinding struct {
	action     Action
	target     bindingTarget
	required   agent.CapabilitySet
	child      agent.ChildSpec
	childInput ChildInputFunc
}

// NewDispatcherBinding attaches an action to an external executor. Binding is
// separate from the action itself so the same predictive model can be planned
// against in tests without an executor present.
func NewDispatcherBinding(config DispatcherBindingConfig) (ActionBinding, error) {
	if !config.Action.Valid() {
		return ActionBinding{}, fmt.Errorf("%w: dispatcher binding Action", ErrInvalidAction)
	}
	required, err := agent.NewCapabilitySet(config.RequiredCapabilities...)
	if err != nil {
		return ActionBinding{}, fmt.Errorf("%w: required capabilities: %w", ErrInvalidAction, err)
	}
	return ActionBinding{action: config.Action, target: bindingTargetDispatcher, required: required}, nil
}

// NewChildBinding attaches an action to a child Deployment, so a plan step
// becomes a real Process with its own identity, budget, and recovery instead
// of an opaque call inside the parent.
func NewChildBinding(config ChildBindingConfig) (ActionBinding, error) {
	if !config.Action.Valid() || !config.DeploymentRef.Valid() || !config.Budget.Valid() || !config.Capabilities.Valid() {
		return ActionBinding{}, fmt.Errorf("%w: invalid child binding", ErrInvalidAction)
	}
	return ActionBinding{
		action: config.Action,
		target: bindingTargetChild,
		child: agent.ChildSpec{
			DeploymentRef: config.DeploymentRef, Budget: config.Budget, Capabilities: config.Capabilities,
		},
		childInput: config.Input,
	}, nil
}

// Action returns the immutable predictive Action owned by the binding.
func (a ActionBinding) Action() Action { return a.action }

func (a ActionBinding) Valid() bool {
	if !a.action.Valid() || !a.required.Valid() {
		return false
	}
	switch a.target {
	case bindingTargetDispatcher:
		return !a.child.DeploymentRef.Valid() && !a.child.Budget.Valid() &&
			a.childInput == nil
	case bindingTargetChild:
		return len(a.required.Values()) == 0 && a.child.DeploymentRef.Valid() &&
			a.child.Budget.Valid() && a.child.Capabilities.Valid()
	default:
		return false
	}
}

func (a ActionBinding) childSpec(key agent.ChildKey, input agent.Input) agent.ChildSpec {
	spec := a.child
	spec.Key = key
	spec.Input = input
	return spec
}
