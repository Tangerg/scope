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
type ChildInputFunc func(processInput agent.Payload, worldState WorldState) (agent.Payload, error)

// DispatcherBindingConfig binds a predictive Action to the Planning
// Dispatcher. RequiredCapabilities are enforced by Engine before dispatch.
type DispatcherBindingConfig struct {
	Action               Action
	RequiredCapabilities []agent.Capability
}

// ChildBindingConfig binds a predictive Action to one exact child Deployment.
// Every attempt starts a new child Process with a stable Engine-derived
// identity, explicit budget, and attenuated capabilities.
type ChildBindingConfig struct {
	Action        Action
	DeploymentRef agent.DeploymentRef
	// Input deterministically derives child input; nil reuses Process input.
	Input ChildInputFunc
	// Budget is permanently allocated to each child attempt.
	Budget       agent.Budget
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

func NewChildBinding(config ChildBindingConfig) (ActionBinding, error) {
	if !config.Action.Valid() || !config.DeploymentRef.Valid() || !config.Capabilities.Valid() {
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

func (a ActionBinding) Action() Action { return a.action }

func (a ActionBinding) Valid() bool {
	if !a.action.Valid() || !a.required.Valid() {
		return false
	}
	switch a.target {
	case bindingTargetDispatcher:
		return !a.child.DeploymentRef.Valid() &&
			a.childInput == nil
	case bindingTargetChild:
		return len(a.required.Values()) == 0 && a.child.DeploymentRef.Valid() &&
			a.child.Capabilities.Valid()
	default:
		return false
	}
}

func (a ActionBinding) childSpec(key agent.ChildKey, input agent.Payload) agent.ChildSpec {
	spec := a.child
	spec.Key = key
	spec.Input = input
	return spec
}
