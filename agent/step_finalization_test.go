package agent

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"testing/synctest"
)

func TestImmediateChildCompletionLimitReportsExecutionFailure(t *testing.T) {
	for _, limit := range []struct {
		name   string
		limits Limits
	}{
		{name: "pending mailbox", limits: Limits{MaxPendingSignals: 1}},
		{name: "allocated child budget", limits: Limits{MaxSignals: 52, MaxPendingSignals: 52}},
	} {
		for _, durable := range []bool{false, true} {
			mode := "ephemeral"
			if durable {
				mode = "durable"
			}
			t.Run(limit.name+"/"+mode, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					base := newChildTestDeployment(t)
					definition := &heldChildWaitDefinition{
						childTestDefinition: base.Definition().(*childTestDefinition),
						entered:             make(chan struct{}), release: make(chan struct{}),
					}
					release := sync.OnceFunc(func() { close(definition.release) })
					defer release()
					deployment := engineTestDeployment(t, definition, childTestDispatcher{})
					definition.reference = deployment.DeploymentRef()
					config := EngineConfig{Limits: limit.limits}
					if durable {
						config.TreeDurability = &recordingTreeDurability{}
					}
					engine, err := NewEngine(config)
					if err != nil {
						t.Fatal(err)
					}
					defer mustCloseEngine(t, engine)
					input, err := EncodeInput(childTestInput{Mode: "recurse:1"})
					if err != nil {
						t.Fatal(err)
					}
					root, err := engine.Start(t.Context(), deployment, input)
					if err != nil {
						t.Fatal(err)
					}
					<-definition.entered
					childID := deriveChildProcessID(deriveEffectID(root.ID(), 1, 0))
					child, found := engine.Process(childID)
					if !found {
						t.Fatal("child was not published")
					}
					if joinErr := child.Join(t.Context()); joinErr != nil {
						t.Fatal(joinErr)
					}
					if result := mustAwait(t, child); result.Status() != StatusCompleted {
						t.Fatalf("child did not complete before wait registration: %s", result.Status())
					}
					release()
					result := mustAwait(t, root)
					failure, failed := result.Termination().Failure()
					if result.Status() != StatusFailed || !failed || failure.Kind() != FailureKindExecution ||
						failure.Code() != "engine.limit.child_wait_signal" || failure.Message() != ErrResourceLimitExceeded.Error() {
						t.Fatalf("child completion budget became a contract error: status=%s failure=%+v", result.Status(), failure)
					}
					if result.Usage() != (Usage{CommittedSteps: 1, PreparedEffects: 2, AcceptedSignals: 1}) {
						t.Fatalf("rejected completion changed committed usage: %+v", result.Usage())
					}
					tree := interruptedTreeSnapshot(t, engine, root, config)
					if len(tree.ProcessSnapshots()) != 2 {
						t.Fatal("failed finalization lost the completed child")
					}
					restoredEngine, err := NewEngine(config)
					if err != nil {
						t.Fatal(err)
					}
					defer mustCloseEngine(t, restoredEngine)
					restored, err := restoredEngine.RestoreTree(t.Context(), deployment, tree)
					if err != nil {
						t.Fatal(err)
					}
					restoredResult := mustAwait(t, restored)
					restoredFailure, failed := restoredResult.Termination().Failure()
					if !failed || restoredFailure != failure || restoredResult.Usage() != result.Usage() {
						t.Fatal("restoration changed the resource failure or usage")
					}
				})
			})
		}
	}
}

type heldChildWaitDefinition struct {
	*childTestDefinition
	entered chan struct{}
	release chan struct{}
}

func (h *heldChildWaitDefinition) Restore(state ExecutionState) (Execution, error) {
	execution, err := h.childTestDefinition.Restore(state)
	if err != nil {
		return nil, err
	}
	return &heldChildWaitExecution{childTestExecution: execution.(*childTestExecution), definition: h}, nil
}

type heldChildWaitExecution struct {
	*childTestExecution
	definition *heldChildWaitDefinition
}

func (h *heldChildWaitExecution) Step(ctx context.Context, signals []Signal) (Transition, error) {
	if h.state.Mode == "recurse:1" && h.state.Phase == "started" {
		close(h.definition.entered)
		select {
		case <-h.definition.release:
		case <-ctx.Done():
			return Transition{}, ctx.Err()
		}
	}
	return h.childTestExecution.Step(ctx, signals)
}

func TestFinalizationFailureRetainsSettlementsWithoutAdoptingCandidate(t *testing.T) {
	for _, durable := range []bool{false, true} {
		name := "ephemeral"
		if durable {
			name = "durable"
		}
		t.Run(name, func(t *testing.T) {
			config := EngineConfig{}
			if durable {
				config.TreeDurability = &recordingTreeDurability{}
			}
			engine, err := NewEngine(config)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { mustCloseEngine(t, engine) })
			key, err := ParseWaitKey("duplicate")
			if err != nil {
				t.Fatal(err)
			}
			wait, err := RequestWait(key, []byte(`{"request":"answer"}`))
			if err != nil {
				t.Fatal(err)
			}
			external, err := NewDispatcherEffect([]byte(`{"kind":"effect","value":"retained"}`))
			if err != nil {
				t.Fatal(err)
			}
			definition := &effectSequenceDefinition{
				descriptor: newEngineTestDefinition(t, "engine.finalization", "effect").Descriptor(),
				effects:    []Effect{external, wait, wait},
			}
			dispatcher := &engineTestDispatcher{policy: ReplayPolicySameIdentity}
			deployment := engineTestDeployment(t, definition, dispatcher)
			input, err := EncodeInput(engineTestInput{Value: "retained"})
			if err != nil {
				t.Fatal(err)
			}
			process, err := engine.Start(t.Context(), deployment, input)
			if err != nil {
				t.Fatal(err)
			}
			result := mustAwait(t, process)
			failure, failed := result.Termination().Failure()
			if result.Status() != StatusFailed || !failed || failure.Code() != "engine.finalize.invalid" {
				t.Fatalf("finalization result=%s failure=%+v", result.Status(), failure)
			}
			snapshot := inspectProcessSnapshot(t, process)
			wire, err := snapshot.wire()
			if err != nil {
				t.Fatal(err)
			}
			if wire.Prepared == nil || len(wire.Prepared.Effects) != 3 {
				t.Fatal("failed finalization erased the performed operations")
			}
			settled := wire.Prepared.Effects[0]
			if !settled.definitelySettled() || settled.Settlement.Status() != SettlementStatusSucceeded ||
				string(settled.Settlement.Payload()) != `{"kind":"result","value":"retained:done"}` {
				t.Fatalf("external settlement changed: %+v", settled)
			}
			state, err := wireJSON.decode[engineTestState](wire.CommittedExecutionState.Payload())
			if err != nil || state.Phase != "ready" || wire.Usage != (Usage{PreparedEffects: 3}) ||
				wire.Mailbox.SignalCursor != 0 || len(wire.Mailbox.Signals) != 0 || len(wire.Mailbox.Waits) != 0 {
				t.Fatalf("failed finalization adopted candidate state: %+v, %v", wire, err)
			}
			tree := interruptedTreeSnapshot(t, engine, process, config)
			restoredEngine, err := NewEngine(config)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { mustCloseEngine(t, restoredEngine) })
			restored, err := restoredEngine.RestoreTree(t.Context(), deployment, tree)
			if err != nil {
				t.Fatal(err)
			}
			if got := mustAwait(t, restored); got.Status() != result.Status() || got.Usage() != result.Usage() {
				t.Fatalf("restored failure changed: %+v", got)
			}
			if !bytes.Equal(inspectProcessSnapshot(t, restored).JSON(), snapshot.JSON()) || dispatcher.calls.Load() != 1 {
				t.Fatal("restoration changed settlement evidence or repeated the external operation")
			}
		})
	}
}
