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

// ForkBranch declares one exact managed child Deployment.
type ForkBranch struct {
	// ID is unique within this Fork Stage and stable across restoration.
	ID string

	// Deployment is the exact child behavior binding for this branch.
	Deployment agent.Deployment

	// Budget is permanently allocated from the parent when the branch starts.
	Budget agent.Budget

	// Capabilities is the attenuated authority set granted to the child.
	Capabilities agent.CapabilitySet
}

// ForkConfig declares a homogeneous fan-out and deterministic reduction.
type ForkConfig[I, B, O any] struct {
	// ID is unique within the Workflow and remains stable across restoration.
	ID string

	// Branches is a non-empty list in stable declaration order. Every branch
	// accepts I and produces B.
	Branches []ForkBranch

	// WindowSize is the positive number of branches started and settled as one
	// execution window before the next window begins.
	WindowSize uint32

	// Reduce combines all B values after every branch succeeds.
	Reduce ForkReducer[B, O]
}

type forkSource struct{ branches []fanoutMember }

func (f forkSource) count(json.RawMessage) (uint32, error) {
	return uint32(len(f.branches)), nil
}

func (f forkSource) windowInputs(raw json.RawMessage, start, windowSize uint32) ([]agent.Input, uint32, error) {
	count := uint32(len(f.branches))
	if start > count {
		return nil, 0, ErrInvalidExecutionState
	}
	input, err := agent.ParseInput(raw)
	if err != nil {
		return nil, 0, err
	}
	inputs := make([]agent.Input, min(windowSize, count-start))
	for index := range inputs {
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
	if !validStageID(config.ID) || len(config.Branches) == 0 ||
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
	branches := make([]fanoutMember, 0, len(config.Branches))
	seen := make(map[string]struct{}, len(config.Branches))
	for index, branch := range config.Branches {
		if !validStageID(branch.ID) || !branch.Deployment.Valid() ||
			!branch.Budget.Valid() || !branch.Capabilities.Valid() {
			return Stage{}, fmt.Errorf("%w: Fork %q Branches[%d]", ErrInvalidStage, config.ID, index)
		}
		if _, duplicate := seen[branch.ID]; duplicate {
			return Stage{}, fmt.Errorf("%w: Fork %q has duplicate branch %q", ErrInvalidStage, config.ID, branch.ID)
		}
		descriptor := branch.Deployment.Descriptor()
		if !schemasEqual(inputSchema, descriptor.InputSchema()) ||
			!schemasEqual(branchSchema, descriptor.OutputSchema()) {
			return Stage{}, fmt.Errorf("%w: Fork %q branch %q schema mismatch", ErrInvalidStage, config.ID, branch.ID)
		}
		seen[branch.ID] = struct{}{}
		branches = append(branches, fanoutMember{
			id: branch.ID,
			binding: childBinding{
				deploymentRef: branch.Deployment.DeploymentRef(), budget: branch.Budget,
				capabilities: branch.Capabilities,
			},
		})
	}
	reducer := config.Reduce
	decoder := fanoutOutputDecoder{
		stageName: "Fork", stageID: config.ID, memberName: "branch", schema: branchSchema,
	}
	reduce := func(ctx context.Context, raw []json.RawMessage) (json.RawMessage, error) {
		values, err := decoder.decode[B](raw)
		if err != nil {
			return nil, err
		}
		result, err := reducer(ctx, values)
		if err != nil {
			return nil, fmt.Errorf("Fork %q reducer: %w", config.ID, err)
		}
		erased, err := agent.EncodeOutput(result)
		if err != nil {
			return nil, fmt.Errorf("Fork %q encode result: %w", config.ID, err)
		}
		if err := outputSchema.ValidateOutput(erased); err != nil {
			return nil, fmt.Errorf("Fork %q result contract: %w", config.ID, err)
		}
		return erased.JSON(), nil
	}
	return Stage{
		id: config.ID, kind: StageKindFork,
		inputSchema: inputSchema, outputSchema: outputSchema,
		fanout: fanoutStage{
			source: forkSource{branches: branches}, windowSize: config.WindowSize,
			outputSchema: branchSchema, complete: reduce,
		},
	}, nil
}

func schemasEqual(left, right agent.Schema) bool {
	return left.Valid() && right.Valid() && string(left.JSON()) == string(right.JSON())
}
