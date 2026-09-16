package agent

import (
	"errors"
	"testing"
)

// A fixed number of owner turns makes starvation observable without depending
// on goroutine timing or measuring machine-specific execution latency.
const schedulingProgressTurns = 8

func TestTreeSchedulingMakesProgressUnderContinuousRequests(t *testing.T) {
	runtime, process := newChildCompletionTestProcess(t)
	runtime.completions = make(chan treeJobCompletion, 1)
	responses := make(chan treeInspectionResponse, treeCommandBufferCapacity)
	commandResponses := make(chan processResponse, treeCommandBufferCapacity)
	answered, commandsAnswered := 0, 0
	refill := func() {
		for len(runtime.inspections) < cap(runtime.inspections) {
			runtime.inspections <- responses
		}
		for len(runtime.processCommands) < cap(runtime.processCommands) {
			runtime.processCommands <- newTreeProcessCommand(process.handle.processID, processCommand{
				kind: commandResume, response: commandResponses,
			})
		}
	}
	refill()
	advance := func() {
		t.Helper()
		runtime.advanceReadyWork()
		runtime.tryInspection()
		for len(responses) != 0 {
			response := <-responses
			if response.err != nil || response.inspection.RootID != runtime.rootID {
				t.Fatalf("ready query failed: %v", response.err)
			}
			answered++
		}
		for len(commandResponses) != 0 {
			response := <-commandResponses
			if response.err != nil && !errors.Is(response.err, ErrProcessFinished) && !errors.Is(response.err, ErrInvalidProcessControl) {
				t.Fatal(response.err)
			}
			commandsAnswered++
		}
		refill()
	}

	advance()
	if process.attemptSequence == 0 {
		t.Fatal("ready commands prevented queued work in the same owner turn")
	}

	// Hold the real Step result until it is ready, then keep the query lane full
	// while the same owner must adopt and publish that result.
	completion := receiveTreeRuntimeProbe(t, runtime.completions)
	runtime.completions <- completion
	for range schedulingProgressTurns {
		advance()
	}
	select {
	case <-process.handle.outcomePublished:
		result, err := process.handle.outcome()
		if err != nil || result.Status() != StatusCompleted {
			t.Fatalf("completed work result=%s error=%v", result.Status(), err)
		}
	default:
		t.Fatal("continuous queries starved a completed Step")
	}
	if answered == 0 || commandsAnswered == 0 {
		t.Fatal("ready work starved queries or commands")
	}
}

func TestTreeSchedulingHonorsControlBeforeAdoptingReadyWork(t *testing.T) {
	runtime, process := newChildCompletionTestProcess(t)
	runtime.completions = make(chan treeJobCompletion, 1)
	runtime.startStep(process)
	completion := receiveTreeRuntimeProbe(t, runtime.completions)
	runtime.completions <- completion
	response := make(chan processResponse, 1)
	runtime.processCommands <- newTreeProcessCommand(process.handle.processID, processCommand{
		kind: commandKill, reason: "stop before adopting the ready Step", response: response,
	})
	for range schedulingProgressTurns {
		runtime.advanceReadyWork()
		select {
		case <-process.handle.outcomePublished:
			select {
			case reply := <-response:
				if reply.err != nil {
					t.Fatal(reply.err)
				}
			default:
				t.Fatal("ready work overtook the control request")
			}
			result, err := process.handle.outcome()
			if err != nil || result.Status() != StatusKilled {
				t.Fatalf("controlled result=%s error=%v", result.Status(), err)
			}
			return
		default:
		}
	}
	t.Fatal("ready work starved the control request")
}

func TestTreeSchedulingCommitsParkedStateUnderContinuousQueries(t *testing.T) {
	runtime, process := newChildCompletionTestProcess(t)
	durability := &recordingTreeDurability{}
	runtime.engine.durability = durability
	runtime.commitDone = make(chan treeCommitCompletion, 1)
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
	process.pauseReason = "wait for explicit resumption"
	runtime.dequeueProcess()
	response := make(chan treeInspectionResponse, 1)
	for range schedulingProgressTurns {
		runtime.inspections <- response
		runtime.advanceReadyWork()
		runtime.tryInspection()
		if reply := <-response; reply.err != nil {
			t.Fatal(reply.err)
		}
		if runtime.commit != nil {
			break
		}
	}
	if runtime.commit == nil {
		t.Fatal("continuous queries prevented a safe checkpoint")
	}
	if inspectionStatus(t, runtime, process.handle.processID) != StatusRunning {
		t.Fatal("parked state was published before checkpoint acknowledgment")
	}
	runtime.applyTreeCommitCompletion(receiveTreeRuntimeProbe(t, runtime.commitDone))
	checkpoints := durability.treeCheckpoints()
	if len(checkpoints) != 1 || checkpoints[0].Kind() != TreeCheckpointParked ||
		inspectionStatus(t, runtime, process.handle.processID) != StatusPaused {
		t.Fatal("safe checkpoint did not publish the parked state")
	}
}

func TestTreeInspectionDoesNotWakePausedExecution(t *testing.T) {
	runtime, process := newChildCompletionTestProcess(t)
	process.status = StatusPaused
	process.pauseReason = "wait for explicit resumption"

	runtime.dequeueProcess()
	response := make(chan treeInspectionResponse, 1)
	runtime.inspections <- response
	if !runtime.tryInspection() {
		t.Fatal("ready inspection was not served")
	}
	if reply := <-response; reply.err != nil {
		t.Fatal(reply.err)
	}
	if runtime.advanceReadyWork() {
		t.Fatal("read-only query introduced execution work for a paused Process")
	}
}

func inspectionStatus(t *testing.T, runtime *treeRuntime, processID ProcessID) Status {
	t.Helper()
	response := make(chan treeInspectionResponse, 1)
	runtime.inspections <- response
	if !runtime.tryInspection() {
		t.Fatal("inspection was not served")
	}
	reply := <-response
	if reply.err != nil {
		t.Fatal(reply.err)
	}
	process, ok := reply.inspection.Process(processID)
	if !ok {
		t.Fatal("Process missing from inspection")
	}
	return process.Snapshot.Status()
}
