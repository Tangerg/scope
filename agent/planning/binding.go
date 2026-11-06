package planning

import (
	"fmt"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/agent/run"
)

// ChildInputFunc derives a child Process input from the parent input and the
// WorldState observed when the Action is selected.
type ChildInputFunc func(processInput run.Payload, worldState WorldState) (run.Payload, error)

// Replay is opt-in: SameIdentity requires the executor to honor EffectID even
// after an interrupted attempt. It does not promise concurrent safety or a
// deterministic result.
type DispatcherBindingConfig struct {
	Action               Action
	Executor             ActionExecutor
	Replay               run.ReplayPolicy
	RequiredCapabilities []run.Capability
}

type ChildBindingConfig struct {
	Action Action
	Child  run.ChildBinding
	// Input must be deterministic; nil reuses the process input.
	Input ChildInputFunc
}

// ActionBinding fixes one action's prediction, authority, and execution
// mechanism together, so a separate name-indexed registry cannot redirect it.
type ActionBinding struct {
	declaration actionDeclaration
	executor    ActionExecutor
}

func NewDispatcherBinding(config DispatcherBindingConfig) (ActionBinding, error) {
	if !config.Action.Valid() || lo.IsNil(config.Executor) {
		return ActionBinding{}, fmt.Errorf("%w: dispatcher binding requires an Action and Executor", ErrInvalidAction)
	}
	replay := config.Replay
	if replay == run.ReplayPolicyInvalid {
		replay = run.ReplayPolicyNever
	}
	if !replay.Valid() {
		return ActionBinding{}, fmt.Errorf("%w: dispatcher binding Replay", ErrInvalidAction)
	}
	required, err := run.NewCapabilitySet(config.RequiredCapabilities...)
	if err != nil {
		return ActionBinding{}, fmt.Errorf("%w: required capabilities: %w", ErrInvalidAction, err)
	}
	return ActionBinding{declaration: actionDeclaration{action: config.Action, policy: run.EffectPolicy{Replay: replay, RequiredCapabilities: required}}, executor: config.Executor}, nil
}

func NewChildBinding(config ChildBindingConfig) (ActionBinding, error) {
	if !config.Action.Valid() || !config.Child.Valid() {
		return ActionBinding{}, fmt.Errorf("%w: invalid child binding", ErrInvalidAction)
	}
	return ActionBinding{declaration: actionDeclaration{action: config.Action, child: config.Child, childInput: config.Input}}, nil
}

func (a ActionBinding) Action() Action { return a.declaration.action }

func (a ActionBinding) Valid() bool {
	return a.declaration.valid() && a.declaration.delegatesToChild() == lo.IsNil(a.executor)
}
