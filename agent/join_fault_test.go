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
		runtime.writer.committer = &recordingTreeCommitter{}
		incarnation := newTreeIncarnationID()
		runtime.writer.identity = incarnation
		initial, err := runtime.captureTree()
		if err != nil {
			t.Fatal(err)
		}
		runtime.writer.identity = incarnation
		runtime.writer.establish(initial)
		process.pause = pause{reason: "checkpoint preparation"}
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
	runtime.writer.committer = &recordingTreeCommitter{}
	childID := newProcessID()
	key, _ := ParseChildKey("completed")
	relation := childProcessRelation(childID, parent.handle.relation, key)
	handle := newProcessHandle(relation, parent.handle.deployment, Digest{},
		parent.handle.budget, parent.handle.capabilities, parent.handle.startedAt)
	output, err := EncodePayload(childTestOutput{})
	if err != nil {
		t.Fatal(err)
	}
	termination, err := (terminationInputs{outcome: completedOutcome()}).resolve()
	if err != nil {
		t.Fatal(err)
	}
	child := &processState{
		handle: handle, finish: &processFinish{Termination: termination, FinishedAt: parent.handle.startedAt, Output: output},
	}
	runtime.addProcess(child)
	handle.publishResult(child.result())
	runtime.finishProcessBookkeeping(child)
	waitID, _ := ParseWaitID("wait:completed-child-drain")
	waitKey, _ := ParseWaitKey("completed-child-drain")
	spec := ChildWaitSpec{Key: waitKey, Children: []ProcessID{childID}, Condition: AllChildren(), Boundary: ChildWaitBoundaryDrained}
	openTestChildWait(t, &parent.mailbox, "signal:engine:completed-child-opened", waitID, spec)
	parent.currentWaitID = waitID
	_, err = parent.capture()
	if err != nil {
		t.Fatal(err)
	}
	// A sibling commit may fail after this child's result is acknowledged but
	// before its local join is published. The parent's runtime has already failed.
	cause := errors.New("sibling storage acknowledgment lost")
	runtime.fault = cause
	parent.handle.publishRuntimeFailure(&RuntimeError{processID: parent.handle.processID, cause: cause})
	runtime.finishProcessBookkeeping(parent)
	runtime.runQueue.clear()
	beforeUsage := parent.usage()
	runtime.publishJoins()
	if !handle.joinDone() || handle.joinError() != nil {
		t.Fatal("completed child's local join did not succeed")
	}
	if !parent.handle.joinDone() || !errors.Is(parent.handle.joinError(), cause) {
		t.Fatal("parent lost its runtime failure")
	}
	if parent.usage() != beforeUsage || parent.mailbox.contains(waitID.childWaitSignalID()) || len(runtime.publications) != 0 {
		t.Fatal("join published new strategy input after the tree runtime failed")
	}
	if !runtime.canStop() {
		t.Fatal("failed runtime cannot release its drained tree")
	}
}
