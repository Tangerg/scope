package agent

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/jsonschema"
)

func TestStepRefusesUnfundedEffectsBeforeExternalPermission(t *testing.T) {
	effect := controlValue(NewDispatcherEffect([]byte(`{"operation":"bounded"}`)))
	for _, test := range []struct {
		name       string
		budget     Budget
		maxPending uint64
		code       string
	}{
		{"effect authority", Budget{Effects: NewQuota(0)}, 4, failureCodeEngineLimitEffects},
		{"settlement authority", Budget{Signals: NewQuota(1)}, 4, failureCodeEngineLimitSignals},
		{"mailbox capacity", Budget{}, 1, failureCodeEngineLimitSignals},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := NewMemoryTreeCommitter()
			engine := controlValue(NewEngine(EngineConfig{TreeCommitter: store, Budget: test.budget, TreeLimits: TreeLimits{MaxPendingSignals: test.maxPending}}))
			defer mustCloseEngine(t, engine)
			dispatcher := &engineTestDispatcher{policy: ReplayPolicyNever}
			definition := &capacityDefinition{
				engineTestDefinition: newEngineTestDefinition(t, "engine.effect", "effect"),
				effects:              []Effect{effect, effect},
			}
			result := controlValue(engine.Run(t.Context(), engineTestDeployment(t, definition, dispatcher), controlValue(EncodePayload(engineTestInput{Value: "bounded"}))))
			failure, failed := result.Termination().Failure()
			if !failed || failure.Kind() != FailureKindExecution || failure.Code() != test.code || dispatcher.calls.Load() != 0 || result.Usage().PreparedEffects != 0 || result.Usage().CommittedSteps != 0 {
				t.Fatalf("unfunded Step escaped admission: failure=%+v usage=%+v calls=%d", failure, result.Usage(), dispatcher.calls.Load())
			}
			head, found, err := store.LoadTree(t.Context(), result.ProcessID())
			if err != nil || !found || head.ProcessSnapshots()[0].state.Prepared != nil {
				t.Fatalf("unfunded candidate reached the durable head: %v", err)
			}
		})
	}
}

type failingStepSnapshotDefinition struct {
	Definition
	cause error
}

func (f *failingStepSnapshotDefinition) Restore(ctx context.Context, state ExecutionState) (Execution, error) {
	execution, err := f.Definition.Restore(ctx, state)
	if err != nil {
		return nil, err
	}
	return &failingStepSnapshotExecution{Execution: execution, cause: f.cause}, nil
}

type failingStepSnapshotExecution struct {
	Execution
	cause     error
	attempted bool
}

func (f *failingStepSnapshotExecution) Step(ctx context.Context, signals []Signal) (Transition, error) {
	f.attempted = true
	return f.Execution.Step(ctx, signals)
}

func (f *failingStepSnapshotExecution) Snapshot() (ExecutionState, error) {
	if f.attempted {
		return ExecutionState{}, f.cause
	}
	return f.Execution.Snapshot()
}

func TestUncapturableStepCannotAdmitEffectsOrReplaceCommittedState(t *testing.T) {
	cause := errors.New("candidate snapshot unavailable")
	base := newEngineTestDefinition(t, "engine.effect", "effect")
	definition := &failingStepSnapshotDefinition{Definition: base, cause: cause}
	dispatcher := &engineTestDispatcher{policy: ReplayPolicyNever}
	store := NewMemoryTreeCommitter()
	engine := controlValue(NewEngine(EngineConfig{TreeCommitter: store}))
	defer mustCloseEngine(t, engine)
	input := controlValue(EncodePayload(engineTestInput{Value: "uncommitted candidate"}))
	initial := controlValue(controlValue(base.Start(input)).Snapshot())
	result := controlValue(engine.Run(t.Context(), engineTestDeployment(t, definition, dispatcher), input))
	failure, failed := result.Termination().Failure()
	if !failed || failure.Kind() != FailureKindExecution || failure.Code() != failureCodeExecutionSnapshotFailed || dispatcher.calls.Load() != 0 || result.Usage().CommittedSteps != 0 || result.Usage().PreparedEffects != 0 {
		t.Fatalf("uncapturable Step escaped admission: failure=%+v usage=%+v calls=%d", failure, result.Usage(), dispatcher.calls.Load())
	}
	head, found, err := store.LoadTree(t.Context(), result.ProcessID())
	if err != nil || !found {
		t.Fatalf("terminal head missing: found=%t err=%v", found, err)
	}
	process := head.ProcessSnapshots()[0]
	current := process.CommittedExecutionState()
	if current.Kind() != initial.Kind() || !bytes.Equal(current.Payload(), initial.Payload()) || process.state.Prepared != nil {
		t.Fatal("uncapturable candidate replaced durable execution state")
	}
}

func TestStepCannotPublishOutputOutsideItsFrozenDescriptor(t *testing.T) {
	base := newEngineTestDefinition(t, "engine.effect", "effect")
	descriptor := controlValue(NewDescriptor(DescriptorConfig{
		Name: base.Descriptor().Name(), Description: "Require an integer output.",
		InputSchema: base.Descriptor().InputSchema(), OutputSchema: controlValue(jsonschema.For[int]()),
	}))
	definition := &descriptorBoundaryDefinition{Definition: base, descriptor: func() Descriptor { return descriptor }}
	engine := controlValue(NewEngine(EngineConfig{TreeCommitter: NewMemoryTreeCommitter()}))
	defer mustCloseEngine(t, engine)
	result := controlValue(engine.Run(t.Context(), engineTestDeployment(t, definition, &engineTestDispatcher{policy: ReplayPolicyNever}), controlValue(EncodePayload(engineTestInput{Value: "wrong shape"}))))
	failure, failed := result.Termination().Failure()
	if !failed || failure.Kind() != FailureKindContract || failure.Code() != failureCodeExecutionOutputInvalid || result.Usage().CommittedSteps != 1 {
		t.Fatalf("invalid output escaped admission: failure=%+v usage=%+v", failure, result.Usage())
	}
	if output, present := result.Termination().Output(); present || output.Valid() {
		t.Fatal("failure carried rejected output")
	}
}
