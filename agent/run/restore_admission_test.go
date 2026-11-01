package run

import (
	"context"
	"errors"
	"testing"
)

type restoreAdmissionDefinition struct {
	Definition
	calls int
	err   error
}

func (r *restoreAdmissionDefinition) Restore(ctx context.Context, state ExecutionState) (Execution, error) {
	r.calls++
	if r.err != nil {
		return nil, r.err
	}
	return r.Definition.Restore(ctx, state)
}

func TestRestoreReservesBeforeCallingDefinition(t *testing.T) {
	tree := completedTreeSnapshot(t)
	for _, closed := range []bool{false, true} {
		engine := controlValue(New(t.Context(), Config{Store: newSnapshotTestCommitter(tree)}))
		definition := &restoreAdmissionDefinition{Definition: newEngineTestDefinition(t, "engine.effect", "effect")}
		deployment := engineTestDeployment(t, definition, &engineTestDispatcher{policy: ReplayPolicyNever})
		want := ErrClosed
		if closed {
			mustCloseEngine(t, engine)
		} else {
			controlValue(engine.Recover(t.Context(), deployment, tree))
			want = ErrProcessAlreadyExists
		}
		before := definition.calls
		if _, err := engine.Recover(t.Context(), deployment, tree); !errors.Is(err, want) {
			t.Fatalf("Restore: %v; want %v", err, want)
		}
		if definition.calls != before {
			t.Fatal("rejected restore called Definition.Restore")
		}
		mustCloseEngine(t, engine)
	}
}

func TestFailedRestoreReleasesEveryReservedIdentity(t *testing.T) {
	runtime := newWaitingSnapshotTree(t, 3)
	tree := controlValue(runtime.captureTree())
	cause := errors.New("restore rejected")
	definition := &restoreAdmissionDefinition{Definition: newChildTestDeployment(t).Definition(), err: cause}
	base := newChildTestDeployment(t)
	deployment := controlValue(Bind[Payload, Payload](BindingConfig{
		Definition: definition, Dispatcher: base.dispatcher,
		ImplementationDigest: base.ProgramRef().ImplementationDigest(),
		ConfigurationDigest:  base.ProgramRef().ConfigurationDigest(),
	}))
	engine := runtime.engine
	if _, err := engine.Recover(t.Context(), deployment, tree); !errors.Is(err, cause) {
		t.Fatalf("Restore: %v", err)
	}
	restoration := &treeRestoration{wire: controlValue(tree.wire())}
	if err := engine.reserveRestoredTree(restoration); err != nil {
		t.Fatalf("failed restore retained identities: %v", err)
	}
	engine.discardRestoredTree(restoration)
}
