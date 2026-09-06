package agent

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestOversizedChildCompletionFailsParentAtSafeBoundary(t *testing.T) {
	runtime, parent := newChildCompletionTestProcess(t)
	controller := parent.controller
	parentID := controller.processID
	now := parent.startedAt
	parent.status = StatusWaiting
	waitID, _ := ParseWaitID("wait:oversized-children")
	waitKey, _ := ParseWaitKey("children")
	if err := parent.mailbox.registerWait(waitKey, waitID, true); err != nil {
		t.Fatal(err)
	}
	parent.currentWaitID = waitID
	output, err := EncodeOutput(strings.Repeat("x", 32<<20))
	if err != nil {
		t.Fatal(err)
	}
	termination, err := resolveTermination(terminationFacts{outcome: completedOutcome()})
	if err != nil {
		t.Fatal(err)
	}
	var children []ProcessID
	var last *processState
	for _, name := range []string{"first", "second"} {
		id, _ := newProcessID()
		key, _ := ParseChildKey(name)
		relation := childProcessRelation(id, controller.relation, key)
		last = &processState{
			status: StatusCompleted, startedAt: now, finishedAt: now,
			termination: termination, finalOutput: output,
			controller: &processController{processID: id, relation: relation},
		}
		if !last.result().Valid() {
			t.Fatal("invalid child result")
		}
		runtime.processes[id] = last
		children = append(children, id)
	}
	runtime.childWaits[waitID] = &childWaitRegistration{
		parent: parentID, waitID: waitID,
		spec: ChildWaitSpec{Key: waitKey, Children: children, Condition: AllChildren()},
	}
	runtime.processFinished(last)
	if !parent.pendingControl.failure.Valid() {
		t.Fatal("aggregate encoding failure left the parent waiting without a failure intent")
	}
	if snapshot, err := parent.capture(); err != nil || !snapshot.Valid() {
		t.Fatalf("pending failure snapshot = %v, error = %v", snapshot.Valid(), err)
	}
	if !runtime.advanceOne() {
		t.Fatal("failed parent was not scheduled")
	}
	result := mustAwait(t, &Process{controller: controller})
	failure, present := result.Termination().Failure()
	if result.Status() != StatusFailed || !present || failure.Code() != "engine.child.completion.encoding_failed" {
		t.Fatalf("parent result = %s, failure = %+v", result.Status(), failure)
	}
	if snapshot, err := (&Process{controller: controller}).Snapshot(t.Context()); err != nil || !snapshot.Valid() {
		t.Fatalf("terminal snapshot = %v, error = %v", snapshot.Valid(), err)
	}
}

func TestPendingFailureRetainsUnknownExternalEffect(t *testing.T) {
	runtime, parent := newChildCompletionTestProcess(t)
	parent.recordFailure(FailureKindExecution, "engine.child.completion.encoding_failed", errors.New("completion exceeds signal budget"))
	if parent.status.Terminal() {
		t.Fatal("asynchronous failure terminated a Process before settlement")
	}
	control, err := pendingControlFromWire(parent.pendingControl.wire())
	if err != nil || control.failure != parent.pendingControl.failure {
		t.Fatalf("pending failure round trip = %+v, error = %v", control, err)
	}
	id := deriveEffectID(parent.controller.processID, 1, 0)
	effect, err := NewDispatcherEffect([]byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	record := preparedEffectWire{ID: id, Effect: effect, Phase: effectPhasePending}
	if err := record.settleUnknown(); err != nil {
		t.Fatal(err)
	}
	parent.prepared = &preparedStep{wire: preparedStepWire{Effects: []preparedEffectWire{record}}}
	parent.usage.PreparedEffects = 1
	runtime.advancePrepared(parent)
	result := mustAwait(t, &Process{controller: parent.controller})
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
	controller := newProcessController(rootProcessRelation(parentID), deployment.DeploymentRef(),
		budgetFromLimits(engine.limits), engine.capabilities, engine.treeLimits, now, StatusRunning)
	parent := newProcessState(engine, controller, deployment, execution, state, now, engine.limits)
	runtime := newTreeRuntime(engine, parentID, t.Context(), parent)
	return runtime, parent
}
