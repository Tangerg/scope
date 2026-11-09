package team

import (
	"context"
	"fmt"
	"strings"

	"github.com/Tangerg/scope/agent/internal/strategy/restore"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/jsonschema"
)

const stateKind = "team"

func validateCoordinatorContract(binding run.ChildBinding) error {
	inputSchema, err := jsonschema.For[Turn]()
	if err != nil {
		return err
	}
	outputSchema, err := jsonschema.For[Decision]()
	if err != nil {
		return err
	}
	if !inputSchema.Equal(binding.Program.Descriptor().InputSchema()) ||
		!outputSchema.Equal(binding.Program.Descriptor().OutputSchema()) {
		return fmt.Errorf("%w: coordinator must accept Turn and return Decision", ErrInvalidConfig)
	}
	return nil
}

// definitionConfig binds the coordinator, permitted workers, schemas, and
// finite collaboration bounds. Coordinator must accept Turn and return Decision
// with exactly their core/jsonschema.For contracts. It may itself be any Strategy.
// MaxTasks counts all attempts, including rejected starts. MaxConcurrentTasks
// counts admitted tasks until their drained outcomes have been observed.
// The Program configuration digest must cover every field; the coordinator
// and worker bindings enter the Program identity through ChildPrograms.
type definitionConfig struct {
	Name         string
	Description  string
	Coordinator  run.ChildBinding
	Workers      []run.ChildBinding
	StateSchema  jsonschema.Schema
	OutputSchema jsonschema.Schema
	// MaxTurns is unlimited by default; a finite zero cannot admit the first turn.
	MaxTurns run.Quota
	// MaxTasks is unlimited by default; a finite zero forbids worker starts.
	MaxTasks           run.Quota
	MaxConcurrentTasks uint32
	// MaxControlsPerTurn bounds each Decision's controls independently of
	// cumulative work.
	MaxControlsPerTurn uint32
}

// definition coordinates background tasks through ordinary child Effects.
// It owns decision policy, not a scheduler, mailbox, model client, or session.
type definition struct {
	descriptor         run.Descriptor
	coordinator        run.ChildBinding
	workers            []run.ChildBinding
	maxTurns           run.Quota
	maxTasks           run.Quota
	maxConcurrentTasks uint32
	maxControlsPerTurn uint32
}

func newDefinition(config definitionConfig) (*definition, error) {
	if !config.MaxTurns.Allows(1) {
		return nil, fmt.Errorf("%w: MaxTurns must admit one coordinator turn", ErrInvalidConfig)
	}
	if !config.Coordinator.Valid() || len(config.Workers) == 0 || !config.StateSchema.Valid() ||
		!config.OutputSchema.Valid() ||
		config.MaxConcurrentTasks == 0 || config.MaxControlsPerTurn == 0 {
		return nil, ErrInvalidConfig
	}
	coordinator := config.Coordinator
	if err := validateCoordinatorContract(coordinator); err != nil {
		return nil, err
	}
	workers, err := newWorkerBindings(config.Workers)
	if err != nil {
		return nil, err
	}
	descriptor, err := run.NewDescriptor(run.DescriptorConfig{
		Name: config.Name, Description: config.Description, InputSchema: config.StateSchema, OutputSchema: config.OutputSchema,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}
	return &definition{descriptor: descriptor, coordinator: coordinator, workers: workers,
		maxTurns: config.MaxTurns, maxTasks: config.MaxTasks,
		maxConcurrentTasks: config.MaxConcurrentTasks, maxControlsPerTurn: config.MaxControlsPerTurn}, nil
}

func newWorkerBindings(configs []run.ChildBinding) ([]run.ChildBinding, error) {
	workers := make([]run.ChildBinding, 0, len(configs))
	for _, worker := range configs {
		if !worker.Valid() {
			return nil, ErrInvalidConfig
		}
		child := worker
		for _, previous := range workers {
			if previous.Program.Descriptor().Name() == child.Program.Descriptor().Name() {
				return nil, fmt.Errorf("%w: duplicate worker name", ErrInvalidConfig)
			}
		}
		workers = append(workers, child)
	}
	return workers, nil
}

// ChildPrograms reports the coordinator and every worker binding.
func (d *definition) ChildPrograms() []run.Program[run.Payload, run.Payload] {
	if d == nil {
		return nil
	}
	children := []run.Program[run.Payload, run.Payload]{d.coordinator.Program}
	for _, worker := range d.workers {
		children = append(children, worker.Program)
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
		return nil, ErrInvalidConfig
	}
	if err := d.descriptor.ValidateInput(input); err != nil {
		return nil, err
	}
	return &execution{definition: d, state: executionState{WorkingState: input}}, nil
}

func (d *definition) Restore(ctx context.Context, state run.ExecutionState) (run.Execution, error) {
	if !d.valid() {
		return nil, ErrInvalidConfig
	}
	decoded, err := restore.Decode(ctx, state, stateKind, ErrInvalidExecutionState, func(ctx context.Context, decoded executionState) error {
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

func (d *definition) worker(name string) (run.ChildBinding, bool) {
	for _, worker := range d.workers {
		if worker.Program.Descriptor().Name() == name {
			return worker, true
		}
	}
	return run.ChildBinding{}, false
}

func (d *definition) validateRequest(request TaskRequest) error {
	if !request.Key.Valid() || strings.HasPrefix(request.Key.String(), turnPrefix) {
		return ErrInvalidDecision
	}
	worker, found := d.worker(request.Worker)
	if !found {
		return fmt.Errorf("%w: unknown worker %q", ErrInvalidDecision, request.Worker)
	}
	if err := worker.Program.Descriptor().ValidateInput(request.Input); err != nil {
		return fmt.Errorf("%w: task input: %w", ErrInvalidDecision, err)
	}
	return nil
}

var _ run.Definition = (*definition)(nil)
