package workflow

import (
	"encoding/json"
	"fmt"

	agent "github.com/Tangerg/scope/agent"
)

// LoopPredicate is a bounded, deterministic, side-effect-free completion test
// evaluated after each successful body child Process.
type LoopPredicate[T any] func(value T) (bool, error)

// LoopResult is the exact semantic output of a Loop Stage. Satisfied is false
// when MaxIterations was exhausted; that outcome is still a valid completion.
type LoopResult[T any] struct {
	// Value is the latest body output, or the initial input before any iteration.
	Value T `json:"value"`
	// Iterations is the number of completed body child Processes.
	Iterations uint32 `json:"iterations"`
	// Satisfied reports whether Predicate accepted Value.
	Satisfied bool `json:"satisfied"`
}

func (l LoopResult[T]) Valid() bool { return l.Iterations > 0 }

// LoopConfig declares one at-least-once managed body iteration.
type LoopConfig[T any] struct {
	// ID is unique within the Workflow and remains stable across restoration.
	ID string

	// Body is the exact T-to-T child Deployment used for every iteration.
	Body agent.Deployment

	// Budget is permanently allocated from the parent for each iteration.
	Budget agent.Budget

	// Capabilities is the attenuated authority set granted to each child.
	Capabilities agent.CapabilitySet

	// MaxIterations is the positive hard upper bound on body child Processes.
	MaxIterations uint32

	// Predicate decides whether the latest body output satisfies the Loop.
	Predicate LoopPredicate[T]
}

type loopStage struct {
	binding       childBinding
	maxIterations uint32
	valueSchema   agent.Schema
	predicate     func(json.RawMessage) (bool, error)
	result        func(json.RawMessage, uint32, bool) (json.RawMessage, error)
}

func (l loopStage) valid() bool {
	return l.binding.valid() && l.maxIterations > 0 && l.valueSchema.Valid() &&
		l.predicate != nil && l.result != nil
}

// Loop constructs one at-least-once managed iteration Stage. Body must accept
// and produce exactly T; the Stage itself produces LoopResult[T].
func Loop[T any](config LoopConfig[T]) (Stage, error) {
	if !validStageID(config.ID) || !config.Body.Valid() || !config.Budget.Valid() ||
		!config.Capabilities.Valid() || config.MaxIterations == 0 || config.Predicate == nil {
		return Stage{}, ErrInvalidStage
	}
	valueSchema, err := agent.SchemaFor[T]()
	if err != nil {
		return Stage{}, fmt.Errorf("%w: Loop %q value schema: %w", ErrInvalidStage, config.ID, err)
	}
	resultSchema, err := agent.SchemaFor[LoopResult[T]]()
	if err != nil {
		return Stage{}, fmt.Errorf("%w: Loop %q result schema: %w", ErrInvalidStage, config.ID, err)
	}
	descriptor := config.Body.Descriptor()
	if !schemasEqual(valueSchema, descriptor.InputSchema()) ||
		!schemasEqual(valueSchema, descriptor.OutputSchema()) {
		return Stage{}, fmt.Errorf("%w: Loop %q body must have an exact T-to-T contract", ErrInvalidStage, config.ID)
	}
	predicate := config.Predicate
	evaluate := func(raw json.RawMessage) (bool, error) {
		output, err := agent.ParseOutput(raw)
		if err != nil {
			return false, err
		}
		if validateOutputErr := valueSchema.ValidateOutput(output); validateOutputErr != nil {
			return false, validateOutputErr
		}
		value, err := output.Decode[T]()
		if err != nil {
			return false, err
		}
		satisfied, err := predicate(value)
		if err != nil {
			return false, fmt.Errorf("Loop %q predicate: %w", config.ID, err)
		}
		return satisfied, nil
	}
	result := func(raw json.RawMessage, iterations uint32, satisfied bool) (json.RawMessage, error) {
		output, err := agent.ParseOutput(raw)
		if err != nil {
			return nil, err
		}
		value, err := output.Decode[T]()
		if err != nil {
			return nil, err
		}
		loopResult := LoopResult[T]{Value: value, Iterations: iterations, Satisfied: satisfied}
		if !loopResult.Valid() {
			return nil, ErrInvalidExecutionState
		}
		erased, err := agent.EncodeOutput(loopResult)
		if err != nil {
			return nil, err
		}
		if err := resultSchema.ValidateOutput(erased); err != nil {
			return nil, err
		}
		return erased.JSON(), nil
	}
	return Stage{
		id: config.ID, kind: StageKindLoop,
		inputSchema: valueSchema, outputSchema: resultSchema,
		loop: loopStage{
			binding: childBinding{
				deploymentRef: config.Body.DeploymentRef(), budget: config.Budget,
				capabilities: config.Capabilities,
			},
			maxIterations: config.MaxIterations, valueSchema: valueSchema,
			predicate: evaluate, result: result,
		},
	}, nil
}
