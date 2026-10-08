package workflow

import (
	"context"
	"encoding/json"
	"fmt"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/internal/stepfail"
)

const invalidEnumName = "invalid"

const (
	failureSuffixCaseUnknown       = "case_unknown"
	failureSuffixUnresolvedEffects = "unresolved_effects"
	failureSuffixNotCompleted      = "not_completed"
	failureSuffixOutputMissing     = "output_missing"
	failureSuffixMaxItemsExceeded  = "max_items_exceeded"
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

func (s StageKind) Valid() bool {
	return s == StageKindTransform || s == StageKindCall || s == StageKindSwitch || s == StageKindFork || s == StageKindMap || s == StageKindLoop
}

func (s StageKind) String() string {
	if !s.Valid() {
		return invalidEnumName
	}
	return string(s)
}

// TransformFunc is a bounded, deterministic, side-effect-free reduction. It
// must honor ctx cancellation during CPU work. External work belongs in a Call
// stage that starts a child Process; ctx is not a source of domain input.
type TransformFunc[I, O any] func(ctx context.Context, input I) (O, error)

type transformStage func(context.Context, json.RawMessage) (json.RawMessage, error)

type childBinding struct {
	deployment   agent.Deployment
	budget       agent.Budget
	capabilities agent.CapabilitySet
}

func newChildBinding(deployment agent.Deployment, budget agent.Budget, capabilities agent.CapabilitySet) (childBinding, bool) {
	if !deployment.Valid() || !capabilities.Valid() {
		return childBinding{}, false
	}
	return childBinding{deployment: deployment, budget: budget, capabilities: capabilities}, true
}

func (c childBinding) deploymentRef() agent.DeploymentRef { return c.deployment.DeploymentRef() }

func (c childBinding) topology(
	role BindingRole,
	id string,
	inputSchema agent.Schema,
	outputSchema agent.Schema,
) BindingTopology {
	return BindingTopology{
		Role: role, ID: id, DeploymentRef: c.deploymentRef(),
		InputSchema: inputSchema, OutputSchema: outputSchema,
		Budget: c.budget, Capabilities: c.capabilities,
	}
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
	fanout       fanoutStage
	loop         loopStage
}

// CallConfig declares one exact child Deployment and its non-renewable
// Framework resource allocation.
type CallConfig struct {
	// ID is unique within the Workflow and remains stable across restoration.
	ID string

	// Deployment is the exact child behavior binding. The Workflow owns it as
	// one of its ChildDeployments.
	Deployment agent.Deployment

	// Budget is permanently allocated from the parent when the child starts.
	Budget agent.Budget

	Capabilities agent.CapabilitySet
}

// Transform constructs one typed pure Stage. JSON schemas derived from I and O
// remain the authoritative erased boundary used by the Workflow Definition.
func Transform[I, O any](id string, transform TransformFunc[I, O]) (Stage, error) {
	if !agent.ValidQualifiedName(id) || transform == nil {
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
		input, err := agent.ParsePayload(raw)
		if err != nil {
			return nil, fmt.Errorf("transform %q input: %w", id, err)
		}
		// The value's producer and Restore already hold it to this Stage's input.
		decoded, err := input.Decode[I]()
		if err != nil {
			return nil, fmt.Errorf("transform %q decode input: %w", id, err)
		}
		output, err := transform(ctx, decoded)
		if err != nil {
			return nil, fmt.Errorf("transform %q: %w", id, err)
		}
		erased, err := agent.EncodePayload(output)
		if err != nil {
			return nil, fmt.Errorf("transform %q encode output: %w", id, err)
		}
		if err := outputSchema.Validate(erased.JSON()); err != nil {
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
// until the Workflow Execution returns a Framework NewChildStartEffect Effect.
func Call(config CallConfig) (Stage, error) {
	binding, valid := newChildBinding(config.Deployment, config.Budget, config.Capabilities)
	if !agent.ValidQualifiedName(config.ID) || !valid {
		return Stage{}, ErrInvalidStage
	}
	descriptor := config.Deployment.Descriptor()
	return Stage{
		id: config.ID, kind: StageKindCall,
		inputSchema: descriptor.InputSchema(), outputSchema: descriptor.OutputSchema(),
		call: binding,
	}, nil
}

func (s Stage) Valid() bool { return s.kind.Valid() }

func (s Stage) hasIdenticalInputSchema(schema agent.Schema) bool {
	return s.inputSchema.Equal(schema)
}

func (s Stage) fanoutMemberLabel(index uint32) string {
	member, present := s.fanout.source.member(index)
	if !present {
		panic("workflow: fanout diagnostic refers to an absent member")
	}
	return s.fanoutMemberNoun() + " " + member.id
}

func (s Stage) fanoutFailureCode(suffix string) string {
	return s.failureCode(s.fanoutMemberNoun() + "_" + suffix)
}

func (s Stage) failureCode(suffix string) string {
	return fmt.Sprintf("workflow.%s.%s", string(s.kind), suffix)
}

func (s Stage) fanoutMemberNoun() string {
	if s.kind == StageKindFork {
		return "branch"
	}
	return "item"
}

func (s Stage) fanoutOutcome(index uint32, outcome agent.ChildOutcome) (*agent.Failure, json.RawMessage, error) {
	subject := string(s.kind) + " Stage " + s.id + " " + s.fanoutMemberLabel(index)
	failure, output, err := childOutcome(outcome, s.fanoutFailureCode, subject)
	if failure != nil || err != nil {
		return failure, nil, err
	}
	return nil, output.JSON(), nil
}

// childOutcome classifies a finished child that subject names: the Output it
// completed with, or the Failure that ends the Workflow. Single-child and
// fan-out bindings both decide with it and differ only in code and subject.
func childOutcome(outcome agent.ChildOutcome, code func(suffix string) string, subject string) (*agent.Failure, agent.Payload, error) {
	if !outcome.SubtreeResolved() {
		failure, err := stepfail.Failure(agent.FailureKindExternal, code(failureSuffixUnresolvedEffects), subject+" has unresolved subtree Effects")
		return &failure, agent.Payload{}, err
	}
	result := outcome.Result()
	if result.Termination().Status() != agent.StatusCompleted {
		if failure, failed := result.Termination().Failure(); failed {
			return &failure, agent.Payload{}, nil
		}
		failure, err := stepfail.Failure(agent.FailureKindExternal, code(failureSuffixNotCompleted), subject+" terminated with status "+result.Termination().Status().String())
		return &failure, agent.Payload{}, err
	}
	output, present := result.Termination().Output()
	if !present {
		failure, err := stepfail.Failure(agent.FailureKindContract, code(failureSuffixOutputMissing), subject+" returned no Output")
		return &failure, agent.Payload{}, err
	}
	return nil, output, nil
}

// childBindings returns every child binding of the Stage in topology order.
func (s Stage) childBindings() []childBinding {
	switch s.kind {
	case StageKindCall:
		return []childBinding{s.call}
	case StageKindSwitch:
		bindings := make([]childBinding, len(s.switcher.cases))
		for index, candidate := range s.switcher.cases {
			bindings[index] = candidate.binding
		}
		return bindings
	case StageKindFork, StageKindMap:
		return s.fanout.source.bindings()
	case StageKindLoop:
		return []childBinding{s.loop.binding}
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
			s.call.topology(BindingRoleCall, "", s.inputSchema, s.outputSchema),
		}
	case StageKindSwitch:
		projected.Bindings = make([]BindingTopology, len(s.switcher.cases))
		for index, candidate := range s.switcher.cases {
			projected.Bindings[index] = candidate.binding.topology(
				BindingRoleCase, candidate.id, s.inputSchema, s.outputSchema,
			)
		}
	case StageKindFork, StageKindMap:
		projected.WindowSize = s.fanout.windowSize
		projected.Bindings, projected.MaxItems = s.fanout.source.topology(s.inputSchema, s.fanout.outputSchema)
	case StageKindLoop:
		projected.MaxIterations = new(s.loop.maxIterations)
		projected.Bindings = []BindingTopology{s.loop.binding.topology(
			BindingRoleBody, "", s.inputSchema, s.inputSchema,
		)}
	}
	return projected
}
