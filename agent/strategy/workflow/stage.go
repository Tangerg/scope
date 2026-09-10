package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	agent "github.com/Tangerg/scope/agent"
)

const maxStageIDBytes = 128

// StageKind is the operation kind owned by a sealed Workflow Stage.
type StageKind string

const (
	// StageKindInvalid is the invalid zero value.
	StageKindInvalid StageKind = ""
	// StageKindTransform identifies a pure value transformation.
	StageKindTransform StageKind = "transform"
	// StageKindCall identifies one exact child Process call.
	StageKindCall StageKind = "call"
	// StageKindSwitch identifies pure selection among exact child cases.
	StageKindSwitch StageKind = "switch"
	// StageKindFork identifies bounded homogeneous branch fan-out.
	StageKindFork StageKind = "fork"
	// StageKindMap identifies bounded homogeneous item fan-out.
	StageKindMap StageKind = "map"
	// StageKindLoop identifies bounded at-least-once child iteration.
	StageKindLoop StageKind = "loop"
)

// TransformFunc is a bounded, deterministic, side-effect-free reduction. It
// must honor ctx cancellation during CPU work. External work belongs in a Call
// stage that starts a child Process; ctx is not a source of domain input.
type TransformFunc[I, O any] func(ctx context.Context, input I) (O, error)

type transformStage func(context.Context, json.RawMessage) (json.RawMessage, error)

type childBinding struct {
	deploymentRef agent.DeploymentRef
	budget        agent.Budget
	capabilities  agent.CapabilitySet
}

// Stage is an immutable operation in one Workflow Definition. Values can only
// be constructed by this package, keeping the execution algebra closed.
type Stage struct {
	id           string
	kind         StageKind
	inputSchema  agent.Schema
	outputSchema agent.Schema
	transform    transformStage
	call         childBinding
	switcher     switchStage
	fork         forkStage
	mapper       mapStage
	loop         loopStage
}

// CallConfig declares one exact child Deployment and its non-renewable
// Framework resource allocation.
type CallConfig struct {
	// ID is unique within the Workflow and remains stable across restoration.
	ID string

	// Deployment is the exact child behavior binding. The Stage retains only
	// its immutable DeploymentRef and Descriptor schemas.
	Deployment agent.Deployment

	// Budget is permanently allocated from the parent when the child starts.
	Budget agent.Budget

	// Capabilities is the attenuated authority set granted to the child.
	Capabilities agent.CapabilitySet
}

// Transform constructs one typed pure Stage. JSON schemas derived from I and O
// remain the authoritative erased boundary used by the Workflow Definition.
func Transform[I, O any](id string, transform TransformFunc[I, O]) (Stage, error) {
	if !validStageID(id) || transform == nil {
		return Stage{}, ErrInvalidStage
	}
	inputSchema, err := agent.SchemaFor[I]()
	if err != nil {
		return Stage{}, fmt.Errorf("%w: transform %q input schema: %w", ErrInvalidStage, id, err)
	}
	outputSchema, err := agent.SchemaFor[O]()
	if err != nil {
		return Stage{}, fmt.Errorf("%w: transform %q output schema: %w", ErrInvalidStage, id, err)
	}
	apply := func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
		input, err := agent.ParseInput(raw)
		if err != nil {
			return nil, fmt.Errorf("transform %q input: %w", id, err)
		}
		if validateInputErr := inputSchema.ValidateInput(input); validateInputErr != nil {
			return nil, fmt.Errorf("transform %q input contract: %w", id, validateInputErr)
		}
		decoded, err := input.Decode[I]()
		if err != nil {
			return nil, fmt.Errorf("transform %q decode input: %w", id, err)
		}
		output, err := transform(ctx, decoded)
		if err != nil {
			return nil, fmt.Errorf("transform %q: %w", id, err)
		}
		erased, err := agent.EncodeOutput(output)
		if err != nil {
			return nil, fmt.Errorf("transform %q encode output: %w", id, err)
		}
		if err := outputSchema.ValidateOutput(erased); err != nil {
			return nil, fmt.Errorf("transform %q output contract: %w", id, err)
		}
		return erased.JSON(), nil
	}
	return Stage{
		id: id, kind: StageKindTransform,
		inputSchema: inputSchema, outputSchema: outputSchema, transform: apply,
	}, nil
}

// Call constructs one managed child-Process Stage. No child Process is created
// until the Workflow Execution returns a Framework StartChild Effect.
func Call(config CallConfig) (Stage, error) {
	if !validStageID(config.ID) || !config.Deployment.Valid() ||
		!config.Budget.Valid() || !config.Capabilities.Valid() {
		return Stage{}, ErrInvalidStage
	}
	descriptor := config.Deployment.Descriptor()
	return Stage{
		id: config.ID, kind: StageKindCall,
		inputSchema: descriptor.InputSchema(), outputSchema: descriptor.OutputSchema(),
		call: childBinding{
			deploymentRef: config.Deployment.DeploymentRef(), budget: config.Budget,
			capabilities: config.Capabilities,
		},
	}, nil
}

func (s Stage) Valid() bool {
	if !validStageID(s.id) || !s.inputSchema.Valid() || !s.outputSchema.Valid() {
		return false
	}
	behaviorKind, exclusivelyOwned := s.behaviorKind()
	return exclusivelyOwned && behaviorKind == s.kind
}

func (s Stage) behaviorKind() (StageKind, bool) {
	behaviors := [...]struct {
		kind   StageKind
		active bool
	}{
		{kind: StageKindTransform, active: s.transform != nil},
		{kind: StageKindCall, active: s.call.valid()},
		{kind: StageKindSwitch, active: s.switcher.valid()},
		{kind: StageKindFork, active: s.fork.valid()},
		{kind: StageKindMap, active: s.mapper.valid()},
		{kind: StageKindLoop, active: s.loop.valid()},
	}
	selected := StageKindInvalid
	for _, behavior := range behaviors {
		if !behavior.active {
			continue
		}
		if selected != StageKindInvalid {
			return StageKindInvalid, false
		}
		selected = behavior.kind
	}
	return selected, selected != StageKindInvalid
}

func (s Stage) accepts(schema agent.Schema) bool {
	return schema.Valid() && bytes.Equal(s.inputSchema.JSON(), schema.JSON())
}

func validStageID(value string) bool {
	if len(value) == 0 || len(value) > maxStageIDBytes || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for index := 1; index < len(value); index++ {
		character := value[index]
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' ||
			character == '.' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}
