package planning

import (
	"context"
	"fmt"
	"slices"

	"github.com/samber/lo"

	agent "github.com/Tangerg/scope/agent"
)

const executionStateKind = "planning"

// DefinitionConfig contains one immutable managed Planning behavior. Goal,
// Planner, and Action bindings are fixed for the exact Deployment; only Input
// varies per Process.
type DefinitionConfig struct {
	// Name is the stable qualified Definition name.
	Name string

	// Description states the managed goal-directed behavior for discovery.
	Description string

	// InputSchema is the authoritative schema for opaque task input passed to
	// Sensor, ActionExecutor, and child input functions.
	InputSchema agent.Schema

	// Goal is the immutable target state.
	Goal Goal

	// Actions binds every predictive Action to exactly one execution mechanism.
	Actions []ActionBinding

	// Planner selects Actions from each newly observed WorldState.
	Planner Planner

	// MaxActionAttempts bounds external Action attempts. Its zero value is
	// unlimited; a finite zero is rejected because Planning must admit an Action.
	// This is an execution limit, not a Planner path-length constraint. A lowest-cost
	// plan may exceed the remaining attempts and finish Stuck even when a more
	// expensive shorter plan could reach the Goal within that limit.
	MaxActionAttempts agent.Quota
}

// Definition is an immutable Planning Strategy definition. It contains no
// Sensor or ActionExecutor; those I/O capabilities belong to its
// Deployment-bound Dispatcher.
// Failed and unconfirmed Action names remain excluded for this Definition's
// entire execution, including after WorldState changes. Restore enforces this
// admission policy; portable Output validation only checks attempt facts.
type Definition struct {
	descriptor        agent.Descriptor
	goal              Goal
	bindings          []ActionBinding
	bindingsByName    map[string]int
	planner           Planner
	maxActionAttempts agent.Quota
}

// NewDefinition freezes the goal, actions, and planner into one immutable
// behavior. The planner is chosen here rather than at run time so that a
// restored Execution searches with the same algorithm that produced the plan
// it is resuming.
func NewDefinition(config DefinitionConfig) (*Definition, error) {
	if !config.MaxActionAttempts.Allows(1) {
		return nil, fmt.Errorf("%w: MaxActionAttempts must admit one Action", ErrInvalidDefinitionConfig)
	}
	if !config.InputSchema.Valid() || !config.Goal.Valid() || lo.IsNil(config.Planner) {
		return nil, ErrInvalidDefinitionConfig
	}
	bindings := slices.Clone(config.Actions)
	names := make(map[string]int, len(bindings))
	for index, binding := range bindings {
		if !binding.Valid() {
			return nil, fmt.Errorf("%w: Actions[%d]", ErrInvalidDefinitionConfig, index)
		}
		name := binding.action.name
		if _, duplicate := names[name]; duplicate {
			return nil, fmt.Errorf("%w: duplicate Action %q", ErrInvalidDefinitionConfig, name)
		}
		names[name] = index
	}
	outputSchema, err := agent.SchemaFor[Output]()
	if err != nil {
		return nil, fmt.Errorf("%w: output schema: %w", ErrInvalidDefinitionConfig, err)
	}
	descriptor, err := agent.NewDescriptor(agent.DescriptorConfig{
		Name: config.Name, Description: config.Description,
		InputSchema: config.InputSchema, OutputSchema: outputSchema,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: descriptor: %w", ErrInvalidDefinitionConfig, err)
	}
	return &Definition{
		descriptor: descriptor, goal: config.Goal, bindings: bindings, bindingsByName: names,
		planner: config.Planner, maxActionAttempts: config.MaxActionAttempts,
	}, nil
}

// Descriptor returns the immutable managed Planning contract.
func (d *Definition) Descriptor() agent.Descriptor {
	if d == nil {
		return agent.Descriptor{}
	}
	return d.descriptor
}

// Start creates a fresh Planning Execution from validated opaque task input.
func (d *Definition) Start(input agent.Payload) (agent.Execution, error) {
	if !d.valid() {
		return nil, ErrInvalidDefinitionConfig
	}
	if err := d.descriptor.ValidateInput(input); err != nil {
		return nil, err
	}
	state := executionState{Phase: phaseReadySense, Input: input.JSON()}
	return &execution{definition: d, state: state}, nil
}

// Restore recreates a Planning Execution solely from its opaque state and this
// exact Definition. A completed outcome must agree with the observed Goal
// satisfaction.
func (d *Definition) Restore(ctx context.Context, state agent.ExecutionState) (agent.Execution, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if !d.valid() {
		return nil, ErrInvalidDefinitionConfig
	}
	decoded, err := state.Decode[executionState](executionStateKind)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidExecutionState, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := decoded.validate(ctx, d); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &execution{definition: d, state: decoded}, nil
}

func (d *Definition) valid() bool {
	return d != nil && d.descriptor.Valid()
}

func (d *Definition) binding(name string) (ActionBinding, bool) {
	index, found := d.bindingsByName[name]
	if !found {
		return ActionBinding{}, false
	}
	return d.bindings[index], true
}

func (d *Definition) problem(state executionState) Problem {
	excluded := make(map[string]struct{}, len(state.Attempts))
	for _, attempt := range state.Attempts {
		if attempt.excluded() {
			excluded[attempt.ActionName] = struct{}{}
		}
	}
	actions := make([]Action, 0, len(d.bindings))
	for _, binding := range d.bindings {
		if _, found := excluded[binding.action.name]; !found {
			actions = append(actions, binding.action)
		}
	}
	return Problem{initial: state.WorldState, goal: d.goal, actions: actions}
}

func (d *Definition) validateActionHistory(ctx context.Context, attempts []Attempt) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	excluded := make(map[string]struct{}, len(attempts))
	for index, attempt := range attempts {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := attempt.Validate(); err != nil {
			return fmt.Errorf("%w: attempt %d: %w", ErrInvalidResult, index, err)
		}
		if _, found := d.binding(attempt.ActionName); !found {
			return fmt.Errorf("attempt references unknown Action %q", attempt.ActionName)
		}
		if _, present := excluded[attempt.ActionName]; present {
			return fmt.Errorf("Action %q was attempted after exclusion", attempt.ActionName)
		}
		if attempt.excluded() {
			excluded[attempt.ActionName] = struct{}{}
		}
	}
	return ctx.Err()
}

var _ agent.Definition = (*Definition)(nil)
