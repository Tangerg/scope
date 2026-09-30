package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"math"

	agent "github.com/Tangerg/scope/agent"
)

// ForkReducer combines branch outputs in declaration order. It must be
// bounded, deterministic, side-effect-free, honor ctx cancellation, and never
// retain the slice. Context is not a source of domain input.
type ForkReducer[B, O any] func(ctx context.Context, branchOutputs []B) (O, error)

type ForkBranch struct {
	// ID is unique within this Fork Stage and stable across restoration.
	ID string

	Deployment agent.Deployment

	// Budget is permanently allocated from the parent when the branch starts.
	Budget agent.Budget

	Capabilities agent.CapabilitySet
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

	Reduce ForkReducer[B, O]
}

type forkSource struct{ branches []fanoutMember }

func newForkSource(stageID string, inputSchema, branchSchema agent.Schema, declared []ForkBranch) (forkSource, error) {
	branches := make([]fanoutMember, 0, len(declared))
	seen := make(map[string]struct{}, len(declared))
	for index, branch := range declared {
		binding, valid := newChildBinding(branch.Deployment, branch.Budget, branch.Capabilities)
		if !agent.ValidQualifiedName(branch.ID) || !valid {
			return forkSource{}, fmt.Errorf("%w: Fork %q Branches[%d]", ErrInvalidStage, stageID, index)
		}
		if _, duplicate := seen[branch.ID]; duplicate {
			return forkSource{}, fmt.Errorf("%w: Fork %q has duplicate branch %q", ErrInvalidStage, stageID, branch.ID)
		}
		descriptor := branch.Deployment.Descriptor()
		if !schemasEqual(inputSchema, descriptor.InputSchema()) ||
			!schemasEqual(branchSchema, descriptor.OutputSchema()) {
			return forkSource{}, fmt.Errorf("%w: Fork %q branch %q schema mismatch", ErrInvalidStage, stageID, branch.ID)
		}
		seen[branch.ID] = struct{}{}
		branches = append(branches, fanoutMember{id: branch.ID, binding: binding})
	}
	return forkSource{branches: branches}, nil
}

func (f forkSource) count(ctx context.Context, _ json.RawMessage) (uint32, error) {
	return uint32(len(f.branches)), ctx.Err()
}

func (f forkSource) windowInputs(ctx context.Context, raw json.RawMessage, start, windowSize uint32) ([]agent.Payload, uint32, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	count := uint32(len(f.branches))
	if start > count {
		return nil, 0, ErrInvalidExecutionState
	}
	input, err := agent.ParsePayload(raw)
	if err != nil {
		return nil, 0, err
	}
	var inputs []agent.Payload
	if size := min(windowSize, count-start); size > 0 {
		inputs = make([]agent.Payload, size)
	}
	for index := range inputs {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		inputs[index] = input
	}
	return inputs, count, nil
}

func (f forkSource) member(index uint32) (fanoutMember, bool) {
	if uint64(index) >= uint64(len(f.branches)) {
		return fanoutMember{}, false
	}
	return f.branches[index], true
}

func (f forkSource) bindings() []childBinding {
	bindings := make([]childBinding, len(f.branches))
	for index, branch := range f.branches {
		bindings[index] = branch.binding
	}
	return bindings
}

func (f forkSource) topology(inputSchema, outputSchema agent.Schema) ([]BindingTopology, uint32) {
	bindings := make([]BindingTopology, len(f.branches))
	for index, branch := range f.branches {
		bindings[index] = branch.binding.topology(BindingRoleBranch, branch.id, inputSchema, outputSchema)
	}
	return bindings, 0
}

// Fork constructs one windowed managed fan-out Stage. Branch inputs and
// outputs are homogeneous; heterogeneous work can be wrapped by child
// Workflows that expose a shared contract.
func Fork[I, B, O any](config ForkConfig[I, B, O]) (Stage, error) {
	if !agent.ValidQualifiedName(config.ID) || len(config.Branches) == 0 ||
		uint64(len(config.Branches)) > math.MaxUint32 || config.Reduce == nil ||
		config.WindowSize == 0 || uint64(config.WindowSize) > uint64(len(config.Branches)) {
		return Stage{}, ErrInvalidStage
	}
	inputSchema, err := agent.SchemaFor[I]()
	if err != nil {
		return Stage{}, fmt.Errorf("%w: Fork %q input schema: %w", ErrInvalidStage, config.ID, err)
	}
	branchSchema, err := agent.SchemaFor[B]()
	if err != nil {
		return Stage{}, fmt.Errorf("%w: Fork %q branch schema: %w", ErrInvalidStage, config.ID, err)
	}
	outputSchema, err := agent.SchemaFor[O]()
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
		memberSchema: branchSchema, resultSchema: outputSchema,
	}
	reduce := func(ctx context.Context, raw []json.RawMessage) (json.RawMessage, error) {
		values, err := outputs.decode[B](ctx, raw)
		if err != nil {
			return nil, err
		}
		result, err := reducer(ctx, values)
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

func schemasEqual(left, right agent.Schema) bool {
	return left.Valid() && right.Valid() && string(left.JSON()) == string(right.JSON())
}
