package collaboration

import (
	"bytes"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"slices"

	agent "github.com/Tangerg/scope/agent"
)

var (
	ErrInvalidConfig   = errors.New("collaboration: invalid configuration")
	ErrInvalidDecision = errors.New("collaboration: invalid decision")
	ErrInvalidState    = errors.New("collaboration: invalid execution state")
	ErrInvalidProtocol = errors.New("collaboration: invalid execution protocol")
	ErrTurnLimit       = errors.New("collaboration: turn limit reached")
)

const stateKind = "collaboration"

// WorkerConfig freezes a child binding and its permanently allocated grants.
// Workers are selected by Deployment.Descriptor().Name(), never by routing or
// a model-supplied DeploymentRef, budget, or capability grant.
type WorkerConfig struct {
	Deployment   agent.Deployment
	Budget       agent.Budget
	Capabilities agent.CapabilitySet
}

func (w WorkerConfig) valid() bool {
	return w.Deployment.Valid() && w.Budget.Valid() && w.Capabilities.Valid()
}

func (w WorkerConfig) spec(key agent.ChildKey, input agent.Input) agent.ChildSpec {
	return agent.ChildSpec{Key: key, Input: input, DeploymentRef: w.Deployment.DeploymentRef(),
		Budget: w.Budget, Capabilities: w.Capabilities}
}

// DefinitionConfig binds the coordinator, permitted workers, schemas, and
// finite collaboration bounds. Coordinator must accept Turn and return Decision
// with exactly their SchemaFor contracts. It may itself be any Strategy.
// MaxTasks counts all attempts, including rejected starts. MaxConcurrentTasks
// counts admitted tasks until their drained outcomes have been observed.
// The Deployment configuration digest must cover every field and child binding.
type DefinitionConfig struct {
	Name               string
	Description        string
	Coordinator        WorkerConfig
	Workers            []WorkerConfig
	StateSchema        agent.Schema
	OutputSchema       agent.Schema
	MaxTurns           uint32
	MaxTasks           uint32
	MaxConcurrentTasks uint32
	MaxControlsPerTurn uint32
}

// Definition coordinates background tasks through ordinary child Effects.
// It owns decision policy, not a scheduler, mailbox, model client, or session.
type Definition struct {
	descriptor agent.Descriptor
	config     DefinitionConfig
}

func NewDefinition(config DefinitionConfig) (*Definition, error) {
	if !config.Coordinator.valid() || len(config.Workers) == 0 || !config.StateSchema.Valid() ||
		!config.OutputSchema.Valid() || config.MaxTurns == 0 || config.MaxTasks == 0 ||
		config.MaxConcurrentTasks == 0 || config.MaxConcurrentTasks > config.MaxTasks || config.MaxControlsPerTurn == 0 {
		return nil, ErrInvalidConfig
	}
	inputSchema, err := agent.SchemaFor[Turn]()
	if err != nil {
		return nil, err
	}
	outputSchema, err := agent.SchemaFor[Decision]()
	if err != nil {
		return nil, err
	}
	coordinator := config.Coordinator.Deployment.Descriptor()
	if !bytes.Equal(inputSchema.JSON(), coordinator.InputSchema().JSON()) ||
		!bytes.Equal(outputSchema.JSON(), coordinator.OutputSchema().JSON()) {
		return nil, fmt.Errorf("%w: coordinator must accept Turn and return Decision", ErrInvalidConfig)
	}
	for index, worker := range config.Workers {
		if !worker.valid() {
			return nil, ErrInvalidConfig
		}
		for _, previous := range config.Workers[:index] {
			if previous.Deployment.Descriptor().Name() == worker.Deployment.Descriptor().Name() {
				return nil, fmt.Errorf("%w: duplicate worker name", ErrInvalidConfig)
			}
		}
	}
	descriptor, err := agent.NewDescriptor(agent.DescriptorConfig{
		Name: config.Name, Description: config.Description, InputSchema: config.StateSchema, OutputSchema: config.OutputSchema,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}
	config.Workers = slices.Clone(config.Workers)
	return &Definition{descriptor: descriptor, config: config}, nil
}

func (d *Definition) Descriptor() agent.Descriptor {
	if d == nil {
		return agent.Descriptor{}
	}
	return d.descriptor
}

func (d *Definition) Start(input agent.Input) (agent.Execution, error) {
	if d == nil || !d.descriptor.Valid() {
		return nil, ErrInvalidConfig
	}
	if err := d.descriptor.ValidateInput(input); err != nil {
		return nil, err
	}
	return &execution{definition: d, state: executionState{Phase: phaseReady, State: input}}, nil
}

func (d *Definition) Restore(state agent.ExecutionState) (agent.Execution, error) {
	if d == nil || !d.descriptor.Valid() {
		return nil, ErrInvalidConfig
	}
	if state.Kind() != stateKind {
		return nil, ErrInvalidState
	}
	var decoded executionState
	if err := jsonv2.Unmarshal(state.Payload(), &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidState, err)
	}
	if err := decoded.validate(d); err != nil {
		return nil, err
	}
	return &execution{definition: d, state: decoded}, nil
}

func (d *Definition) worker(name string) (WorkerConfig, bool) {
	for _, worker := range d.config.Workers {
		if worker.Deployment.Descriptor().Name() == name {
			return worker, true
		}
	}
	return WorkerConfig{}, false
}

func (e *execution) Snapshot() (agent.ExecutionState, error) {
	if err := e.state.validate(e.definition); err != nil {
		return agent.ExecutionState{}, err
	}
	payload, err := json.Marshal(e.state)
	if err != nil {
		return agent.ExecutionState{}, err
	}
	return agent.NewExecutionState(stateKind, payload)
}
