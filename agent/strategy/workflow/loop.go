package workflow

import (
	"context"
	"encoding/json"
	"fmt"

	agent "github.com/Tangerg/scope/agent"
)

// LoopPredicate is a bounded, deterministic, side-effect-free completion test
// evaluated after each successful body child Process. It must honor ctx
// cancellation; context is not a source of domain input.
type LoopPredicate[T any] func(ctx context.Context, value T) (bool, error)

// LoopResult is the exact semantic output of a Loop Stage. Satisfied is false
// when MaxIterations was exhausted; that outcome is still a valid completion.
type LoopResult[T any] struct {
	// Value is the output of the last completed body iteration.
	Value T `json:"value"`
	// Iterations is the number of completed body child Processes.
	Iterations uint64 `json:"iterations"`
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

	// MaxIterations bounds body child Processes. Its zero value is unlimited;
	// a finite zero is rejected because a Loop runs its body at least once.
	MaxIterations agent.Quota

	// Predicate decides whether the latest body output satisfies the Loop.
	Predicate LoopPredicate[T]
}

type loopStage struct {
	binding       childBinding
	maxIterations agent.Quota
	valueSchema   agent.Schema
	predicate     func(context.Context, json.RawMessage) (bool, error)
	result        func(json.RawMessage, uint64, bool) (json.RawMessage, error)
}

// Loop constructs one at-least-once managed iteration Stage. Body must accept
// and produce exactly T; the Stage itself produces LoopResult[T].
func Loop[T any](config LoopConfig[T]) (Stage, error) {
	if !agent.ValidQualifiedName(config.ID) || !config.Body.Valid() ||
		!config.Capabilities.Valid() || !config.MaxIterations.Allows(1) || config.Predicate == nil {
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
	evaluate := func(ctx context.Context, raw json.RawMessage) (bool, error) {
		output, err := agent.ParsePayload(raw)
		if err != nil {
			return false, err
		}
		if validateOutputErr := valueSchema.Validate(output.JSON()); validateOutputErr != nil {
			return false, validateOutputErr
		}
		value, err := output.Decode[T]()
		if err != nil {
			return false, err
		}
		satisfied, err := predicate(ctx, value)
		if err != nil {
			return false, fmt.Errorf("Loop %q predicate: %w", config.ID, err)
		}
		return satisfied, nil
	}
	result := func(raw json.RawMessage, iterations uint64, satisfied bool) (json.RawMessage, error) {
		output, err := agent.ParsePayload(raw)
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
		erased, err := agent.EncodePayload(loopResult)
		if err != nil {
			return nil, err
		}
		if err := resultSchema.Validate(erased.JSON()); err != nil {
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
