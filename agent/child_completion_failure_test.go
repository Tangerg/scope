package agent

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestChildCompletionPreservesParentSchedulingAcrossRestore(t *testing.T) {
	for _, test := range []struct {
		mode   string
		status Status
	}{
		{mode: "wait:paused_parent", status: StatusPaused},
		{mode: "wait:external_parent", status: StatusWaiting},
	} {
		t.Run(test.mode, func(t *testing.T) {
			dispatcher := newBlockingChildDispatcher("first", "second", "third")
			t.Cleanup(dispatcher.ReleaseAll)
			deployment := newChildTestDeploymentWithDispatcher(t, dispatcher)
			engine, err := NewEngine(EngineConfig{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { mustCloseEngine(t, engine) })
			input, _ := EncodeInput(childTestInput{Mode: test.mode})
			root, err := engine.Start(t.Context(), deployment, input)
			if err != nil {
				t.Fatal(err)
			}
			waitForStatus(t, root, test.status)
			waitID, _ := inspectProcessSnapshot(t, root).WaitID()
			dispatcher.ReleaseAll()
			awaitChildren(t, engine, directChildIDs(t, engine, root.ID()))
			if inspectProcessSnapshot(t, root).Status().Terminal() {
				t.Fatalf("child completion terminated parent: %+v", mustAwait(t, root).Termination())
			}
			snapshot := inspectProcessSnapshot(t, root)
			if snapshot.Status() != test.status {
				t.Fatalf("child completion changed parent status to %s, want %s", snapshot.Status(), test.status)
			}
			if got, _ := snapshot.WaitID(); got != waitID {
				t.Fatalf("child completion changed current wait to %s, want %s", got, waitID)
			}
			tree, err := engine.CaptureTree(t.Context(), root.ID())
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := ParseTreeSnapshot(tree.JSON())
			if err != nil {
				t.Fatal(err)
			}
			restoredEngine, err := NewEngine(EngineConfig{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { mustCloseEngine(t, restoredEngine) })
			restored, err := restoredEngine.RestoreTree(t.Context(), deployment, parsed)
			if err != nil {
				t.Fatal(err)
			}
			for _, process := range []*Process{root, restored} {
				if inspectProcessSnapshot(t, process).Status() != test.status {
					t.Fatalf("parent status before resume = %s, want %s", inspectProcessSnapshot(t, process).Status(), test.status)
				}
				if test.status == StatusPaused {
					if resumeErr := process.Resume(t.Context()); resumeErr != nil {
						t.Fatal(resumeErr)
					}
				} else {
					id, _ := ParseSignalID("signal:external-answer")
					request, _ := NewSignalRequest(id, waitID, []byte(`{}`))
					if accepted, deliverErr := process.DeliverSignals(t.Context(), request); deliverErr != nil || !accepted {
						t.Fatalf("external answer accepted = %t, error = %v", accepted, deliverErr)
					}
				}
				output := childTestResult(t, mustAwait(t, process))
				if !slices.Equal(output.CompletedKeys, []string{"first", "second", "third"}) {
					t.Fatalf("completed children = %v", output.CompletedKeys)
				}
			}
		})
	}
}

func TestOversizedChildCompletionFailsParentAtSafeBoundary(t *testing.T) {
	runtime, parent := newChildCompletionTestProcess(t)
	handle := parent.handle
	parentID := handle.processID
	now := parent.startedAt
	parent.status = StatusWaiting
	waitID, _ := ParseWaitID("wait:oversized-children")
	waitKey, _ := ParseWaitKey("children")
	if err := parent.mailbox.openWait(waitKey, mustMailboxSignal(t, "signal:oversized-opened", waitID, []byte(`{}`)), true); err != nil {
		t.Fatal(err)
	}
	parent.currentWaitID = waitID
	parent.usage.AcceptedSignals++
	output, err := EncodeOutput(strings.Repeat("x", 32<<20))
	if err != nil {
		t.Fatal(err)
	}
	termination, err := (terminationFacts{outcome: completedOutcome()}).resolve()
	if err != nil {
		t.Fatal(err)
	}
	var children []ProcessID
	var last *processState
	for _, name := range []string{"first", "second"} {
		id, _ := newProcessID()
		key, _ := ParseChildKey(name)
		relation := childProcessRelation(id, handle.relation, key)
		last = &processState{
			status: StatusCompleted, startedAt: now, finishedAt: now,
			termination: termination, finalOutput: output,
			handle: &processHandleState{processID: id, relation: relation},
		}
		if !last.result().Valid() {
			t.Fatal("invalid child result")
		}
		runtime.addProcess(last)
		children = append(children, id)
	}
	runtime.childWaits[parentID] = map[WaitID]*childWaitRegistration{waitID: {
		waitID: waitID,
		spec:   ChildWaitSpec{Boundary: ChildWaitBoundaryResult, Key: waitKey, Children: children, Condition: AllChildren()},
	}}
	runtime.propagateProcessTermination(last)
	if !parent.pendingControl.failure.Valid() {
		t.Fatal("aggregate encoding failure left the parent waiting without a failure intent")
	}
	if snapshot, err := parent.capture(); err != nil || !snapshot.Valid() {
		t.Fatalf("pending failure snapshot = %v, error = %v", snapshot.Valid(), err)
	}
	if !runtime.advanceOne() {
		t.Fatal("failed parent was not scheduled")
	}
	result := mustAwait(t, &Process{handle: handle})
	failure, present := result.Termination().Failure()
	if result.Status() != StatusFailed || !present || failure.Code() != "engine.child.wait.satisfaction.encoding_failed" {
		t.Fatalf("parent result = %s, failure = %+v", result.Status(), failure)
	}
	if snapshot, err := runtime.processes[handle.processID].capture(); err != nil || !snapshot.Valid() {
		t.Fatalf("terminal snapshot = %v, error = %v", snapshot.Valid(), err)
	}
}

func TestPendingFailureRetainsUnknownExternalEffect(t *testing.T) {
	runtime, parent := newChildCompletionTestProcess(t)
	parent.recordFailure(FailureKindExecution, "engine.child.wait.satisfaction.encoding_failed", errors.New("completion exceeds signal budget"))
	if parent.status.Terminal() {
		t.Fatal("asynchronous failure terminated a Process before settlement")
	}
	control, err := pendingControlFromWire(parent.pendingControl.wire())
	if err != nil || control.failure != parent.pendingControl.failure {
		t.Fatalf("pending failure round trip = %+v, error = %v", control, err)
	}
	id := parent.handle.processID.effectID(1, 0)
	effect, err := NewDispatcherEffect([]byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	record := preparedEffect{ID: id, Effect: effect, Phase: effectPhasePending}
	if err := record.settleUnknown(); err != nil {
		t.Fatal(err)
	}
	parent.prepared = &preparedStep{Effects: []preparedEffect{record}}
	parent.usage.PreparedEffects = 1
	runtime.advancePrepared(parent)
	result := mustAwait(t, &Process{handle: parent.handle})
	if result.Status() != StatusFailed || !slices.Equal(result.Termination().UnresolvedEffectIDs(), []EffectID{id}) {
		t.Fatalf("failure lost unresolved effect: %+v", result.Termination())
	}
}

func newChildCompletionTestProcess(t *testing.T) (*treeRuntime, *processState) {
	t.Helper()
	engine, err := NewEngine(EngineConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mustCloseEngine(t, engine) })
	deployment := newChildTestDeployment(t)
	input, _ := EncodeInput(childTestInput{Mode: "leaf"})
	execution, state, _, err := initializeExecution(deployment.Definition(), input)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Round(0).UTC()
	parentID, _ := newProcessID()
	handle := newProcessHandleState(rootProcessRelation(parentID), deployment.DeploymentRef(),
		engine.limits.budget(), engine.capabilities, engine.treeLimits, now, StatusRunning)
	parent := newProcessState(handle, deployment, execution, state, now, engine.limits)
	runtime := newTreeRuntime(engine, parentID, t.Context(), parent)
	return runtime, parent
}
