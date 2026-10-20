package agent

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"testing"
)

type brokenUnwrapError struct{}

func (brokenUnwrapError) Error() string { return "broken unwrap" }
func (brokenUnwrapError) Unwrap() error { panic("unwrap failed") }

type brokenAsError struct{}

func (brokenAsError) Error() string { return "broken as" }
func (brokenAsError) As(any) bool   { panic("as failed") }

type brokenIsError struct{}

func (brokenIsError) Error() string { return "broken is" }
func (brokenIsError) Is(error) bool { panic("is failed") }

type brokenMessageError struct{}

func (brokenMessageError) Error() string { panic("error failed") }

type returningErrorCallbacks struct{ err error }

func (r returningErrorCallbacks) Descriptor() Descriptor           { return Descriptor{} }
func (returningErrorCallbacks) ChildDeployments() []Deployment     { return nil }
func (r returningErrorCallbacks) Start(Payload) (Execution, error) { return nil, r.err }
func (r returningErrorCallbacks) Restore(context.Context, ExecutionState) (Execution, error) {
	return nil, r.err
}
func (r returningErrorCallbacks) Step(context.Context, []Signal) (Transition, error) {
	return Transition{}, r.err
}
func (r returningErrorCallbacks) Snapshot() (ExecutionState, error) { return ExecutionState{}, r.err }
func (r returningErrorCallbacks) Policy(Effect) EffectPolicy {
	return EffectPolicy{Replay: ReplayPolicyNever}
}
func (r returningErrorCallbacks) Dispatch(context.Context, EffectRequest, DeltaEmitter) (Settlement, error) {
	return Settlement{}, r.err
}

func TestCallbackReturnedErrorsAreContained(t *testing.T) {
	var nilPath *fs.PathError
	for name, cause := range map[string]error{
		"unwrap": brokenUnwrapError{}, "as": brokenAsError{}, "is": brokenIsError{}, "message": brokenMessageError{},
		"typed_nil": nilPath, "wrapped_typed_nil": fmt.Errorf("wrapped: %w", nilPath),
	} {
		t.Run(name, func(t *testing.T) {
			callbacks := returningErrorCallbacks{err: cause}
			for _, call := range []func() error{
				func() error { _, err := startExecution(callbacks, Payload{}); return err },
				func() error { _, err := restoreExecution(t.Context(), callbacks, ExecutionState{}); return err },
				func() error { _, err := stepExecution(t.Context(), callbacks, nil); return err },
				func() error { _, err := captureExecution(callbacks); return err },
				func() error { _, err := dispatchEffect(t.Context(), callbacks, EffectRequest{}, nil); return err },
			} {
				err := call()
				if err == nil || failureKindForError(err, FailureKindExecution) != FailureKindPanic {
					t.Fatalf("uncontained error: %v", err)
				}
				if failure := newEngineFailure(FailureKindPanic, failureCodeExecutionStepFailed, err); !failure.Valid() {
					t.Fatal("invalid failure")
				}
				if failure := dispatchFailure(err); failure.Kind() != FailureKindPanic {
					t.Fatalf("dispatch: %+v", failure)
				}
			}
		})
	}
}

func TestBrokenStepErrorDoesNotCrashHost(t *testing.T) {
	if os.Getenv("SCOPE_BROKEN_STEP_CHILD") == "1" {
		cause := brokenUnwrapError{}
		definition := &rejectedStepDefinition{descriptor: newEngineTestDefinition(t, "test.broken_step", "complete").Descriptor(), err: cause}
		engine := controlValue(NewEngine(EngineConfig{TreeCommitter: NewMemoryTreeCommitter()}))
		defer mustCloseEngine(t, engine)
		process := controlValue(engine.Start(t.Context(), engineTestDeployment(t, definition, &engineTestDispatcher{policy: ReplayPolicyNever}), controlValue(EncodePayload(engineTestInput{Value: "input"}))))
		waitForStatus(t, process, StatusPaused)
		if err := process.Resume(t.Context()); err != nil {
			t.Fatal(err)
		}
		result := mustAwait(t, process)
		failure, ok := result.Termination().Failure()
		if !ok || failure.Kind() != FailureKindPanic || failure.Code() != failureCodeExecutionStepFailed {
			t.Fatalf("failure: %+v", failure)
		}
		return
	}
	command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestBrokenStepErrorDoesNotCrashHost$")
	command.Env = append(os.Environ(), "SCOPE_BROKEN_STEP_CHILD=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("host crashed: %v\n%s", err, output)
	}
}

func TestSealedCallbackErrorRetainsHostCause(t *testing.T) {
	cause := errors.New("host failure")
	_, err := stepExecution(t.Context(), returningErrorCallbacks{err: cause}, nil)
	if !errors.Is(err, cause) || failureKindForError(err, FailureKindExecution) != FailureKindExecution {
		t.Fatalf("lost cause: %v", err)
	}
}

type guardedCallbackError struct{ sealed bool }

func (g *guardedCallbackError) Error() string {
	if g.sealed {
		panic("message after boundary")
	}
	return "guarded error"
}
func (g *guardedCallbackError) Unwrap() error {
	if g.sealed {
		panic("unwrap after boundary")
	}
	return nil
}
func (g *guardedCallbackError) As(any) bool {
	if g.sealed {
		panic("as after boundary")
	}
	return false
}
func (g *guardedCallbackError) Is(error) bool {
	if g.sealed {
		panic("is after boundary")
	}
	return false
}

func TestRuntimeUsesSealedErrorFacts(t *testing.T) {
	cause := &guardedCallbackError{}
	_, err := stepExecution(t.Context(), returningErrorCallbacks{err: cause}, nil)
	cause.sealed = true
	if got := failureKindForError(err, FailureKindExecution); got != FailureKindExecution {
		t.Fatalf("kind=%s", got)
	}
	if got := dispatchFailure(errors.Join(err, nil)); got.Code() != failureCodeEngineDispatchFailed {
		t.Fatalf("dispatch=%+v", got)
	}
	if got := newTreeRuntimeFailure(fmt.Errorf("wrapped: %w", err)); got.Code() != failureCodeEngineTreeCommitterFailed {
		t.Fatalf("runtime=%+v", got)
	}
	if got := newEngineFailure(FailureKindExecution, failureCodeExecutionStepFailed, err); got.Message() != "guarded error" {
		t.Fatalf("failure=%+v", got)
	}
}

type panickingPolicyDispatcher struct{ cause error }

func (p panickingPolicyDispatcher) Policy(Effect) EffectPolicy { panic(p.cause) }
func (p panickingPolicyDispatcher) Dispatch(context.Context, EffectRequest, DeltaEmitter) (Settlement, error) {
	return Settlement{}, errors.New("dispatch must not run after a policy panic")
}

func TestPolicyPanicDoesNotCrashRuntime(t *testing.T) {
	if os.Getenv("SCOPE_POLICY_PANIC_CHILD") == "1" {
		var nilPath *fs.PathError
		for _, cause := range []error{errors.New("policy defect"), nilPath} {
			engine := controlValue(NewEngine(EngineConfig{TreeCommitter: NewMemoryTreeCommitter()}))
			definition := newEngineTestDefinition(t, "engine.effect", "effect")
			process := controlValue(engine.Start(t.Context(), engineTestDeployment(t, definition, panickingPolicyDispatcher{cause}), controlValue(EncodePayload(engineTestInput{Value: "input"}))))
			failure, ok := mustAwait(t, process).Termination().Failure()
			if !ok || failure.Kind() != FailureKindPanic || failure.Code() != failureCodeExecutionEffectInvalid {
				t.Fatalf("policy failure = %+v", failure)
			}
			mustCloseEngine(t, engine)
		}
		return
	}
	command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestPolicyPanicDoesNotCrashRuntime$")
	command.Env = append(os.Environ(), "SCOPE_POLICY_PANIC_CHILD=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("policy crashed runtime: %v\n%s", err, output)
	}
}

type recoveryPolicyDispatcher struct {
	calls int
	cause error
}

func (r *recoveryPolicyDispatcher) Policy(Effect) EffectPolicy {
	r.calls++
	if r.calls > 1 {
		panic(r.cause)
	}
	return EffectPolicy{Replay: ReplayPolicySameIdentity}
}
func (*recoveryPolicyDispatcher) Dispatch(context.Context, EffectRequest, DeltaEmitter) (Settlement, error) {
	return Settlement{}, errors.New("must not dispatch")
}

func TestRecoveryPolicyPanicUsesSealedClassification(t *testing.T) {
	var nilPath *fs.PathError
	for _, cause := range []error{errors.New("policy defect"), nilPath} {
		pending, previous, _ := durablePendingTreeSnapshot(t)
		dispatcher := &recoveryPolicyDispatcher{cause: cause}
		deployment := engineTestDeployment(t, previous.Definition(), dispatcher)
		engine := controlValue(NewEngine(EngineConfig{TreeCommitter: newSnapshotTestCommitter(pending)}))
		process := controlValue(engine.RestoreTree(t.Context(), deployment, pending))
		failure, ok := mustAwait(t, process).Termination().Failure()
		if !ok || failure.Kind() != FailureKindPanic || failure.Code() != failureCodeEngineEffectRecoveryInvalid {
			t.Fatalf("recovery policy failure=%+v", failure)
		}
		mustCloseEngine(t, engine)
	}
}
