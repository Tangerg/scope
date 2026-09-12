package agent

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"
)

func TestChildWaitCompletionAndTerminationRemainWithinParent(t *testing.T) {
	runtime, first := waitingOwnerFixture(t, 3)
	parentID, _ := first.handle.relation.ParentID()
	parent := runtime.processes[parentID]
	waitID := parent.currentWaitID
	second := runtime.processes[runtime.childrenByParent[parentID][1]]
	second.installTermination(first.termination, first.finalOutput, first.finishedAt)
	runtime.finishIfTerminal(second)
	for ownerID := range runtime.childWaits {
		owner := runtime.processes[ownerID]
		if ownerID != parentID {
			if owner.mailbox.pendingCount() != 0 || owner.status != StatusWaiting {
				t.Fatal("child completion changed an unrelated parent's wait")
			}
			continue
		}
		pending := owner.mailbox.pending()
		if len(pending) != 1 {
			t.Fatalf("parent received %d completion signals, want 1", len(pending))
		}
		completed, err := ParseChildWaitSatisfied(pending[0])
		if err != nil {
			t.Fatal(err)
		}
		if completed.WaitID() != waitID || completed.Boundary() != ChildWaitBoundaryResult {
			t.Fatal("completion lost the parent's active wait")
		}
		var got []ProcessID
		for _, outcome := range completed.Outcomes() {
			got = append(got, outcome.Result().ProcessID())
		}
		if !slices.Equal(got, []ProcessID{first.handle.processID, second.handle.processID}) {
			t.Fatalf("completion outcomes=%v", got)
		}
	}
	closed, err := parent.mailbox.commit(1)
	if err != nil {
		t.Fatal(err)
	}
	for _, waitID := range closed {
		runtime.unregisterChildWait(parentID, waitID)
	}
	if len(runtime.childWaits) != 2 {
		t.Fatal("consuming one parent's completion removed another wait")
	}
	for ownerID := range runtime.childWaits {
		owner := runtime.processes[ownerID]
		owner.installTermination(first.termination, first.finalOutput, first.finishedAt)
		runtime.finishIfTerminal(owner)
		if len(runtime.childWaits) != 1 {
			t.Fatal("terminating one parent removed another parent's wait")
		}
		break
	}
}

func TestJoinReadinessSurvivesCommitAndFreeze(t *testing.T) {
	for _, barrier := range []string{"commit", "freeze"} {
		t.Run(barrier, func(t *testing.T) {
			runtime, completed := waitingOwnerFixture(t, 1)
			if barrier == "commit" {
				runtime.commit = &treeCommit{}
			} else {
				runtime.freeze = &activeTreeFreeze{}
			}
			if runtime.publishJoins() || completed.handle.joinDone() {
				t.Fatal("join crossed a publication barrier")
			}
			runtime.commit = nil
			runtime.freeze = nil
			if !runtime.publishJoins() || !completed.handle.joinDone() || completed.handle.joinError() != nil {
				t.Fatal("releasing the barrier lost a ready join")
			}
			if runtime.publishJoins() {
				t.Fatal("unchanged tree published another join")
			}
		})
	}
}

func waitingOwnerFixture(b testing.TB, parents int) (*treeRuntime, *processState) {
	b.Helper()
	engine, err := NewEngine(EngineConfig{})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if closeErr := engine.Close(context.WithoutCancel(b.Context())); closeErr != nil {
			b.Error(closeErr)
		}
	})
	deployment := newChildTestDeployment(b)
	input, err := EncodeInput(childTestInput{Mode: "leaf"})
	if err != nil {
		b.Fatal(err)
	}
	now := time.Now().Round(0).UTC()
	makeProcess := func(parent *processState, name string) *processState {
		id, err := newProcessID()
		if err != nil {
			b.Fatal(err)
		}
		relation := rootProcessRelation(id)
		if parent != nil {
			key, keyErr := ParseChildKey(name)
			if keyErr != nil {
				b.Fatal(keyErr)
			}
			relation = childProcessRelation(id, parent.handle.relation, key)
		}
		execution, state, _, err := initializeExecution(deployment.Definition(), input)
		if err != nil {
			b.Fatal(err)
		}
		handle := newProcessHandleState(relation, deployment.DeploymentRef(),
			engine.limits.budget(), engine.capabilities, engine.treeLimits, now, StatusRunning)
		return newProcessState(handle, deployment, execution, state, now, engine.limits)
	}
	root := makeProcess(nil, "root")
	runtime := newTreeRuntime(engine, root.handle.processID, b.Context(), root)
	var notified *processState
	for index := range parents {
		parent := makeProcess(root, fmt.Sprintf("parent-%d", index))
		first := makeProcess(parent, "first")
		second := makeProcess(parent, "second")
		for _, process := range []*processState{parent, first, second} {
			runtime.addProcess(process)
		}
		output, err := EncodeOutput(childTestOutput{})
		if err != nil {
			b.Fatal(err)
		}
		termination, err := (terminationFacts{outcome: completedOutcome()}).resolve()
		if err != nil {
			b.Fatal(err)
		}
		first.installTermination(termination, output, now)
		first.handle.publishResult(first.result())
		runtime.completeProcessBookkeeping(first)
		waitID, err := ParseWaitID(fmt.Sprintf("wait:parent-%d", index))
		if err != nil {
			b.Fatal(err)
		}
		key, err := ParseWaitKey("children")
		if err != nil {
			b.Fatal(err)
		}
		spec := ChildWaitSpec{
			Key: key, Children: []ProcessID{first.handle.processID, second.handle.processID},
			Boundary: ChildWaitBoundaryResult, Condition: AllChildren(),
		}
		payload, err := encodeChildWaitOpened(spec)
		if err != nil {
			b.Fatal(err)
		}
		opening := mustMailboxSignal(b, fmt.Sprintf("signal:parent-%d", index), waitID, payload)
		if err := parent.mailbox.openWait(key, opening, false); err != nil {
			b.Fatal(err)
		}
		parent.usage.AcceptedSignals++
		if _, err := parent.mailbox.commit(1); err != nil {
			b.Fatal(err)
		}
		parent.currentWaitID = waitID
		parent.status = StatusWaiting
		if _, satisfied, err := runtime.registerChildWait(parent.handle.processID, waitID, spec); err != nil || satisfied {
			b.Fatalf("register wait satisfied=%t error=%v", satisfied, err)
		}
		notified = first
	}
	return runtime, notified
}
