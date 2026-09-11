package agent

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

func TestCheckpointPreparationFailureCompletesJoinBeforeRuntimeStops(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		runtime, process := newChildCompletionTestProcess(t)
		runtime.engine.durability = &recordingTreeDurability{}
		incarnation, err := newTreeIncarnationID()
		if err != nil {
			t.Fatal(err)
		}
		runtime.incarnation = incarnation
		initial, err := runtime.captureTree()
		if err != nil {
			t.Fatal(err)
		}
		runtime.establishDurableHead(incarnation, initial)
		process.status = StatusPaused
		process.pauseReason = "checkpoint preparation"
		// Inject an unencodable prospective state after a valid acknowledged
		// head. Capture failure must drain the same lifecycle as storage failure.
		process.committedExecutionState = ExecutionState{}
		runtime.run(t.Context())
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		handle := &Process{handle: process.handle}
		_, awaitErr := handle.Await(ctx)
		if _, ok := errors.AsType[*RuntimeError](awaitErr); !ok {
			t.Fatalf("checkpoint preparation did not fail the runtime: %v", awaitErr)
		}
		joinErr := handle.Join(ctx)
		if _, ok := errors.AsType[*RuntimeError](joinErr); !ok {
			t.Fatalf("stopped runtime left Join incomplete: %v", joinErr)
		}
		if !errors.Is(joinErr, runtime.fault) {
			t.Fatalf("Join lost the checkpoint failure: %v", joinErr)
		}
	})
}

func TestJoinAfterTreeFaultCannotPublishChildWait(t *testing.T) {
	runtime, parent := newChildCompletionTestProcess(t)
	runtime.engine.durability = &recordingTreeDurability{}
	childID, _ := newProcessID()
	key, _ := ParseChildKey("completed")
	relation := childProcessRelation(childID, parent.handle.relation, key)
	handle := newProcessHandleState(relation, parent.handle.deploymentRef,
		parent.handle.budget, parent.handle.capabilities, parent.handle.treeLimits, parent.startedAt, StatusCompleted)
	output, err := EncodeOutput(childTestOutput{})
	if err != nil {
		t.Fatal(err)
	}
	termination, err := resolveTermination(terminationFacts{outcome: completedOutcome()})
	if err != nil {
		t.Fatal(err)
	}
	child := &processState{
		handle: handle, status: StatusCompleted, startedAt: parent.startedAt,
		finishedAt: parent.startedAt, finalOutput: output, termination: termination,
	}
	runtime.addProcess(child)
	handle.publishResult(child.result())
	runtime.completeProcessBookkeeping(child)
	waitID, _ := ParseWaitID("wait:completed-child-drain")
	waitKey, _ := ParseWaitKey("completed-child-drain")
	spec := ChildWaitSpec{Key: waitKey, Children: []ProcessID{childID}, Condition: AllChildren(), Boundary: ChildWaitBoundaryDrained}
	payload, err := encodeChildWaitOpened(spec)
	if err != nil {
		t.Fatal(err)
	}
	opening := mustMailboxSignal(t, "signal:completed-child-opened", waitID, payload)
	if openErr := parent.mailbox.openWait(waitKey, opening, false); openErr != nil {
		t.Fatal(openErr)
	}
	parent.usage.AcceptedSignals++
	parent.currentWaitID = waitID
	parent.status = StatusWaiting
	runtime.childWaits[parent.handle.processID] = map[WaitID]*childWaitRegistration{waitID: {waitID: waitID, spec: spec}}
	acknowledged, err := parent.capture()
	if err != nil {
		t.Fatal(err)
	}
	// A sibling commit may fail after this child's result is acknowledged but
	// before its local join is published. The parent's runtime has already failed.
	cause := errors.New("sibling storage acknowledgment lost")
	runtime.fault = cause
	parent.handle.publishRuntimeFailure(&RuntimeError{processID: parent.handle.processID, cause: cause}, acknowledged)
	runtime.completeProcessBookkeeping(parent)
	clear(runtime.queued)
	runtime.processQueue = nil
	beforeUsage := parent.usage
	runtime.publishJoins()
	if !handle.joinDone() || handle.joinError() != nil {
		t.Fatal("completed child's local join did not succeed")
	}
	if !parent.handle.joinDone() || !errors.Is(parent.handle.joinError(), cause) {
		t.Fatal("parent lost its runtime failure")
	}
	if parent.usage != beforeUsage || parent.mailbox.contains(deriveChildWaitSignalID(waitID)) || len(runtime.pendingPublications) != 0 {
		t.Fatal("join published new strategy input after the tree runtime failed")
	}
	if !runtime.canStop() {
		t.Fatal("failed runtime cannot release its drained tree")
	}
}
