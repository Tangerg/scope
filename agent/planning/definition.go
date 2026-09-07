package planning

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
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

	// MaxActionAttempts bounds external Action attempts. It must be positive.
	MaxActionAttempts uint32
}

// Definition is an immutable Planning Strategy definition. It contains no
// Sensor or ActionExecutor; those I/O capabilities belong to its
// Deployment-bound Dispatcher.
type Definition struct {
	descriptor        agent.Descriptor
	goal              Goal
	bindings          []ActionBinding
	byName            map[string]ActionBinding
	planner           Planner
	maxActionAttempts uint32
}

// NewDefinition freezes the goal, actions, and planner into one immutable
// behavior. The planner is chosen here rather than at run time so that a
// restored Execution searches with the same algorithm that produced the plan
// it is resuming.
func NewDefinition(config DefinitionConfig) (*Definition, error) {
	if !config.InputSchema.Valid() || !config.Goal.Valid() || lo.IsNil(config.Planner) || config.MaxActionAttempts == 0 {
		return nil, ErrInvalidDefinitionConfig
	}
	bindings := slices.Clone(config.Actions)
	byName := make(map[string]ActionBinding, len(bindings))
	for index, binding := range bindings {
		if !binding.Valid() {
			return nil, fmt.Errorf("%w: Actions[%d]", ErrInvalidDefinitionConfig, index)
		}
		name := binding.action.name
		if _, duplicate := byName[name]; duplicate {
			return nil, fmt.Errorf("%w: duplicate Action %q", ErrInvalidDefinitionConfig, name)
		}
		byName[name] = binding
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
		descriptor: descriptor, goal: config.Goal, bindings: bindings, byName: byName,
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
func (d *Definition) Start(input agent.Input) (agent.Execution, error) {
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
// exact Definition.
func (d *Definition) Restore(state agent.ExecutionState) (agent.Execution, error) {
	if !d.valid() {
		return nil, ErrInvalidDefinitionConfig
	}
	if state.Kind() != executionStateKind {
		return nil, fmt.Errorf("%w: unsupported kind", ErrInvalidExecutionState)
	}
	var decoded executionState
	if err := jsonv2.Unmarshal(state.Payload(), &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return nil, fmt.Errorf("%w: decode: %w", ErrInvalidExecutionState, err)
	}
	if err := decoded.validate(d); err != nil {
		return nil, err
	}
	return &execution{definition: d, state: decoded}, nil
}

func (d *Definition) valid() bool {
	if d == nil || !d.descriptor.Valid() || !d.goal.Valid() ||
		lo.IsNil(d.planner) || d.maxActionAttempts == 0 || len(d.bindings) != len(d.byName) {
		return false
	}
	for _, binding := range d.bindings {
		if !binding.Valid() || d.byName[binding.action.name].action.name != binding.action.name {
			return false
		}
	}
	return true
}

func (d *Definition) binding(name string) (ActionBinding, bool) {
	binding, found := d.byName[name]
	return binding, found
}

func (d *Definition) problem(state executionState) (Problem, error) {
	actions := make([]Action, 0, len(d.bindings))
	for _, binding := range d.bindings {
		if !state.actionExcluded(binding.action.name) {
			actions = append(actions, binding.action)
		}
	}
	return NewProblem(state.WorldState, d.goal, actions...)
}

func encodeExecutionState(state executionState) (agent.ExecutionState, error) {
	payload, err := json.Marshal(state)
	if err != nil {
		return agent.ExecutionState{}, fmt.Errorf("planning: encode execution state: %w", err)
	}
	return agent.NewExecutionState(executionStateKind, payload)
}
