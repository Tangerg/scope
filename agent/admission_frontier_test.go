package agent

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/jsonschema"
)

type admissionFrontierDefinition struct {
	descriptor Descriptor
	reference  DeploymentRef
	entered    chan struct{}
	release    chan struct{}
	first      atomic.Bool
}

func (a *admissionFrontierDefinition) Descriptor() Descriptor       { return a.descriptor }
func (*admissionFrontierDefinition) ChildDeployments() []Deployment { return nil }
func (a *admissionFrontierDefinition) Start(input Payload) (Execution, error) {
	root, err := input.Decode[bool]()
	if err != nil {
		return nil, err
	}
	phase := 2
	if root {
		phase = 0
	}
	return &admissionFrontierExecution{definition: a, phase: phase}, nil
}
func (a *admissionFrontierDefinition) Restore(ctx context.Context, state ExecutionState) (Execution, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	phase, err := state.Decode[int]("test.admission.frontier")
	if err != nil {
		return nil, err
	}
	return &admissionFrontierExecution{definition: a, phase: phase}, nil
}

type admissionFrontierExecution struct {
	definition *admissionFrontierDefinition
	phase      int
}

func (a *admissionFrontierExecution) Snapshot() (ExecutionState, error) {
	return EncodeExecutionState("test.admission.frontier", a.phase)
}
func (a *admissionFrontierExecution) Step(ctx context.Context, signals []Signal) (Transition, error) {
	if a.phase != 0 {
		output, err := EncodePayload(true)
		if err != nil {
			return Transition{}, err
		}
		return Complete(uint32(len(signals)), output)
	}
	if a.definition.first.CompareAndSwap(false, true) {
		close(a.definition.entered)
		select {
		case <-a.definition.release:
		case <-ctx.Done():
			return Transition{}, ctx.Err()
		}
	}
	input, err := EncodePayload(false)
	if err != nil {
		return Transition{}, err
	}
	key, err := ParseChildKey("child")
	if err != nil {
		return Transition{}, err
	}
	effect, err := NewChildStartEffect(ChildSpec{Key: key, DeploymentRef: a.definition.reference, Input: input, Budget: Budget{Steps: NewQuota(uint64(len(signals) + 1))}})
	if err != nil {
		return Transition{}, err
	}
	a.phase = 1
	return Continue(uint32(len(signals)), effect)
}

func TestChildAdmissionReplaysTheDurableInputFrontier(t *testing.T) {
	schema, err := jsonschema.For[bool]()
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := NewDescriptor(DescriptorConfig{Name: "test.admission.frontier", Description: "Freeze the input frontier before Host admission.", InputSchema: schema, OutputSchema: schema, SignalSchema: schema})
	if err != nil {
		t.Fatal(err)
	}
	definition := &admissionFrontierDefinition{descriptor: descriptor, entered: make(chan struct{}), release: make(chan struct{})}
	deployment, err := NewDeployment(DeploymentConfig{Definition: definition, ImplementationDigest: ComputeDigest([]byte("frontier")), ConfigurationDigest: ComputeDigest([]byte("frontier"))})
	if err != nil {
		t.Fatal(err)
	}
	definition.reference = deployment.DeploymentRef()
	admissions := make(chan ProcessAdmission, 2)
	admitter := ProcessAdmitterFunc(func(ctx context.Context, admission ProcessAdmission) error {
		if _, child := admission.Relation().ParentID(); !child {
			return nil
		}
		select {
		case admissions <- admission:
		case <-ctx.Done():
			return ctx.Err()
		}
		<-ctx.Done()
		return ctx.Err()
	})
	store := NewMemoryTreeCommitter()
	config := EngineConfig{TreeCommitter: store, ProcessAdmitter: admitter}
	original, err := NewEngine(config)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	input, err := EncodePayload(true)
	if err != nil {
		t.Fatal(err)
	}
	root, err := original.Start(ctx, deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { retireAdmissionEngine(t, original, root) })
	select {
	case <-definition.entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	id, err := ParseSignalID("signal:later-input")
	if err != nil {
		t.Fatal(err)
	}
	signal, err := NewSignalRequest(id, WaitID{}, input.JSON())
	if err != nil {
		t.Fatal(err)
	}
	if accepted, deliveryErr := root.DeliverSignals(ctx, signal); deliveryErr != nil || !accepted {
		t.Fatalf("signal=%t error=%v", accepted, deliveryErr)
	}
	close(definition.release)
	var first ProcessAdmission
	select {
	case first = <-admissions:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	cut, found, err := store.LoadTree(ctx, root.Relation().ProcessID())
	if err != nil || !found {
		t.Fatalf("load=%t error=%v", found, err)
	}
	restored, err := NewEngine(config)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := restored.RestoreTree(ctx, deployment, cut)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { retireAdmissionEngine(t, restored, recovered) })
	var second ProcessAdmission
	select {
	case second = <-admissions:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if first.Relation().ProcessID() != second.Relation().ProcessID() || first.Budget() != second.Budget() || !slices.Equal(first.Capabilities().Values(), second.Capabilities().Values()) {
		t.Fatalf("child admission changed: first ID=%s budget=%+v, second ID=%s budget=%+v", first.Relation().ProcessID(), first.Budget(), second.Relation().ProcessID(), second.Budget())
	}
}

func retireAdmissionEngine(t *testing.T, engine *Engine, process *Process) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
	defer cancel()
	if err := process.Kill(ctx, "test complete"); err != nil && !errors.Is(err, ErrProcessFinished) && !errors.Is(err, ErrTreeIncarnationConflict) {
		t.Error(err)
	}
	if err := process.Join(ctx); err != nil && !errors.Is(err, ErrTreeIncarnationConflict) {
		t.Error(err)
	}
	if err := engine.Close(ctx); err != nil {
		t.Error(err)
	}
}
