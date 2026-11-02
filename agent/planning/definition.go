package planning

import (
	"context"
	"fmt"
	"slices"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/agent/internal/strategy/restore"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/jsonschema"
)

const executionStateKind = "planning"

// definitionConfig contains one immutable managed Planning behavior. Goal,
// Planner, and Action bindings are fixed for the exact Program; only Input
// varies per Process.
type definitionConfig struct {
	Name string

	Description string

	// InputSchema is the authoritative schema for opaque task input passed to
	// Sensor, ActionExecutor, and child input functions.
	InputSchema jsonschema.Schema

	Goal Goal

	Actions []ActionBinding

	Planner Planner

	// MaxActionAttempts bounds external Action attempts. Its zero value is
	// unlimited; a finite zero is rejected because Planning must admit an Action.
	// This is an execution limit, not a Planner path-length constraint. A lowest-cost
	// plan may exceed the remaining attempts and finish Exhausted even when a more
	// expensive shorter plan could reach the Goal within that limit.
	MaxActionAttempts run.Quota
}

// definition is an immutable Planning Strategy definition. It contains no
// Sensor or ActionExecutor; those I/O capabilities belong to its
// Program-bound dispatcher.
// Failed and unconfirmed Action names remain excluded for this definition's
// entire execution, including after WorldState changes. Restore enforces this
// admission policy; portable Output validation only checks attempt facts.
type definition struct {
	descriptor        run.Descriptor
	goal              Goal
	bindings          []ActionBinding
	bindingsByName    map[string]int
	planner           Planner
	maxActionAttempts run.Quota
}

func newDefinition(config definitionConfig) (*definition, error) {
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
	outputSchema, err := jsonschema.For[Output]()
	if err != nil {
		return nil, fmt.Errorf("%w: output schema: %w", ErrInvalidDefinitionConfig, err)
	}
	descriptor, err := run.NewDescriptor(run.DescriptorConfig{
		Name: config.Name, Description: config.Description,
		InputSchema: config.InputSchema, OutputSchema: outputSchema,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: descriptor: %w", ErrInvalidDefinitionConfig, err)
	}
	return &definition{
		descriptor: descriptor, goal: config.Goal, bindings: bindings, bindingsByName: names,
		planner: config.Planner, maxActionAttempts: config.MaxActionAttempts,
	}, nil
}

// ChildPrograms reports the child binding of every child Action.
func (d *definition) ChildPrograms() []run.Program[run.Payload, run.Payload] {
	if d == nil {
		return nil
	}
	var children []run.Program[run.Payload, run.Payload]
	for _, binding := range d.bindings {
		if binding.delegatesToChild() {
			children = append(children, binding.childProgram)
		}
	}
	return children
}

func (d *definition) Descriptor() run.Descriptor {
	if d == nil {
		return run.Descriptor{}
	}
	return d.descriptor
}

func (d *definition) Start(input run.Payload) (run.Execution, error) {
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
// exact definition. A completed state carries its phase and input but no Output,
// which the Runtime owns.
func (d *definition) Restore(ctx context.Context, state run.ExecutionState) (run.Execution, error) {
	if !d.valid() {
		return nil, ErrInvalidDefinitionConfig
	}
	decoded, err := restore.Decode(ctx, state, executionStateKind, ErrInvalidExecutionState, func(ctx context.Context, decoded executionState) error {
		return decoded.validate(ctx, d)
	})
	if err != nil {
		return nil, err
	}
	return &execution{definition: d, state: decoded}, nil
}

func (d *definition) valid() bool {
	return d != nil && d.descriptor.Valid()
}

func (d *definition) binding(name string) (ActionBinding, bool) {
	index, found := d.bindingsByName[name]
	if !found {
		return ActionBinding{}, false
	}
	return d.bindings[index], true
}

func (d *definition) problem(state executionState) Problem {
	excluded := make(map[string]struct{}, len(state.Attempts))
	for _, attempt := range state.Attempts {
		if attempt.excluded() {
			excluded[string(attempt.ActionName)] = struct{}{}
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

func (d *definition) validateActionHistory(ctx context.Context, attempts []Attempt) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	excluded := make(map[string]struct{}, len(attempts))
	for index, attempt := range attempts {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := attempt.Validate(); err != nil {
			return fmt.Errorf("attempt %d: %w", index, err)
		}
		if _, found := d.binding(string(attempt.ActionName)); !found {
			return fmt.Errorf("attempt references unknown Action %q", attempt.ActionName)
		}
		if _, present := excluded[string(attempt.ActionName)]; present {
			return fmt.Errorf("Action %q was attempted after exclusion", attempt.ActionName)
		}
		if attempt.excluded() {
			excluded[string(attempt.ActionName)] = struct{}{}
		}
	}
	return ctx.Err()
}

var _ run.Definition = (*definition)(nil)
