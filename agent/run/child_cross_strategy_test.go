package run

import (
	startrand "crypto/rand"

	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/core/jsonschema"
)

func TestEngineStartsChildFromAnotherStrategyThroughExactResolver(t *testing.T) {
	childDeployment := newChildTestDeployment(t)
	parentDeployment := newCrossParentDeployment(t, childDeployment.ProgramRef())
	resolver := deploymentMapResolver{childDeployment.ProgramRef(): childDeployment}
	engine, err := New(t.Context(), Config{Store: NewMemoryStore(), ProgramResolver: resolver})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := EncodePayload(struct{}{})
	parent, err := engine.Start(context.Background(), parentDeployment, StartRequest[Payload]{ID: StartID{Scope: "test", Key: startrand.Text()}, Input: input})
	if err != nil {
		t.Fatal(err)
	}
	output := childTestResult(t, mustAwait(t, parent))
	if len(output.ChildIDs) != 1 || output.Failures != 0 {
		t.Fatalf("cross-Strategy output = %#v", output)
	}
	childID, _ := ParseProcessID(output.ChildIDs[0])
	child, found := engine.Process(childID)
	if !found {
		t.Fatal("resolved child is missing")
	}
	if child.ProgramRef() != childDeployment.ProgramRef() ||
		child.Relation().RootID() != parent.Relation().ProcessID() || child.Relation().Depth() != 1 {
		t.Fatalf("resolved child binding = %#v, relation = %#v", child.ProgramRef(), child.Relation())
	}
	_ = mustAwait(t, child)
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
}

func TestEngineStartsAndRestoresBoundChildrenWithoutResolver(t *testing.T) {
	childDeployment := newChildTestDeployment(t)
	parentDeployment := newBoundCrossParentDeployment(t, childDeployment)
	engine, err := New(t.Context(), Config{Store: NewMemoryStore()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mustCloseEngine(t, engine) })
	input, _ := EncodePayload(struct{}{})
	parent, err := engine.Start(t.Context(), parentDeployment, StartRequest[Payload]{ID: StartID{Scope: "test", Key: startrand.Text()}, Input: input})
	if err != nil {
		t.Fatal(err)
	}
	output := childTestResult(t, mustAwait(t, parent))
	if len(output.ChildIDs) != 1 || output.Failures != 0 {
		t.Fatalf("bound child output = %#v", output)
	}
	if joinErr := parent.Join(t.Context()); joinErr != nil {
		t.Fatal(joinErr)
	}
	tree, err := engine.CaptureTree(t.Context(), parent.Relation().ProcessID())
	if err != nil {
		t.Fatal(err)
	}
	if len(tree.ProcessSnapshots()) != 2 {
		t.Fatalf("captured %d Processes, want parent and child", len(tree.ProcessSnapshots()))
	}

	restoredEngine, err := New(t.Context(), Config{Store: newSnapshotTestCommitter(tree)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mustCloseEngine(t, restoredEngine) })
	if validateErr := restoredEngine.ValidateRestorableTree(t.Context(), parentDeployment, tree); validateErr != nil {
		t.Fatalf("bound child was not restorable without a resolver: %v", validateErr)
	}
	unbound := newCrossParentDeployment(t, childDeployment.ProgramRef())
	if unbound.ProgramRef() == parentDeployment.ProgramRef() {
		t.Fatal("static binding did not change the parent identity")
	}
}

func TestEngineRejectsResolverBindingMismatch(t *testing.T) {
	childDeployment := newChildTestDeployment(t)
	parentDeployment := newCrossParentDeployment(t, childDeployment.ProgramRef())
	resolver := deploymentMapResolver{childDeployment.ProgramRef(): parentDeployment}
	engine, err := New(t.Context(), Config{Store: NewMemoryStore(), ProgramResolver: resolver})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := EncodePayload(struct{}{})
	parent, err := engine.Start(context.Background(), parentDeployment, StartRequest[Payload]{ID: StartID{Scope: "test", Key: startrand.Text()}, Input: input})
	if err != nil {
		t.Fatal(err)
	}
	output := childTestResult(t, mustAwait(t, parent))
	if output.Failures != 1 || len(output.ChildIDs) != 0 ||
		len(output.FailureCodes) != 1 || output.FailureCodes[0] != "engine.child.deployment_unavailable" {
		t.Fatalf("mismatched resolver output = %#v", output)
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
}

func TestEngineContainsProgramResolverPanic(t *testing.T) {
	childDeployment := newChildTestDeployment(t)
	parentDeployment := newCrossParentDeployment(t, childDeployment.ProgramRef())
	resolver := programResolverFunc(func(ProgramRef) (Program[Payload, Payload], error) {
		panic("resolver failure")
	})
	engine, err := New(t.Context(), Config{Store: NewMemoryStore(), ProgramResolver: resolver})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := EncodePayload(struct{}{})
	parent, err := engine.Start(context.Background(), parentDeployment, StartRequest[Payload]{ID: StartID{Scope: "test", Key: startrand.Text()}, Input: input})
	if err != nil {
		t.Fatal(err)
	}
	output := childTestResult(t, mustAwait(t, parent))
	if output.Failures != 1 || len(output.ChildIDs) != 0 ||
		len(output.FailureCodes) != 1 || output.FailureCodes[0] != "engine.child.deployment_unavailable" {
		t.Fatalf("panicking resolver output = %#v", output)
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
}

func TestEngineBypassesResolverForSameDeploymentChild(t *testing.T) {
	deployment := newChildTestDeployment(t)
	var calls atomic.Uint32
	resolver := programResolverFunc(func(ProgramRef) (Program[Payload, Payload], error) {
		calls.Add(1)
		return Program[Payload, Payload]{}, errors.New("same Program unexpectedly resolved")
	})
	engine, err := New(t.Context(), Config{Store: NewMemoryStore(), ProgramResolver: resolver})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := EncodePayload(childTestInput{Mode: "parent"})
	parent, err := engine.Start(context.Background(), deployment, StartRequest[Payload]{ID: StartID{Scope: "test", Key: startrand.Text()}, Input: input})
	if err != nil {
		t.Fatal(err)
	}
	output := childTestResult(t, mustAwait(t, parent))
	if len(output.ChildIDs) != 1 || output.Failures != 0 {
		t.Fatalf("same-Program output = %#v", output)
	}
	childID, _ := ParseProcessID(output.ChildIDs[0])
	child, found := engine.Process(childID)
	if !found {
		t.Fatal("same-Program child is missing")
	}
	_ = mustAwait(t, child)
	if got := calls.Load(); got != 0 {
		t.Fatalf("same-Program resolver calls = %d, want 0", got)
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
}

type deploymentMapResolver map[ProgramRef]Program[Payload, Payload]

func (d deploymentMapResolver) Resolve(reference ProgramRef) (Program[Payload, Payload], error) {
	deployment, found := d[reference]
	if !found {
		return Program[Payload, Payload]{}, errors.New("deployment not found")
	}
	return deployment, nil
}

type programResolverFunc func(ProgramRef) (Program[Payload, Payload], error)

func (p programResolverFunc) Resolve(reference ProgramRef) (Program[Payload, Payload], error) {
	return p(reference)
}

type crossParentDefinition struct {
	descriptor Descriptor
	target     ProgramRef
	bound      []Program[Payload, Payload]
}

func newCrossParentDeployment(t *testing.T, target ProgramRef) Program[Payload, Payload] {
	t.Helper()
	return newCrossParent(t, &crossParentDefinition{target: target})
}

// newBoundCrossParentDeployment binds child in the parent's own configuration
// instead of naming it as runtime input.
func newBoundCrossParentDeployment(t *testing.T, child Program[Payload, Payload]) Program[Payload, Payload] {
	t.Helper()
	return newCrossParent(t, &crossParentDefinition{target: child.ProgramRef(), bound: []Program[Payload, Payload]{child}})
}

func newCrossParent(t *testing.T, definition *crossParentDefinition) Program[Payload, Payload] {
	t.Helper()
	inputSchema, _ := jsonschema.For[struct{}]()
	outputSchema, _ := jsonschema.For[childTestOutput]()
	descriptor, err := NewDescriptor(DescriptorConfig{
		Name: "test.cross_parent", Description: "Start a child owned by another Strategy.",
		InputSchema: inputSchema, OutputSchema: outputSchema,
	})
	if err != nil {
		t.Fatal(err)
	}
	definition.descriptor = descriptor
	deployment, err := Bind[Payload, Payload](BindingConfig{
		Definition:           definition,
		Dispatcher:           childTestDispatcher{},
		ImplementationDigest: ComputeDigest([]byte("cross-parent-implementation")),
		ConfigurationDigest:  ComputeDigest([]byte("cross-parent:" + definition.target.Digest().String())),
	})
	if err != nil {
		t.Fatal(err)
	}
	return deployment
}

func (c *crossParentDefinition) Descriptor() Descriptor { return c.descriptor }

func (c *crossParentDefinition) ChildPrograms() []Program[Payload, Payload] { return c.bound }

func (c *crossParentDefinition) Start(Payload) (Execution, error) {
	return &crossParentExecution{target: c.target}, nil
}

func (c *crossParentDefinition) Restore(ctx context.Context, state ExecutionState) (Execution, error) {
	if state.Kind() != "test.cross_parent" {
		return nil, ErrInvalidExecutionState
	}
	var phase uint8
	if err := jsonv2.Unmarshal(state.Payload(), &phase); err != nil {
		return nil, err
	}
	return &crossParentExecution{target: c.target, phase: phase}, nil
}

type crossParentExecution struct {
	target ProgramRef
	phase  uint8
}

func (c *crossParentExecution) Step(_ context.Context, signals []Signal) (Transition, error) {
	if c.phase == 0 {
		input, _ := EncodePayload(childTestInput{Mode: "leaf"})
		key, _ := ParseChildKey("other-strategy")
		effect, err := NewChildStartEffect(childTestSpec(key, c.target, input))
		if err != nil {
			return Transition{}, err
		}
		c.phase = 1
		return Continue(0, effect)
	}
	if c.phase != 1 || len(signals) != 1 {
		return Transition{}, errors.New("cross-parent expected one child-start result")
	}
	start, err := signals[0].Frame[ChildStartResult]()
	if err != nil {
		return Transition{}, err
	}
	output := childTestOutput{}
	if childID, started := start.ProcessID(); started {
		output.ChildIDs = []string{childID.String()}
	} else if failure, failed := start.Failure(); failed {
		output.Failures = 1
		output.FailureCodes = []string{failure.Code()}
		output.FailureKinds = []FailureKind{failure.Kind()}
	}
	c.phase = 2
	erased, _ := EncodePayload(output)
	return Complete(1, erased)
}

func (c *crossParentExecution) Snapshot() (ExecutionState, error) {
	payload, _ := jsonv2.Marshal(c.phase)
	return ParseExecutionState("test.cross_parent", payload)
}
