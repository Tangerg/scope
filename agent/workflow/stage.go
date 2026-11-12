package workflow

import (
	"context"
	"fmt"

	"github.com/Tangerg/scope/agent/internal/enum"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/jsonschema"
)

const (
	failureSuffixCaseUnknown      = "case_unknown"
	failureSuffixNotCompleted     = "not_completed"
	failureSuffixMaxItemsExceeded = "max_items_exceeded"
)

type StageKind string

const (
	StageKindInvalid   StageKind = ""
	StageKindTransform StageKind = "transform"
	StageKindCall      StageKind = "call"
	StageKindSwitch    StageKind = "switch"
	StageKindFork      StageKind = "fork"
	StageKindMap       StageKind = "map"
	StageKindLoop      StageKind = "loop"
)

// encodeConforming encodes a typed Stage value and holds it to the schema
// derived for its type, which a custom codec can still produce outside of.
func encodeConforming[T any](schema jsonschema.Schema, value T) (run.Payload, error) {
	payload, err := run.EncodePayload(value)
	if err != nil {
		return run.Payload{}, fmt.Errorf("encode: %w", err)
	}
	if err := schema.Validate(payload.JSON()); err != nil {
		return run.Payload{}, fmt.Errorf("contract: %w", err)
	}
	return payload, nil
}

func (s StageKind) Valid() bool {
	return s == StageKindTransform || s == StageKindCall || s == StageKindSwitch || s == StageKindFork || s == StageKindMap || s == StageKindLoop
}

func (s StageKind) String() string { return enum.String(s) }

// TransformFunc is a bounded, deterministic, side-effect-free reduction. It
// must honor ctx cancellation during CPU work. External work belongs in a Call
// stage that starts a child Process; ctx is not a source of domain input.
type TransformFunc[I, O any] func(ctx context.Context, input I) (O, error)

type transformStage func(context.Context, run.Payload) (run.Payload, error)

// Stage is an immutable operation in one Workflow definition. Values can only
// be constructed by this package, keeping the execution algebra closed.
type Stage struct {
	id           string
	kind         StageKind
	inputSchema  jsonschema.Schema
	outputSchema jsonschema.Schema
	transform    transformStage
	call         run.ChildBinding
	switcher     switchStage
	fanout       fanoutStage
	loop         loopStage
}

// CallConfig declares one exact child Program and its non-renewable
// Framework resource allocation.
type CallConfig struct {
	ID    string
	Child run.ChildBinding
}

// Transform constructs one typed pure Stage. JSON schemas derived from I and O
// remain the authoritative erased boundary used by the Workflow definition.
func Transform[I, O any](id string, transform TransformFunc[I, O]) (Stage, error) {
	if !run.ValidQualifiedName(id) || transform == nil {
		return Stage{}, ErrInvalidStage
	}
	inputSchema, err := jsonschema.For[I]()
	if err != nil {
		return Stage{}, fmt.Errorf("%w: transform %q input schema: %w", ErrInvalidStage, id, err)
	}
	outputSchema, err := jsonschema.For[O]()
	if err != nil {
		return Stage{}, fmt.Errorf("%w: transform %q output schema: %w", ErrInvalidStage, id, err)
	}
	apply := func(ctx context.Context, input run.Payload) (run.Payload, error) {
		// The value's producer and Restore already hold it to this Stage's input.
		decoded, err := input.Decode[I]()
		if err != nil {
			return run.Payload{}, fmt.Errorf("transform %q decode input: %w", id, err)
		}
		output, err := transform(ctx, decoded)
		if err != nil {
			return run.Payload{}, fmt.Errorf("transform %q: %w", id, err)
		}
		erased, err := encodeConforming(outputSchema, output)
		if err != nil {
			return run.Payload{}, fmt.Errorf("transform %q output: %w", id, err)
		}
		return erased, nil
	}
	return Stage{
		id: id, kind: StageKindTransform,
		inputSchema: inputSchema, outputSchema: outputSchema, transform: apply,
	}, nil
}

// Call constructs one managed child-Process Stage. No child Process is created
// until the Workflow Execution returns a Framework NewChildStartEffect Effect.
func Call(config CallConfig) (Stage, error) {
	binding := config.Child
	if !run.ValidQualifiedName(config.ID) || !binding.Valid() {
		return Stage{}, ErrInvalidStage
	}
	descriptor := binding.Program.Descriptor()
	return Stage{
		id: config.ID, kind: StageKindCall,
		inputSchema: descriptor.InputSchema(), outputSchema: descriptor.OutputSchema(),
		call: binding,
	}, nil
}

func (s Stage) Valid() bool { return s.kind.Valid() }

func (s Stage) hasIdenticalInputSchema(schema jsonschema.Schema) bool {
	return s.inputSchema.Equal(schema)
}

// fanoutChild returns the fan-out member at index and the key of the child
// Process that runs it.
func (s Stage) fanoutChild(index uint32) (fanoutMember, run.ChildKey, error) {
	member, found := s.fanout.source.member(index)
	if !found {
		return fanoutMember{}, run.ChildKey{}, ErrInvalidExecutionState
	}
	key, err := workflowChildKey(string(s.kind), s.id, member.id)
	return member, key, err
}

func (s Stage) fanoutMemberLabel(index uint32) string {
	member, present := s.fanout.source.member(index)
	if !present {
		panic("workflow: fanout diagnostic refers to an absent member")
	}
	return s.kind.fanoutMemberNoun() + " " + member.id
}

func (s Stage) fanoutFailureCode(suffix string) string {
	return s.failureCode(s.kind.fanoutMemberNoun() + "_" + suffix)
}

func (s Stage) failureCode(suffix string) string {
	return fmt.Sprintf("workflow.%s.%s", string(s.kind), suffix)
}

// fanoutMemberNoun names one member of a fan-out Stage of this kind.
func (s StageKind) fanoutMemberNoun() string {
	if s == StageKindFork {
		return "branch"
	}
	return "item"
}

// carriesCallbacks reports whether this Stage runs opaque Host code, whose
// identity a Program's Revision must cover; Call and Map only bind Programs.
func (s Stage) carriesCallbacks() bool {
	return s.kind != StageKindCall && s.kind != StageKindMap
}

func (s Stage) fanoutOutcome(index uint32, outcome run.Termination) (*run.Failure, run.Payload, error) {
	subject := string(s.kind) + " Stage " + s.id + " " + s.fanoutMemberLabel(index)
	return childOutcome(outcome, s.fanoutFailureCode, subject)
}

// childOutcome classifies a finished child that subject names: the Output it
// completed with, or the Failure that ends the Workflow. Single-child and
// fan-out bindings both decide with it and differ only in code and subject.
func childOutcome(outcome run.Termination, code func(suffix string) string, subject string) (*run.Failure, run.Payload, error) {
	if outcome.Status() != run.StatusCompleted {
		if failure, failed := outcome.Failure(); failed {
			return &failure, run.Payload{}, nil
		}
		failure, err := run.NewFailure(run.FailureKindExternal, code(failureSuffixNotCompleted), subject+" terminated with status "+outcome.Status().String())
		return &failure, run.Payload{}, err
	}
	// A validated Completed termination always carries Output.
	output, _ := outcome.Output()
	return nil, output, nil
}

// childBindings returns every child binding of the Stage in topology order.
func (s Stage) childBindings() []run.ChildBinding {
	switch s.kind {
	case StageKindCall:
		return []run.ChildBinding{s.call}
	case StageKindSwitch:
		bindings := make([]run.ChildBinding, len(s.switcher.cases))
		for index, candidate := range s.switcher.cases {
			bindings[index] = candidate.binding
		}
		return bindings
	case StageKindFork, StageKindMap:
		return s.fanout.source.bindings()
	case StageKindLoop:
		return []run.ChildBinding{s.loop.binding}
	default:
		return nil
	}
}

func (s Stage) topology() StageTopology {
	projected := StageTopology{
		ID: s.id, Kind: s.kind,
		InputSchema: s.inputSchema, OutputSchema: s.outputSchema,
	}
	switch s.kind {
	case StageKindCall:
		projected.Bindings = []BindingTopology{
			bindingTopology(s.call, BindingRoleCall, "", s.inputSchema, s.outputSchema),
		}
	case StageKindSwitch:
		projected.Bindings = make([]BindingTopology, len(s.switcher.cases))
		for index, candidate := range s.switcher.cases {
			projected.Bindings[index] = bindingTopology(candidate.binding,
				BindingRoleCase, candidate.id, s.inputSchema, s.outputSchema,
			)
		}
	case StageKindFork, StageKindMap:
		projected.WindowSize = s.fanout.windowSize
		projected.Bindings, projected.MaxItems = s.fanout.source.topology(s.inputSchema, s.fanout.memberOutputSchema)
	case StageKindLoop:
		projected.MaxIterations = new(s.loop.maxIterations)
		projected.Bindings = []BindingTopology{bindingTopology(s.loop.binding,
			BindingRoleBody, "", s.inputSchema, s.inputSchema,
		)}
	}
	return projected
}
