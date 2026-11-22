package workflow

import (
	"fmt"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/jsonschema"
)

// LoopPredicate is the completion test evaluated after each successful body
// child Process. Like TransformFunc it runs inside the replayed Step, so it
// must be bounded, deterministic and side-effect-free, and it takes no context.
type LoopPredicate[T any] func(value T) (bool, error)

// LoopResult is the exact semantic output of a Loop Stage. Satisfied is false
// when MaxIterations was exhausted; that outcome is still a valid completion.
type LoopResult[T any] struct {
	// Value is the output of the last completed body iteration.
	Value T `json:"value"`
	// Iterations counts completed body iterations; a Loop always runs one.
	Iterations uint64 `json:"iterations"`
	Satisfied  bool   `json:"satisfied"`
}

type LoopConfig[T any] struct {
	// ID is unique within the Workflow and remains stable across restoration.
	ID string

	Body run.ChildBinding

	// MaxIterations bounds body child Processes. Its zero value is unlimited;
	// a finite zero is rejected because a Loop runs its body at least once.
	MaxIterations run.Quota

	Predicate LoopPredicate[T]
}

type loopStage struct {
	binding       run.ChildBinding
	maxIterations run.Quota
	predicate     func(run.Payload) (bool, error)
	result        func(run.Payload, uint64, bool) (run.Payload, error)
}

// Loop constructs one at-least-once managed iteration Stage. Body must accept
// and produce exactly T; the Stage itself produces LoopResult[T].
func Loop[T any](config LoopConfig[T]) (Stage, error) {
	binding := config.Body
	if !run.QualifiedName(config.ID).Valid() || !binding.Valid() || !config.MaxIterations.Allows(1) || config.Predicate == nil {
		return Stage{}, ErrInvalidStage
	}
	valueSchema, err := jsonschema.For[T]()
	if err != nil {
		return Stage{}, fmt.Errorf("%w: Loop %q value schema: %w", ErrInvalidStage, config.ID, err)
	}
	resultSchema, err := jsonschema.For[LoopResult[T]]()
	if err != nil {
		return Stage{}, fmt.Errorf("%w: Loop %q result schema: %w", ErrInvalidStage, config.ID, err)
	}
	descriptor := binding.Program.Descriptor()
	if !valueSchema.Equal(descriptor.InputSchema()) ||
		!valueSchema.Equal(descriptor.OutputSchema()) {
		return Stage{}, fmt.Errorf("%w: Loop %q body must have an exact T-to-T contract", ErrInvalidStage, config.ID)
	}
	return Stage{
		id: config.ID, kind: StageKindLoop,
		inputSchema: valueSchema, outputSchema: resultSchema,
		loop: loopStage{
			binding: binding, maxIterations: config.MaxIterations,
			predicate: loopPredicate(config.ID, config.Predicate),
			result:    loopResult[T](resultSchema),
		},
	}, nil
}

func loopPredicate[T any](stageID string, predicate LoopPredicate[T]) func(run.Payload) (bool, error) {
	return func(output run.Payload) (bool, error) {
		value, err := output.Decode[T]()
		if err != nil {
			return false, err
		}
		satisfied, err := predicate(value)
		if err != nil {
			return false, fmt.Errorf("Loop %q predicate: %w", stageID, err)
		}
		return satisfied, nil
	}
}

func loopResult[T any](resultSchema jsonschema.Schema) func(run.Payload, uint64, bool) (run.Payload, error) {
	return func(output run.Payload, iterations uint64, satisfied bool) (run.Payload, error) {
		value, err := output.Decode[T]()
		if err != nil {
			return run.Payload{}, err
		}
		return encodeConforming(resultSchema, LoopResult[T]{Value: value, Iterations: iterations, Satisfied: satisfied})
	}
}
