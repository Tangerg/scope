package workflow

import (
	"encoding/json"
	"fmt"
	"math"

	agent "github.com/Tangerg/scope/agent"
)

// ForkReducer combines branch outputs in declaration order. It must be
// bounded, deterministic, side-effect-free, and must not retain the slice.
type ForkReducer[B, O any] func(branchOutputs []B) (O, error)

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

type forkBranch struct {
	id      string
	binding childBinding
}

type forkStage struct {
	branches     []forkBranch
	windowSize   uint32
	branchSchema agent.Schema
	reduce       func([]json.RawMessage) (json.RawMessage, error)
}

func (f forkStage) valid() bool {
	if len(f.branches) == 0 || f.windowSize == 0 || !f.branchSchema.Valid() ||
		uint64(f.windowSize) > uint64(len(f.branches)) || f.reduce == nil {
		return false
	}
	seen := make(map[string]struct{}, len(f.branches))
	for _, branch := range f.branches {
		if !validStageID(branch.id) || !branch.binding.valid() {
			return false
		}
		if _, duplicate := seen[branch.id]; duplicate {
			return false
		}
		seen[branch.id] = struct{}{}
	}
	return true
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
	branches := make([]forkBranch, 0, len(config.Branches))
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
		branches = append(branches, forkBranch{
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
	reduce := func(raw []json.RawMessage) (json.RawMessage, error) {
		values, err := decoder.decode[B](raw)
		if err != nil {
			return nil, err
		}
		result, err := reducer(values)
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
		fork: forkStage{
			branches: branches, windowSize: config.WindowSize,
			branchSchema: branchSchema, reduce: reduce,
		},
	}, nil
}

func schemasEqual(left, right agent.Schema) bool {
	return left.Valid() && right.Valid() && string(left.JSON()) == string(right.JSON())
}
