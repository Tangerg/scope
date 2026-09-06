package agent

import (
	"context"
	"sync"
	"testing"
	"time"
)

type fixtureCompletionDefinition struct {
	base    *childTestDefinition
	ready   chan struct{}
	release chan struct{}
	once    sync.Once
}

func (f *fixtureCompletionDefinition) Descriptor() Descriptor { return f.base.Descriptor() }
func (f *fixtureCompletionDefinition) Start(input Input) (Execution, error) {
	e, err := f.base.Start(input)
	if err != nil {
		return nil, err
	}
	return &fixtureCompletionExecution{definition: f, base: e.(*childTestExecution)}, nil
}
func (f *fixtureCompletionDefinition) Restore(state ExecutionState) (Execution, error) {
	e, err := f.base.Restore(state)
	if err != nil {
		return nil, err
	}
	return &fixtureCompletionExecution{definition: f, base: e.(*childTestExecution)}, nil
}

type fixtureCompletionExecution struct {
	definition *fixtureCompletionDefinition
	base       *childTestExecution
}

func (f *fixtureCompletionExecution) Snapshot() (ExecutionState, error) { return f.base.Snapshot() }
func (f *fixtureCompletionExecution) Step(ctx context.Context, signals []Signal) (Transition, error) {
	if f.base.state.Mode == "nested_wait" && f.base.state.Phase == "waiting" && len(signals) > 0 {
		f.definition.once.Do(func() { close(f.definition.ready) })
		select {
		case <-f.definition.release:
		case <-ctx.Done():
			return Transition{}, ctx.Err()
		}
	}
	return f.base.Step(ctx, signals)
}

func TestRestoreDoesNotChargeExistingChildCompletion(t *testing.T) {
	base := newChildTestDeployment(t)
	definition := &fixtureCompletionDefinition{
		base:  base.Definition().(*childTestDefinition),
		ready: make(chan struct{}), release: make(chan struct{}),
	}
	deployment, err := NewDeployment(DeploymentConfig{
		Definition: definition, Dispatcher: childTestDispatcher{},
		ImplementationDigest: ComputeDigest([]byte("completion-implementation")),
		ConfigurationDigest:  ComputeDigest([]byte("completion-configuration")),
	})
	if err != nil {
		t.Fatal(err)
	}
	definition.base.reference = deployment.DeploymentRef()
	limits := DefaultLimits()
	limits.MaxPendingSignals = 1
	engine, err := NewEngine(EngineConfig{Limits: limits})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	input, _ := EncodeInput(childTestInput{Mode: "nested_wait"})
	root, err := engine.Start(ctx, deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	waitForProcessStatus(t, root, StatusWaiting)
	ids := directChildIDs(t, engine, root.ID())
	if len(ids) != 1 {
		t.Fatalf("children=%v", ids)
	}
	childID, _ := ParseProcessID(ids[0])
	child, _ := engine.Process(childID)
	waitForProcessStatus(t, child, StatusWaiting)
	snapshot, err := child.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	waitID, _ := snapshot.WaitID()
	signalID, _ := ParseSignalID("sig:completion-complete-child")
	request, err := NewSignalRequest(signalID, waitID, []byte(`{"reply":"done"}`))
	if err != nil {
		t.Fatal(err)
	}
	if accepted, deliveryErr := child.DeliverSignals(ctx, request); deliveryErr != nil || !accepted {
		t.Fatalf("child delivery = %t, %v", accepted, deliveryErr)
	}
	select {
	case <-definition.ready:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	tree, err := engine.CaptureTree(ctx, root.ID())
	if err != nil {
		t.Fatal(err)
	}
	close(definition.release)
	original, err := root.Await(ctx)
	if err != nil || original.Status() != StatusCompleted {
		t.Fatalf("original=%s err=%v", original.Status(), err)
	}
	if closeErr := engine.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	restoredEngine, err := NewEngine(EngineConfig{Limits: limits})
	if err != nil {
		t.Fatal(err)
	}
	restored, err := restoredEngine.RestoreTree(ctx, deployment, tree)
	if err != nil {
		t.Fatal(err)
	}
	result, err := restored.Await(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if closeErr := restoredEngine.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if result.Status() != StatusCompleted {
		t.Fatalf("same snapshot resumed as %s, termination=%+v; original completed", result.Status(), result.Termination())
	}
}
