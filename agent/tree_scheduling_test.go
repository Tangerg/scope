package agent

import (
	"errors"
	"fmt"
	"testing"
)

// A fixed number of owner turns makes starvation observable without depending
// on goroutine timing or measuring machine-specific execution latency.
const schedulingProgressTurns = 8

func TestTreeSchedulingAdvancesUnderContinuousCommittedSignals(t *testing.T) {
	for _, readyCompletion := range []bool{false, true} {
		t.Run(fmt.Sprintf("ready_completion=%t", readyCompletion), func(t *testing.T) {
			owner, process := newChildCompletionTestProcess(t)
			owner.completions = make(chan treeJobCompletion, 1)
			if readyCompletion {
				owner.startStep(process)
				completion := receiveTreeRuntimeProbe(t, owner.completions)
				owner.completions <- completion
			}
			replies := make(processReply, treeCommandBufferCapacity)
			nextSignal, accepted := 0, 0
			progressed := false
			for range schedulingProgressTurns {
				for len(owner.signalCommands) < cap(owner.signalCommands) {
					id := controlValue(ParseSignalID(fmt.Sprintf("signal:scheduling-%d", nextSignal)))
					nextSignal++
					request := controlValue(NewSignalRequest(id, WaitID{}, []byte(`null`)))
					owner.signalCommands <- processTreeCommand{
						processID: process.handle.processID(), request: deliverSignalsRequest{requests: []SignalRequest{request}}, reply: replies,
					}
				}
				owner.advanceReadyWork()
				if owner.writer.committing() {
					owner.applyTreeCommitCompletion(receiveTreeRuntimeProbe(t, owner.writer.done))
				}
				for len(replies) != 0 {
					reply := <-replies
					if reply.err != nil && !errors.Is(reply.err, ErrProcessFinished) {
						t.Fatal(reply.err)
					}
					if reply.accepted {
						accepted++
					}
				}
				if readyCompletion {
					progressed = owner.jobs.get(process.handle.processID()) == nil && (process.prepared != nil || process.committedSteps != 0)
				} else {
					progressed = process.attemptSequence != 0
				}
				if progressed {
					break
				}
			}
			if !progressed || accepted == 0 {
				t.Fatalf("valid Signal commits prevented progress: progressed=%t accepted=%d", progressed, accepted)
			}
			if !readyCompletion {
				owner.applyCompletion(receiveTreeRuntimeProbe(t, owner.completions))
			}
		})
	}
}

func TestTreeSchedulingMakesProgressUnderContinuousRequests(t *testing.T) {
	runtime, process := newChildCompletionTestProcess(t)
	runtime.completions = make(chan treeJobCompletion, 1)
	responses := make(chan TreeInspection, treeCommandBufferCapacity)
	commandResponses := make(processReply, treeCommandBufferCapacity)
	answered, commandsAnswered := 0, 0
	refill := func() {
		for len(runtime.inspections) < cap(runtime.inspections) {
			runtime.inspections <- responses
		}
		for len(runtime.controlCommands) < cap(runtime.controlCommands) {
			runtime.controlCommands <- processTreeCommand{
				processID: process.handle.processID(), request: resumeRequest{}, reply: commandResponses,
			}
		}
	}
	refill()
	advance := func() {
		t.Helper()
		runtime.advanceReadyWork()
		if runtime.writer.committing() {
			runtime.applyTreeCommitCompletion(receiveTreeRuntimeProbe(t, runtime.writer.done))
		}
		runtime.tryInspection()
		for len(responses) != 0 {
			if inspection := <-responses; inspection.RootID != runtime.rootID {
				t.Fatalf("ready query answered tree %s", inspection.RootID)
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
		if err != nil || result.Termination().Status() != StatusCompleted {
			t.Fatalf("completed work result=%s error=%v", result.Termination().Status(), err)
		}
	default:
		t.Fatal("continuous queries starved a completed Step")
	}
	if answered == 0 || commandsAnswered == 0 {
		t.Fatal("ready work starved queries or commands")
	}
}

func TestTreeSchedulingHonorsControlBeforeAdoptingReadyWork(t *testing.T) {
	for _, signalCommit := range []bool{false, true} {
		t.Run(fmt.Sprintf("signal_commit=%t", signalCommit), func(t *testing.T) {
			owner, process := newChildCompletionTestProcess(t)
			owner.completions = make(chan treeJobCompletion, 1)
			owner.startStep(process)
			completion := receiveTreeRuntimeProbe(t, owner.completions)
			owner.completions <- completion
			if signalCommit {
				signal := controlValue(NewSignalRequest(controlValue(ParseSignalID("signal:before-kill")), WaitID{}, []byte(`null`)))
				owner.signalCommands <- processTreeCommand{
					processID: process.handle.processID(), request: deliverSignalsRequest{requests: []SignalRequest{signal}}, reply: make(processReply, 1),
				}
				owner.advanceReadyWork()
				if !owner.writer.committing() {
					t.Fatal("Signal did not start a commit")
				}
			}
			response := make(processReply, 1)
			owner.controlCommands <- processTreeCommand{
				processID: process.handle.processID(), request: killRequest{reason: "stop before adopting the ready Step"}, reply: response,
			}
			for range schedulingProgressTurns {
				owner.advanceReadyWork()
				if owner.writer.committing() {
					owner.applyTreeCommitCompletion(receiveTreeRuntimeProbe(t, owner.writer.done))
				}
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
					if err != nil || result.Termination().Status() != StatusKilled {
						t.Fatalf("controlled result=%s error=%v", result.Termination().Status(), err)
					}
					return
				default:
				}
			}
			t.Fatal("ready work starved the control request")
		})
	}
}

func TestTreeSchedulingCommitsParkedStateUnderContinuousQueries(t *testing.T) {
	runtime, process := newChildCompletionTestProcess(t)
	committer := &recordingTreeCommitter{}
	runtime.writer.committer = committer
	runtime.writer.done = make(chan treeCommitCompletion, 1)
	incarnation := newTreeIncarnationID()
	runtime.writer.identity = incarnation
	initial, err := runtime.captureTree()
	if err != nil {
		t.Fatal(err)
	}
	runtime.writer.establish(initial)
	process.pause = pause{reason: "wait for explicit resumption"}
	runtime.dequeueProcess()
	response := make(chan TreeInspection, 1)
	for range schedulingProgressTurns {
		runtime.inspections <- response
		runtime.advanceReadyWork()
		runtime.tryInspection()
		<-response
		if runtime.writer.committing() {
			break
		}
	}
	if !runtime.writer.committing() {
		t.Fatal("continuous queries prevented a safe checkpoint")
	}
	if inspectionStatus(t, runtime, process.handle.processID()) != StatusRunning {
		t.Fatal("parked state was published before checkpoint acknowledgment")
	}
	runtime.applyTreeCommitCompletion(receiveTreeRuntimeProbe(t, runtime.writer.done))
	checkpoints := committer.treeCheckpoints()
	if len(checkpoints) != 1 || checkpoints[0].Kind() != TreeCheckpointKindParked ||
		inspectionStatus(t, runtime, process.handle.processID()) != StatusPaused {
		t.Fatal("safe checkpoint did not publish the parked state")
	}
}

func TestTreeInspectionDoesNotWakePausedExecution(t *testing.T) {
	runtime, process := newChildCompletionTestProcess(t)
	process.pause = pause{reason: "wait for explicit resumption"}

	runtime.dequeueProcess()
	runtime.tryStartCheckpoint()
	runtime.applyTreeCommitCompletion(receiveTreeRuntimeProbe(t, runtime.writer.done))
	response := make(chan TreeInspection, 1)
	runtime.inspections <- response
	if !runtime.tryInspection() {
		t.Fatal("ready inspection was not served")
	}
	<-response
	if runtime.advanceReadyWork() {
		t.Fatal("read-only query introduced execution work for a paused Process")
	}
}

func inspectionStatus(t *testing.T, runtime *treeRuntime, processID ProcessID) Status {
	t.Helper()
	response := make(chan TreeInspection, 1)
	runtime.inspections <- response
	if !runtime.tryInspection() {
		t.Fatal("inspection was not served")
	}
	process, ok := (<-response).Process(processID)
	if !ok {
		t.Fatal("Process missing from inspection")
	}
	return process.Snapshot.Status()
}
