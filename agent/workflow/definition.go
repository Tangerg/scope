package workflow

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"math"
	"slices"

	agent "github.com/Tangerg/scope/agent"
)

const executionStateKind = "workflow"

// DefinitionConfig contains one immutable managed Workflow behavior. Stages
// execute in declaration order and must have exactly matching adjacent schemas.
type DefinitionConfig struct {
	// Name is the stable qualified Definition name.
	Name string

	// Description states the managed orchestration behavior for discovery.
	Description string

	// Stages is a non-empty ordered sequence of sealed operations.
	Stages []Stage
}

// Definition is an immutable managed Workflow Strategy.
type Definition struct {
	descriptor agent.Descriptor
	stages     []Stage
}

// NewDefinition connects adjacent stage schemas at construction, so a
// mismatched pipeline fails where it is declared rather than midway through a
// run that has already started child Processes and spent budget.
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
		if index > 0 && !stage.accepts(stages[index-1].outputSchema) {
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

// Descriptor returns the immutable erased Workflow contract.
func (d *Definition) Descriptor() agent.Descriptor {
	if d == nil {
		return agent.Descriptor{}
	}
	return d.descriptor
}

// Start creates a fresh Workflow from validated caller input.
func (d *Definition) Start(input agent.Input) (agent.Execution, error) {
	if !d.valid() {
		return nil, ErrInvalidDefinitionConfig
	}
	if err := d.descriptor.ValidateInput(input); err != nil {
		return nil, err
	}
	state := executionState{Phase: phaseReady, CurrentValue: input.JSON()}
	return &execution{definition: d, state: state}, nil
}

// Restore recreates a Workflow solely from its opaque state and this exact
// Definition. Phase-specific progress, window bounds, and unique child identities
// are validated before admitting the restored Execution.
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

func encodeExecutionState(state executionState) (agent.ExecutionState, error) {
	payload, err := json.Marshal(state)
	if err != nil {
		return agent.ExecutionState{}, fmt.Errorf("workflow: encode execution state: %w", err)
	}
	return agent.NewExecutionState(executionStateKind, payload)
}
