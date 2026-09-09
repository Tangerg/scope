package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
)

func TestEngineStartsChildFromAnotherStrategyThroughExactResolver(t *testing.T) {
	childDeployment := newChildTestDeployment(t)
	parentDeployment := newCrossParentDeployment(t, childDeployment.DeploymentRef())
	resolver := deploymentMapResolver{childDeployment.DeploymentRef(): childDeployment}
	engine, err := NewEngine(EngineConfig{DeploymentResolver: resolver})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := EncodeInput(struct{}{})
	parent, err := engine.Start(context.Background(), parentDeployment, input)
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
	if child.DeploymentRef() != childDeployment.DeploymentRef() ||
		child.Relation().RootID() != parent.ID() || child.Relation().Depth() != 1 {
		t.Fatalf("resolved child binding = %#v, relation = %#v", child.DeploymentRef(), child.Relation())
	}
	_ = mustAwait(t, child)
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
}

func TestEngineRejectsResolverBindingMismatch(t *testing.T) {
	childDeployment := newChildTestDeployment(t)
	parentDeployment := newCrossParentDeployment(t, childDeployment.DeploymentRef())
	resolver := deploymentMapResolver{childDeployment.DeploymentRef(): parentDeployment}
	engine, err := NewEngine(EngineConfig{DeploymentResolver: resolver})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := EncodeInput(struct{}{})
	parent, err := engine.Start(context.Background(), parentDeployment, input)
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

func TestEngineContainsDeploymentResolverPanic(t *testing.T) {
	childDeployment := newChildTestDeployment(t)
	parentDeployment := newCrossParentDeployment(t, childDeployment.DeploymentRef())
	resolver := deploymentResolverFunc(func(DeploymentRef) (Deployment, error) {
		panic("resolver failure")
	})
	engine, err := NewEngine(EngineConfig{DeploymentResolver: resolver})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := EncodeInput(struct{}{})
	parent, err := engine.Start(context.Background(), parentDeployment, input)
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
	resolver := deploymentResolverFunc(func(DeploymentRef) (Deployment, error) {
		calls.Add(1)
		return Deployment{}, errors.New("same Deployment unexpectedly resolved")
	})
	engine, err := NewEngine(EngineConfig{DeploymentResolver: resolver})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := EncodeInput(childTestInput{Mode: "parent"})
	parent, err := engine.Start(context.Background(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	output := childTestResult(t, mustAwait(t, parent))
	if len(output.ChildIDs) != 1 || output.Failures != 0 {
		t.Fatalf("same-Deployment output = %#v", output)
	}
	childID, _ := ParseProcessID(output.ChildIDs[0])
	child, found := engine.Process(childID)
	if !found {
		t.Fatal("same-Deployment child is missing")
	}
	_ = mustAwait(t, child)
	if got := calls.Load(); got != 0 {
		t.Fatalf("same-Deployment resolver calls = %d, want 0", got)
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
}

type deploymentMapResolver map[DeploymentRef]Deployment

func (d deploymentMapResolver) Resolve(reference DeploymentRef) (Deployment, error) {
	deployment, found := d[reference]
	if !found {
		return Deployment{}, errors.New("deployment not found")
	}
	return deployment, nil
}

type deploymentResolverFunc func(DeploymentRef) (Deployment, error)

func (d deploymentResolverFunc) Resolve(reference DeploymentRef) (Deployment, error) {
	return d(reference)
}

type crossParentDefinition struct {
	descriptor Descriptor
	target     DeploymentRef
}

func newCrossParentDeployment(t *testing.T, target DeploymentRef) Deployment {
	t.Helper()
	inputSchema, _ := SchemaFor[struct{}]()
	outputSchema, _ := SchemaFor[childTestOutput]()
	descriptor, err := NewDescriptor(DescriptorConfig{
		Name: "test.cross_parent", Description: "Start a child owned by another Strategy.",
		InputSchema: inputSchema, OutputSchema: outputSchema,
	})
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := NewDeployment(DeploymentConfig{
		Definition:           &crossParentDefinition{descriptor: descriptor, target: target},
		Dispatcher:           childTestDispatcher{},
		ImplementationDigest: ComputeDigest([]byte("cross-parent-implementation")),
		ConfigurationDigest:  ComputeDigest([]byte("cross-parent:" + target.Digest().String())),
	})
	if err != nil {
		t.Fatal(err)
	}
	return deployment
}

func (c *crossParentDefinition) Descriptor() Descriptor { return c.descriptor }

func (c *crossParentDefinition) Start(Input) (Execution, error) {
	return &crossParentExecution{target: c.target}, nil
}

func (c *crossParentDefinition) Restore(state ExecutionState) (Execution, error) {
	if state.Kind() != "test.cross_parent" {
		return nil, ErrInvalidExecutionState
	}
	var phase uint8
	if err := json.Unmarshal(state.Payload(), &phase); err != nil {
		return nil, err
	}
	return &crossParentExecution{target: c.target, phase: phase}, nil
}

type crossParentExecution struct {
	target DeploymentRef
	phase  uint8
}

func (c *crossParentExecution) Step(_ context.Context, signals []Signal) (Transition, error) {
	if c.phase == 0 {
		input, _ := EncodeInput(childTestInput{Mode: "leaf"})
		key, _ := ParseChildKey("other-strategy")
		effect, err := StartChild(childTestSpec(key, c.target, input))
		if err != nil {
			return Transition{}, err
		}
		c.phase = 1
		return Continue(0, effect)
	}
	if c.phase != 1 || len(signals) != 1 {
		return Transition{}, errors.New("cross-parent expected one child-start result")
	}
	start, err := ParseChildStartResult(signals[0])
	if err != nil {
		return Transition{}, err
	}
	output := childTestOutput{}
	if childID, started := start.ProcessID(); started {
		output.ChildIDs = []string{childID.String()}
	} else if failure, failed := start.Failure(); failed {
		output.Failures = 1
		output.FailureCodes = []string{failure.Code()}
	}
	c.phase = 2
	erased, _ := EncodeOutput(output)
	return Complete(1, erased)
}

func (c *crossParentExecution) Snapshot() (ExecutionState, error) {
	payload, _ := json.Marshal(c.phase)
	return NewExecutionState("test.cross_parent", payload)
}
