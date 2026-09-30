package workflow

import (
	"context"
	"fmt"
	"math"
	"slices"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/internal/restore"
)

const executionStateKind = "workflow"

// DefinitionConfig contains one immutable managed Workflow behavior. Stages
// execute in declaration order and must have exactly matching adjacent schemas.
type DefinitionConfig struct {
	Name string

	Description string

	Stages []Stage
}

// Definition is an immutable managed Workflow Strategy.
type Definition struct {
	descriptor agent.Descriptor
	stages     []Stage
}

func NewDefinition(config DefinitionConfig) (*Definition, error) {
	if len(config.Stages) == 0 || uint64(len(config.Stages)) > math.MaxUint32 {
		return nil, fmt.Errorf("%w: Stages must contain 1 to %d entries", ErrInvalidDefinitionConfig, uint64(math.MaxUint32))
	}
	stages := slices.Clone(config.Stages)
	identities := make(map[string]struct{}, len(stages))
	for index, stage := range stages {
		if !stage.Valid() {
			return nil, fmt.Errorf("%w: Stages[%d]: %w", ErrInvalidDefinitionConfig, index, ErrInvalidStage)
		}
		if _, duplicate := identities[stage.id]; duplicate {
			return nil, fmt.Errorf("%w: duplicate Stage ID %q", ErrInvalidDefinitionConfig, stage.id)
		}
		identities[stage.id] = struct{}{}
		if index > 0 && !stage.hasIdenticalInputSchema(stages[index-1].outputSchema) {
			return nil, fmt.Errorf(
				"%w: Stage %q input schema does not exactly match Stage %q output schema",
				ErrInvalidDefinitionConfig, stage.id, stages[index-1].id,
			)
		}
	}
	descriptor, err := agent.NewDescriptor(agent.DescriptorConfig{
		Name: config.Name, Description: config.Description,
		InputSchema: stages[0].inputSchema, OutputSchema: stages[len(stages)-1].outputSchema,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: descriptor: %w", ErrInvalidDefinitionConfig, err)
	}
	return &Definition{descriptor: descriptor, stages: stages}, nil
}

func (d *Definition) Descriptor() agent.Descriptor {
	if d == nil {
		return agent.Descriptor{}
	}
	return d.descriptor
}

func (d *Definition) Start(input agent.Payload) (agent.Execution, error) {
	if !d.valid() {
		return nil, ErrInvalidDefinitionConfig
	}
	if err := d.descriptor.ValidateInput(input); err != nil {
		return nil, err
	}
	state := executionState{CurrentValue: input.JSON()}
	return &execution{definition: d, state: state}, nil
}

// Restore rejects progress inconsistent with the exact Definition bindings.
func (d *Definition) Restore(ctx context.Context, state agent.ExecutionState) (agent.Execution, error) {
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

func (d *Definition) valid() bool {
	return d != nil && d.descriptor.Valid()
}

// ChildDeployments returns the exact child binding of every Stage.
func (d *Definition) ChildDeployments() []agent.Deployment {
	if !d.valid() {
		return nil
	}
	var children []agent.Deployment
	for _, stage := range d.stages {
		for _, binding := range stage.childBindings() {
			children = append(children, binding.deployment)
		}
	}
	return children
}

// Topology returns a fresh, function-free projection of this Definition. An
// invalid or nil Definition returns the zero Topology.
func (d *Definition) Topology() Topology {
	if !d.valid() {
		return Topology{}
	}
	stages := make([]StageTopology, len(d.stages))
	for index, stage := range d.stages {
		stages[index] = stage.topology()
	}
	return Topology{Descriptor: d.descriptor, Stages: stages}
}

var _ agent.Definition = (*Definition)(nil)
