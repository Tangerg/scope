package agent

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

type panickingCallbacks struct{ cause error }

func (p panickingCallbacks) Descriptor() Descriptor           { panic(p.cause) }
func (p panickingCallbacks) Start(Payload) (Execution, error) { panic(p.cause) }
func (p panickingCallbacks) Restore(context.Context, ExecutionState) (Execution, error) {
	panic(p.cause)
}
func (p panickingCallbacks) Step(context.Context, []Signal) (Transition, error) { panic(p.cause) }
func (p panickingCallbacks) Snapshot() (ExecutionState, error)                  { panic(p.cause) }
func (p panickingCallbacks) Resolve(DeploymentRef) (Deployment, error)          { panic(p.cause) }
func (p panickingCallbacks) ReplayPolicy(Effect) ReplayPolicy                   { panic(p.cause) }
func (p panickingCallbacks) Dispatch(context.Context, EffectRequest, DeltaEmitter) (Settlement, error) {
	panic(p.cause)
}
func (p panickingCallbacks) Admit(context.Context, ProcessAdmission) error { panic(p.cause) }
func (p panickingCallbacks) AcknowledgeProcessInitializationOutcome(context.Context, ProcessInitializationOutcome) error {
	panic(p.cause)
}
func (p panickingCallbacks) ActivateTree(context.Context, TreeActivation) error     { panic(p.cause) }
func (p panickingCallbacks) CommitEffect(context.Context, EffectBoundary) error     { panic(p.cause) }
func (p panickingCallbacks) CommitCheckpoint(context.Context, TreeCheckpoint) error { panic(p.cause) }

func TestCallbackPanicsPreserveTypedIdentityAndCause(t *testing.T) {
	cause := errors.New("host programming error")
	callbacks := panickingCallbacks{cause: cause}
	deployment := newChildTestDeployment(t)
	relation := rootProcessRelation(newProcessID())
	admission := newProcessAdmission(relation, deployment, Budget{}, CapabilitySet{})
	snapshot := preparedEngineTestSnapshot(t)
	incarnation := newTreeIncarnationID()
	tree := controlValue(newTreeSnapshot(treeSnapshotWire{RootID: snapshot.ProcessID(), IncarnationID: incarnation, ProcessSnapshots: []ProcessSnapshot{snapshot}}))
	activation := controlValue(newTreeActivation(newTreeIncarnationID(), ComputeDigest([]byte("old head")), incarnation, tree))
	for _, test := range []struct {
		operation string
		call      func() error
	}{
		{"Definition.Descriptor", func() error { _, err := definitionDescriptor(callbacks); return err }},
		{"Definition.Start", func() error { _, err := startExecution(callbacks, Payload{}); return err }},
		{"Definition.Restore", func() error { _, err := restoreExecution(t.Context(), callbacks, ExecutionState{}); return err }},
		{"Execution.Step", func() error { _, err := stepExecution(t.Context(), callbacks, nil); return err }},
		{"Execution.Snapshot", func() error { _, err := captureExecution(callbacks); return err }},
		{"DeploymentResolver.Resolve", func() error { _, err := resolveDeployment(callbacks, deployment.DeploymentRef()); return err }},
		{"Dispatcher.ReplayPolicy", func() error { _, err := dispatcherReplayPolicy(callbacks, Effect{}); return err }},
		{"Dispatcher.Dispatch", func() error { _, err := dispatchEffect(t.Context(), callbacks, EffectRequest{}, nil); return err }},
		{"ProcessAdmitter.Admit", func() error { return requestProcessAdmission(t.Context(), callbacks, admission) }},
		{"ProcessInitializationOutcomeAcknowledger.AcknowledgeProcessInitializationOutcome", func() error {
			return acknowledgeProcessInitializationOutcome(t.Context(), callbacks, initializedProcessOutcome(admission, time.Now()))
		}},
		{"TreeCommitter.ActivateTree", func() error { return activateTree(t.Context(), callbacks, activation) }},
		{"TreeCommitter.CommitEffect", func() error {
			return commitEffectBoundary(t.Context(), callbacks, EffectBoundary{kind: EffectBoundaryKindPending})
		}},
		{"TreeCommitter.CommitCheckpoint", func() error {
			return commitTreeCheckpoint(t.Context(), callbacks, TreeCheckpoint{kind: TreeCheckpointKindStart})
		}},
	} {
		t.Run(test.operation, func(t *testing.T) {
			err := test.call()
			panicErr, ok := errors.AsType[*CallbackPanicError](fmt.Errorf("host call: %w", err))
			if !ok || panicErr.Operation != test.operation || !errors.Is(err, cause) {
				t.Fatalf("panic identity lost: %v", err)
			}
			if got := failureKindForError(err, FailureKindExternal); got != FailureKindPanic {
				t.Fatalf("kind=%s", got)
			}
		})
	}
	for _, kind := range []FailureKind{FailureKindExecution, FailureKindExternal, FailureKindContract} {
		if got := failureKindForError(cause, kind); got != kind {
			t.Fatalf("ordinary failure=%s, want %s", got, kind)
		}
	}
	textPanic := &CallbackPanicError{Operation: "Execution.Step", Value: "boom"}
	if textPanic.Unwrap() != nil || textPanic.Error() != "agent: Execution.Step panicked: boom" {
		t.Fatalf("non-error panic=%v", textPanic)
	}
}

func TestChildAdmissionFailuresDistinguishPanicsFromOrdinaryErrors(t *testing.T) {
	deployment := newChildTestDeployment(t)
	parent := newCrossParentDeployment(t, deployment.DeploymentRef())
	cause := errors.New("callback failure")
	for _, boundary := range []string{"resolve", "admit", "acknowledge"} {
		for _, panics := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/panic=%t", boundary, panics), func(t *testing.T) {
				fail := func() error {
					if panics {
						panic(cause)
					}
					return cause
				}
				config := EngineConfig{TreeCommitter: NewMemoryTreeCommitter(), DeploymentResolver: deploymentMapResolver{deployment.DeploymentRef(): deployment}}
				code := ""
				switch boundary {
				case "resolve":
					code = "engine.child.deployment_unavailable"
					config.DeploymentResolver = deploymentResolverFunc(func(DeploymentRef) (Deployment, error) { return Deployment{}, fail() })
				case "admit":
					code = "engine.child.admission.rejected"
					config.ProcessAdmitter = ProcessAdmitterFunc(func(_ context.Context, a ProcessAdmission) error {
						if _, child := a.Relation().ParentID(); child {
							return fail()
						}
						return nil
					})
				case "acknowledge":
					code = "engine.child.initialization_outcome.unacknowledged"
					config.ProcessInitializationOutcomeAcknowledger = ProcessInitializationOutcomeAcknowledgerFunc(func(_ context.Context, o ProcessInitializationOutcome) error {
						if _, child := o.Admission().Relation().ParentID(); child {
							return fail()
						}
						return nil
					})
				}
				engine := controlValue(NewEngine(config))
				t.Cleanup(func() { mustCloseEngine(t, engine) })
				process := controlValue(engine.Start(t.Context(), parent, controlValue(EncodePayload(struct{}{}))))
				output := childTestResult(t, mustAwait(t, process))
				want := FailureKindExternal
				if panics {
					want = FailureKindPanic
				}
				if output.Failures != 1 || len(output.FailureCodes) != 1 || output.FailureCodes[0] != code || len(output.FailureKinds) != 1 || output.FailureKinds[0] != want {
					t.Fatalf("child outcome=%+v, want %s/%s", output, want, code)
				}
			})
		}
	}
}
