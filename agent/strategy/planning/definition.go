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
// Failed and unconfirmed Action names remain excluded for this Definition's
// entire execution, including after WorldState changes. Restore enforces this
// admission policy; portable Output validation only checks attempt facts.
type Definition struct {
	descriptor        agent.Descriptor
	goal              Goal
	bindings          []ActionBinding
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
	names := make(map[string]struct{}, len(bindings))
	for index, binding := range bindings {
		if !binding.Valid() {
			return nil, fmt.Errorf("%w: Actions[%d]", ErrInvalidDefinitionConfig, index)
		}
		name := binding.action.name
		if _, duplicate := names[name]; duplicate {
			return nil, fmt.Errorf("%w: duplicate Action %q", ErrInvalidDefinitionConfig, name)
		}
		names[name] = struct{}{}
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
		descriptor: descriptor, goal: config.Goal, bindings: bindings,
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
// exact Definition. A completed outcome must agree with the observed Goal
// satisfaction.
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
	return d != nil && d.descriptor.Valid()
}

func (d *Definition) binding(name string) (ActionBinding, bool) {
	for _, binding := range d.bindings {
		if binding.action.name == name {
			return binding, true
		}
	}
	return ActionBinding{}, false
}

func (d *Definition) problem(state executionState) (Problem, error) {
	actions := make([]Action, 0, len(d.bindings))
	for _, binding := range d.bindings {
		if !d.actionExcluded(state.Attempts, binding.action.name) {
			actions = append(actions, binding.action)
		}
	}
	return NewProblem(state.WorldState, d.goal, actions...)
}

func (d *Definition) actionExcluded(attempts []Attempt, name string) bool {
	for _, attempt := range attempts {
		if attempt.ActionName == name && d.excludes(attempt) {
			return true
		}
	}
	return false
}

func (d *Definition) excludes(attempt Attempt) bool {
	return attempt.Status != AttemptSucceeded
}

func (d *Definition) validateActionHistory(attempts []Attempt) error {
	if err := validateAttempts(attempts); err != nil {
		return err
	}
	excluded := make(map[string]struct{})
	for _, attempt := range attempts {
		if _, found := d.binding(attempt.ActionName); !found {
			return fmt.Errorf("attempt references unknown Action %q", attempt.ActionName)
		}
		if _, present := excluded[attempt.ActionName]; present {
			return fmt.Errorf("Action %q was attempted after exclusion", attempt.ActionName)
		}
		if d.excludes(attempt) {
			excluded[attempt.ActionName] = struct{}{}
		}
	}
	return nil
}

func encodeExecutionState(state executionState) (agent.ExecutionState, error) {
	payload, err := json.Marshal(state)
	if err != nil {
		return agent.ExecutionState{}, fmt.Errorf("planning: encode execution state: %w", err)
	}
	return agent.NewExecutionState(executionStateKind, payload)
}
