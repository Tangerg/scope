package planning

import (
	"fmt"

	agent "github.com/Tangerg/scope/agent"
)

// ChildInputFunc derives a child Process input from the parent input and the
// WorldState observed when the Action is selected.
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
	Action Action
	// Deployment is the exact child behavior; the planning Definition owns it
	// as one of its ChildDeployments.
	Deployment agent.Deployment
	// Input deterministically derives child input; nil reuses Process input.
	Input ChildInputFunc
	// Budget is permanently allocated to each child attempt.
	Budget       agent.Budget
	Capabilities agent.CapabilitySet
}

// ActionBinding is an immutable association between predictive Action
// semantics and exactly one external execution mechanism: a child Deployment
// when one is bound, and the planning Dispatcher otherwise.
type ActionBinding struct {
	action            Action
	required          agent.CapabilitySet
	childDeployment   agent.Deployment
	childBudget       agent.Budget
	childCapabilities agent.CapabilitySet
	childInput        ChildInputFunc
}

func NewDispatcherBinding(config DispatcherBindingConfig) (ActionBinding, error) {
	if !config.Action.Valid() {
		return ActionBinding{}, fmt.Errorf("%w: dispatcher binding Action", ErrInvalidAction)
	}
	required, err := agent.NewCapabilitySet(config.RequiredCapabilities...)
	if err != nil {
		return ActionBinding{}, fmt.Errorf("%w: required capabilities: %w", ErrInvalidAction, err)
	}
	return ActionBinding{action: config.Action, required: required}, nil
}

func NewChildBinding(config ChildBindingConfig) (ActionBinding, error) {
	if !config.Action.Valid() || !config.Deployment.Valid() || !config.Capabilities.Valid() {
		return ActionBinding{}, fmt.Errorf("%w: invalid child binding", ErrInvalidAction)
	}
	return ActionBinding{
		action:          config.Action,
		childDeployment: config.Deployment, childBudget: config.Budget, childCapabilities: config.Capabilities,
		childInput: config.Input,
	}, nil
}

func (a ActionBinding) Action() Action { return a.action }

func (a ActionBinding) delegatesToChild() bool { return a.childDeployment.Valid() }

func (a ActionBinding) Valid() bool {
	if !a.action.Valid() || !a.required.Valid() {
		return false
	}
	if a.delegatesToChild() {
		return len(a.required.Values()) == 0 && a.childCapabilities.Valid()
	}
	return a.childInput == nil
}

func (a ActionBinding) childSpec(key agent.ChildKey, input agent.Payload) agent.ChildSpec {
	return agent.ChildSpec{
		Key: key, DeploymentRef: a.childDeployment.DeploymentRef(), Input: input,
		Budget: a.childBudget, Capabilities: a.childCapabilities,
	}
}
