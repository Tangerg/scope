package workflow

import (
	"context"
	"fmt"
	"math"
	"slices"

	"github.com/Tangerg/scope/agent/internal/strategy/restore"
	"github.com/Tangerg/scope/agent/run"
)

const executionStateKind = "workflow"

// definition is an immutable managed Workflow Strategy.
type definition struct {
	descriptor run.Descriptor
	stages     []Stage
}

func newDefinition(config Config) (*definition, error) {
	if len(config.Stages) == 0 || uint64(len(config.Stages)) > math.MaxUint32 {
		return nil, fmt.Errorf("%w: Stages must contain 1 to %d entries", ErrInvalidConfig, uint64(math.MaxUint32))
	}
	stages := slices.Clone(config.Stages)
	identities := make(map[string]struct{}, len(stages))
	for index, stage := range stages {
		if !stage.Valid() {
			return nil, fmt.Errorf("%w: Stages[%d]: %w", ErrInvalidConfig, index, ErrInvalidStage)
		}
		if _, duplicate := identities[stage.id]; duplicate {
			return nil, fmt.Errorf("%w: duplicate Stage ID %q", ErrInvalidConfig, stage.id)
		}
		identities[stage.id] = struct{}{}
		if index > 0 && !stage.hasIdenticalInputSchema(stages[index-1].outputSchema) {
			return nil, fmt.Errorf(
				"%w: Stage %q input schema does not exactly match Stage %q output schema",
				ErrInvalidConfig, stage.id, stages[index-1].id,
			)
		}
	}
	descriptor, err := run.NewDescriptor(run.DescriptorConfig{
		Name: config.Name, Description: config.Description,
		InputSchema: stages[0].inputSchema, OutputSchema: stages[len(stages)-1].outputSchema,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: descriptor: %w", ErrInvalidConfig, err)
	}
	return &definition{descriptor: descriptor, stages: stages}, nil
}

func (d *definition) Descriptor() run.Descriptor {
	if d == nil {
		return run.Descriptor{}
	}
	return d.descriptor
}

func (d *definition) Start(input run.Payload) (run.Execution, error) {
	if !d.valid() {
		return nil, ErrInvalidConfig
	}
	if err := d.descriptor.ValidateInput(input); err != nil {
		return nil, err
	}
	state := executionState{CurrentValue: input.JSON()}
	return &execution{definition: d, state: state}, nil
}

// Restore rejects progress inconsistent with the exact definition bindings.
func (d *definition) Restore(ctx context.Context, state run.ExecutionState) (run.Execution, error) {
	if !d.valid() {
		return nil, ErrInvalidConfig
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

// ChildPrograms returns the exact child binding of every Stage.
func (d *definition) ChildPrograms() []run.Program[run.Payload, run.Payload] {
	if !d.valid() {
		return nil
	}
	var children []run.Program[run.Payload, run.Payload]
	for _, stage := range d.stages {
		for _, binding := range stage.childBindings() {
			children = append(children, binding.Program)
		}
	}
	return children
}

// Topology returns a fresh, function-free projection of this definition. An
// invalid or nil definition returns the zero Topology.
func (d *definition) Topology() Topology {
	if !d.valid() {
		return Topology{}
	}
	stages := make([]StageTopology, len(d.stages))
	for index, stage := range d.stages {
		stages[index] = stage.topology()
	}
	return Topology{Descriptor: d.descriptor, Stages: stages}
}

var _ run.Definition = (*definition)(nil)
