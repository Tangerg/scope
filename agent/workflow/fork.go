package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"math"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/jsonschema"
)

// ForkReducer combines the Fork's input with its branch outputs in
// declaration order; branches therefore return only what they add. It must be
// bounded, deterministic, side-effect-free, honor ctx cancellation, and never
// retain the slice. Context is not a source of domain input.
type ForkReducer[I, B, O any] func(ctx context.Context, input I, branchOutputs []B) (O, error)

type ForkBranch struct {
	ID    string
	Child run.ChildBinding
}

type ForkConfig[I, B, O any] struct {
	// ID is unique within the Workflow and remains stable across restoration.
	ID string

	// Branches is a non-empty list in stable declaration order. Every branch
	// accepts I and produces B.
	Branches []ForkBranch

	// WindowSize is the positive number of branches started and settled as one
	// execution window before the next window begins.
	WindowSize uint32

	Reduce ForkReducer[I, B, O]
}

type forkSource struct{ branches []fanoutMember }

func newForkSource(stageID string, inputSchema, branchSchema jsonschema.Schema, declared []ForkBranch) (forkSource, error) {
	branches := make([]fanoutMember, 0, len(declared))
	seen := make(map[string]struct{}, len(declared))
	for index, branch := range declared {
		binding := branch.Child
		if !run.ValidQualifiedName(branch.ID) || !binding.Valid() {
			return forkSource{}, fmt.Errorf("%w: Fork %q Branches[%d]", ErrInvalidStage, stageID, index)
		}
		if _, duplicate := seen[branch.ID]; duplicate {
			return forkSource{}, fmt.Errorf("%w: Fork %q has duplicate branch %q", ErrInvalidStage, stageID, branch.ID)
		}
		descriptor := binding.Program.Descriptor()
		if !inputSchema.Equal(descriptor.InputSchema()) ||
			!branchSchema.Equal(descriptor.OutputSchema()) {
			return forkSource{}, fmt.Errorf("%w: Fork %q branch %q schema mismatch", ErrInvalidStage, stageID, branch.ID)
		}
		seen[branch.ID] = struct{}{}
		branches = append(branches, fanoutMember{id: branch.ID, binding: binding})
	}
	return forkSource{branches: branches}, nil
}

func (f forkSource) prepareInput(ctx context.Context, raw json.RawMessage) (fanoutInput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	input, err := run.ParsePayload(raw)
	if err != nil {
		return nil, err
	}
	return forkInputValue{payload: input, branchCount: uint32(len(f.branches))}, nil
}

func (f forkSource) member(index uint32) (fanoutMember, bool) {
	if uint64(index) >= uint64(len(f.branches)) {
		return fanoutMember{}, false
	}
	return f.branches[index], true
}

func (f forkSource) bindings() []run.ChildBinding {
	bindings := make([]run.ChildBinding, len(f.branches))
	for index, branch := range f.branches {
		bindings[index] = branch.binding
	}
	return bindings
}

func (f forkSource) topology(inputSchema, outputSchema jsonschema.Schema) ([]BindingTopology, uint32) {
	bindings := make([]BindingTopology, len(f.branches))
	for index, branch := range f.branches {
		bindings[index] = bindingTopology(branch.binding, BindingRoleBranch, branch.id, inputSchema, outputSchema)
	}
	return bindings, 0
}

type forkInputValue struct {
	payload     run.Payload
	branchCount uint32
}

func (f forkInputValue) count() uint32 { return f.branchCount }

func (f forkInputValue) windowInputs(ctx context.Context, start, windowSize uint32) ([]run.Payload, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if start > f.branchCount {
		return nil, ErrInvalidExecutionState
	}
	var inputs []run.Payload
	if size := fanoutWindowLen(start, f.branchCount, windowSize); size > 0 {
		inputs = make([]run.Payload, size)
	}
	for index := range inputs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		inputs[index] = f.payload
	}
	return inputs, nil
}

// Fork constructs one windowed managed fan-out Stage. Branch inputs and
// outputs are homogeneous; heterogeneous work can be wrapped by child
// Workflows that expose a shared contract.
func Fork[I, B, O any](config ForkConfig[I, B, O]) (Stage, error) {
	if !run.ValidQualifiedName(config.ID) || len(config.Branches) == 0 ||
		uint64(len(config.Branches)) > math.MaxUint32 || config.Reduce == nil ||
		config.WindowSize == 0 || uint64(config.WindowSize) > uint64(len(config.Branches)) {
		return Stage{}, ErrInvalidStage
	}
	inputSchema, err := jsonschema.For[I]()
	if err != nil {
		return Stage{}, fmt.Errorf("%w: Fork %q input schema: %w", ErrInvalidStage, config.ID, err)
	}
	branchSchema, err := jsonschema.For[B]()
	if err != nil {
		return Stage{}, fmt.Errorf("%w: Fork %q branch schema: %w", ErrInvalidStage, config.ID, err)
	}
	outputSchema, err := jsonschema.For[O]()
	if err != nil {
		return Stage{}, fmt.Errorf("%w: Fork %q output schema: %w", ErrInvalidStage, config.ID, err)
	}
	source, err := newForkSource(config.ID, inputSchema, branchSchema, config.Branches)
	if err != nil {
		return Stage{}, err
	}
	reducer := config.Reduce
	outputs := fanoutOutputs{
		stageName: "Fork", stageID: config.ID, memberName: "branch",
		resultSchema: outputSchema,
	}
	reduce := func(ctx context.Context, rawInput json.RawMessage, raw []json.RawMessage) (json.RawMessage, error) {
		input, err := run.ParsePayload(rawInput)
		if err != nil {
			return nil, err
		}
		value, err := input.Decode[I]()
		if err != nil {
			return nil, fmt.Errorf("Fork %q input: %w", config.ID, err)
		}
		values, err := outputs.decode[B](ctx, raw)
		if err != nil {
			return nil, err
		}
		result, err := reducer(ctx, value, values)
		if err != nil {
			return nil, fmt.Errorf("Fork %q reducer: %w", config.ID, err)
		}
		return outputs.encodeResult(result)
	}
	return Stage{
		id: config.ID, kind: StageKindFork,
		inputSchema: inputSchema, outputSchema: outputSchema,
		fanout: fanoutStage{
			source: source, windowSize: config.WindowSize,
			outputSchema: branchSchema, complete: reduce,
		},
	}, nil
}
