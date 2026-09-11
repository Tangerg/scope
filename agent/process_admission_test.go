package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

func TestProcessAdmitterReceivesRootAndChildResourceContracts(t *testing.T) {
	read, err := ParseCapability("resource.read")
	if err != nil {
		t.Fatal(err)
	}
	capabilities, err := NewCapabilitySet(read)
	if err != nil {
		t.Fatal(err)
	}
	childDeployment := newChildTestDeployment(t)
	parentDeployment := newCrossParentDeployment(t, childDeployment.DeploymentRef())
	var mu sync.Mutex
	var admissions []ProcessAdmission
	admitter := ProcessAdmitterFunc(func(_ context.Context, admission ProcessAdmission) error {
		mu.Lock()
		admissions = append(admissions, admission)
		mu.Unlock()
		return nil
	})
	engine, err := NewEngine(EngineConfig{
		DeploymentResolver: deploymentMapResolver{childDeployment.DeploymentRef(): childDeployment},
		ProcessAdmitter:    admitter,
		Capabilities:       capabilities,
	})
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
		t.Fatalf("parent output = %#v", output)
	}
	childID, _ := ParseProcessID(output.ChildIDs[0])
	child, found := engine.Process(childID)
	if !found {
		t.Fatal("admitted child is missing")
	}
	_ = mustAwait(t, child)

	mu.Lock()
	got := append([]ProcessAdmission(nil), admissions...)
	mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("admissions = %d, want root and child", len(got))
	}
	rootAdmission, childAdmission := got[0], got[1]
	if !rootAdmission.Valid() || !rootAdmission.Relation().IsRoot() ||
		rootAdmission.Relation().ProcessID() != parent.ID() ||
		rootAdmission.DeploymentRef() != parentDeployment.DeploymentRef() ||
		rootAdmission.Descriptor().Digest() != parentDeployment.Descriptor().Digest() ||
		rootAdmission.Budget() != budgetFromLimits(DefaultLimits()) ||
		!rootAdmission.Capabilities().Contains(read) {
		t.Fatalf("root admission = %#v", rootAdmission)
	}
	parentID, hasParent := childAdmission.Relation().ParentID()
	if !childAdmission.Valid() || !hasParent || parentID != parent.ID() ||
		childAdmission.Relation().Depth() != 1 ||
		childAdmission.DeploymentRef() != childDeployment.DeploymentRef() ||
		childAdmission.Budget() != (Budget{Steps: 20, Effects: 20, Signals: 40}) ||
		len(childAdmission.Capabilities().Values()) != 0 {
		t.Fatalf("child admission = %#v", childAdmission)
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
}

func TestProcessAdmitterRejectsBeforeDefinitionStarts(t *testing.T) {
	base := newChildTestDeployment(t)
	var starts atomic.Uint32
	definition := &countingDefinition{Definition: base.Definition(), starts: &starts}
	deployment, err := NewDeployment(DeploymentConfig{
		Definition: definition, Dispatcher: base.effectDispatcher(),
		ImplementationDigest: ComputeDigest([]byte("admission-root-implementation")),
		ConfigurationDigest:  ComputeDigest([]byte("admission-root-configuration")),
	})
	if err != nil {
		t.Fatal(err)
	}
	rejection := errors.New("deployment is disabled")
	engine, err := NewEngine(EngineConfig{ProcessAdmitter: ProcessAdmitterFunc(
		func(context.Context, ProcessAdmission) error { return rejection },
	)})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := EncodeInput(childTestInput{Mode: "leaf"})
	process, err := engine.Start(context.Background(), deployment, input)
	if process != nil || !errors.Is(err, ErrProcessAdmissionRejected) || !errors.Is(err, rejection) {
		t.Fatalf("Start process=%v error=%v", process, err)
	}
	if got := starts.Load(); got != 0 {
		t.Fatalf("Definition.Start calls = %d, want 0", got)
	}
	engine.mu.RLock()
	processCount := len(engine.processes)
	engine.mu.RUnlock()
	if processCount != 0 {
		t.Fatalf("published Processes = %d, want 0", processCount)
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
}

func TestProcessAdmitterRejectsChildWithoutPublishingIt(t *testing.T) {
	childDeployment := newChildTestDeployment(t)
	parentDeployment := newCrossParentDeployment(t, childDeployment.DeploymentRef())
	var calls atomic.Uint32
	admitter := ProcessAdmitterFunc(func(_ context.Context, admission ProcessAdmission) error {
		calls.Add(1)
		if !admission.Relation().IsRoot() {
			return errors.New("child deployment denied")
		}
		return nil
	})
	engine, err := NewEngine(EngineConfig{
		DeploymentResolver: deploymentMapResolver{childDeployment.DeploymentRef(): childDeployment},
		ProcessAdmitter:    admitter,
	})
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
		len(output.FailureCodes) != 1 || output.FailureCodes[0] != "engine.child.admission.rejected" {
		t.Fatalf("rejected child output = %#v", output)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("admitter calls = %d, want root and child", got)
	}
	engine.mu.RLock()
	processCount := len(engine.processes)
	engine.mu.RUnlock()
	if processCount != 1 {
		t.Fatalf("published Processes = %d, want only parent", processCount)
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
}

func TestProcessAdmitterCannotOverrideCapabilityAttenuation(t *testing.T) {
	deployment := newChildTestDeployment(t)
	var calls atomic.Uint32
	engine, err := NewEngine(EngineConfig{ProcessAdmitter: ProcessAdmitterFunc(
		func(context.Context, ProcessAdmission) error {
			calls.Add(1)
			return nil
		},
	)})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := EncodeInput(childTestInput{Mode: "capability_escalation"})
	root, err := engine.Start(context.Background(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	output := childTestResult(t, mustAwait(t, root))
	if output.Failures != 1 || len(output.FailureCodes) != 1 ||
		output.FailureCodes[0] != "engine.child.capability_escalation" {
		t.Fatalf("attenuation output = %#v", output)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("admitter calls = %d, want root only", got)
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
}

func TestProcessAdmitterPanicAndTypedNilAreRejected(t *testing.T) {
	var typedNil ProcessAdmitterFunc
	if _, err := NewEngine(EngineConfig{ProcessAdmitter: typedNil}); !errors.Is(err, ErrInvalidEngineConfig) {
		t.Fatalf("typed-nil error = %v, want %v", err, ErrInvalidEngineConfig)
	}
	deployment := newChildTestDeployment(t)
	engine, err := NewEngine(EngineConfig{ProcessAdmitter: ProcessAdmitterFunc(
		func(context.Context, ProcessAdmission) error { panic("admission panic") },
	)})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := EncodeInput(childTestInput{Mode: "leaf"})
	if process, err := engine.Start(context.Background(), deployment, input); process != nil ||
		!errors.Is(err, ErrProcessAdmissionRejected) {
		t.Fatalf("panicking admitter process=%v error=%v", process, err)
	}
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreDoesNotReadmitPreviouslyAdmittedProcess(t *testing.T) {
	deployment := newChildTestDeployment(t)
	write, err := ParseCapability("files.write")
	if err != nil {
		t.Fatal(err)
	}
	capabilities, err := NewCapabilitySet(write)
	if err != nil {
		t.Fatal(err)
	}
	first, err := NewEngine(EngineConfig{Capabilities: capabilities})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := EncodeInput(childTestInput{Mode: "leaf"})
	process, err := first.Start(context.Background(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	_ = mustAwait(t, process)
	snapshot, err := first.CaptureTree(context.Background(), process.ID())
	if err != nil {
		t.Fatal(err)
	}
	if closeErr := first.Close(context.WithoutCancel(t.Context())); closeErr != nil {
		t.Fatal(closeErr)
	}
	var admissionCalls atomic.Uint32
	var outcomeCalls atomic.Uint32
	restoredEngine, err := NewEngine(EngineConfig{
		Limits:     Limits{MaxSteps: 1},
		TreeLimits: TreeLimits{MaxDepth: 1},
		ProcessAdmitter: ProcessAdmitterFunc(func(context.Context, ProcessAdmission) error {
			admissionCalls.Add(1)
			return errors.New("live policy changed")
		}),
		ProcessInitializationOutcomeAcknowledger: ProcessInitializationOutcomeAcknowledgerFunc(func(
			context.Context,
			ProcessInitializationOutcome,
		) error {
			outcomeCalls.Add(1)
			return errors.New("outcome must not be requested")
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	restored, err := restoredEngine.RestoreTree(context.Background(), deployment, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if got := admissionCalls.Load(); got != 0 {
		t.Fatalf("restore admission calls = %d, want 0", got)
	}
	if got := outcomeCalls.Load(); got != 0 {
		t.Fatalf("restore outcome calls = %d, want 0", got)
	}
	if result := mustAwait(t, restored); result.Status() != StatusCompleted {
		t.Fatalf("restored status = %s", result.Status())
	}
	if !restored.Capabilities().Contains(write) || restored.Budget() != process.Budget() {
		t.Fatal("restoration rewrote captured authority or budget using current start defaults")
	}
	before, err := snapshot.ProcessSnapshots()[0].wire()
	if err != nil {
		t.Fatal(err)
	}
	after, err := inspectProcessSnapshot(t, restored).wire()
	if err != nil || before.Limits != after.Limits || before.TreeLimits != after.TreeLimits || before.Usage != after.Usage {
		t.Fatalf("restoration changed captured resource facts: %v", err)
	}
	if err := restoredEngine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
}

func TestProcessAdmitterReceivesStartContext(t *testing.T) {
	type contextKey struct{}
	want := "admission-context"
	engine, err := NewEngine(EngineConfig{ProcessAdmitter: ProcessAdmitterFunc(
		func(ctx context.Context, _ ProcessAdmission) error {
			if got, _ := ctx.Value(contextKey{}).(string); got != want {
				return fmt.Errorf("admission context value = %q, want %q", got, want)
			}
			return nil
		},
	)})
	if err != nil {
		t.Fatal(err)
	}
	deployment := newChildTestDeployment(t)
	input, _ := EncodeInput(childTestInput{Mode: "leaf"})
	ctx := context.WithValue(context.Background(), contextKey{}, want)
	process, err := engine.Start(ctx, deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	_ = mustAwait(t, process)
	if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
}

type countingDefinition struct {
	Definition
	starts *atomic.Uint32
}

func (c *countingDefinition) Start(input Input) (Execution, error) {
	c.starts.Add(1)
	return c.Definition.Start(input)
}
