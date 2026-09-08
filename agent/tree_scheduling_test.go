package agent

import (
	"errors"
	"testing"
)

// A fixed number of owner turns makes starvation observable without depending
// on goroutine timing or measuring machine-specific execution latency.
const schedulingProgressTurns = 8

func TestTreeSchedulingMakesProgressUnderContinuousQueries(t *testing.T) {
	runtime, process := newChildCompletionTestProcess(t)
	runtime.completions = make(chan treeJobCompletion, 1)
	responses := make(chan processResponse, treeCommandBufferCapacity)
	query := newTreeProcessCommand(process.controller.processID, processCommand{
		kind: commandCapture, response: responses,
	})
	for range treeCommandBufferCapacity {
		runtime.commands <- query
	}
	answered := 0
	advance := func() {
		t.Helper()
		runtime.advanceReadyWork()
		for len(responses) != 0 {
			response := <-responses
			if !errors.Is(response.err, ErrProcessFinished) {
				if response.err != nil || !response.snapshot.Valid() {
					t.Fatalf("ready query failed: %v", response.err)
				}
			}
			answered++
		}
		for len(runtime.commands) < cap(runtime.commands) {
			runtime.commands <- query
		}
	}

	for range schedulingProgressTurns {
		advance()
		if process.attemptSequence != 0 {
			break
		}
	}
	if process.attemptSequence == 0 {
		t.Fatal("continuous queries starved a runnable Process")
	}

	// Hold the real Step result until it is ready, then keep the query lane full
	// while the same owner must adopt and publish that result.
	completion := receiveTreeRuntimeProbe(t, runtime.completions)
	runtime.completions <- completion
	for range schedulingProgressTurns {
		advance()
	}
	select {
	case <-process.controller.done:
		result, err := process.controller.outcome()
		if err != nil || result.Status() != StatusCompleted {
			t.Fatalf("completed work result=%s error=%v", result.Status(), err)
		}
	default:
		t.Fatal("continuous queries starved a completed Step")
	}
	if answered == 0 {
		t.Fatal("ready work starved queries")
	}
}

func TestTreeSchedulingHonorsControlBeforeAdoptingReadyWork(t *testing.T) {
	runtime, process := newChildCompletionTestProcess(t)
	runtime.completions = make(chan treeJobCompletion, 1)
	runtime.startStep(process)
	completion := receiveTreeRuntimeProbe(t, runtime.completions)
	runtime.completions <- completion
	response := make(chan processResponse, 1)
	runtime.commands <- newTreeProcessCommand(process.controller.processID, processCommand{
		kind: commandKill, reason: "stop before adopting the ready Step", response: response,
	})
	for range schedulingProgressTurns {
		runtime.advanceReadyWork()
		select {
		case <-process.controller.done:
			select {
			case reply := <-response:
				if reply.err != nil {
					t.Fatal(reply.err)
				}
			default:
				t.Fatal("ready work overtook the control request")
			}
			result, err := process.controller.outcome()
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
	runtime.popRunnable()
	response := make(chan processResponse, 1)
	for range schedulingProgressTurns {
		runtime.commands <- newTreeProcessCommand(process.controller.processID, processCommand{
			kind: commandCapture, response: response,
		})
		runtime.advanceReadyWork()
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
	if process.controller.status() != StatusRunning {
		t.Fatal("parked state was published before checkpoint acknowledgment")
	}
	runtime.applyTreeCommitCompletion(receiveTreeRuntimeProbe(t, runtime.commitDone))
	checkpoints := durability.treeCheckpoints()
	if len(checkpoints) != 1 || checkpoints[0].Kind() != TreeCheckpointParked ||
		process.controller.status() != StatusPaused {
		t.Fatal("safe checkpoint did not publish the parked state")
	}
}

func TestProcessQueriesDoNotWakePausedExecution(t *testing.T) {
	for _, kind := range []commandKind{commandCapture, commandQueryUnknownEffectIDs} {
		runtime, process := newChildCompletionTestProcess(t)
		process.status = StatusPaused
		process.pauseReason = "wait for explicit resumption"
		process.updateView()
		runtime.popRunnable()
		response := make(chan processResponse, 1)
		runtime.applyProcessCommand(process, processCommand{kind: kind, response: response})
		if reply := <-response; reply.err != nil {
			t.Fatal(reply.err)
		}
		if runtime.advanceReadyWork() {
			t.Fatal("read-only query introduced execution work for a paused Process")
		}
	}
}
