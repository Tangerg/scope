package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestPauseWaitingSurvivesRestoreAndRequiresResume(t *testing.T) {
	for _, durable := range []bool{false, true} {
		for _, answerBeforeResume := range []bool{false, true} {
			t.Run(fmt.Sprintf("durable_%t/answer_before_resume_%t", durable, answerBeforeResume), func(t *testing.T) {
				config := EngineConfig{TreeCommitter: NewMemoryTreeCommitter()}
				committer := &recordingTreeCommitter{}
				if durable {
					config.TreeCommitter = committer
				}
				engine := controlValue(NewEngine(config))
				t.Cleanup(func() {
					if err := engine.Close(context.WithoutCancel(t.Context())); err != nil {
						t.Errorf("source Close: %v", err)
					}
				})
				deployment := engineTestDeployment(t, newEngineTestDefinition(t, "engine.wait", "wait"), nil)
				process := controlValue(engine.Start(t.Context(), deployment, controlValue(EncodePayload(engineTestInput{Value: "pause"}))))
				waitForStatus(t, process, StatusWaiting)
				waitID, _ := inspectProcessSnapshot(t, process).WaitID()
				if err := process.Resume(t.Context()); !errors.Is(err, ErrInvalidProcessControl) {
					t.Fatalf("Resume Waiting: %v", err)
				}
				if err := process.Pause(t.Context(), "inspect wait"); err != nil {
					t.Fatal(err)
				}
				waitForStatus(t, process, StatusPaused)
				if err := process.Pause(t.Context(), "again"); !errors.Is(err, ErrInvalidProcessControl) {
					t.Fatalf("Pause Paused: %v", err)
				}
				var tree TreeSnapshot
				if durable {
					checkpoints := committer.treeCheckpoints()
					tree = checkpoints[len(checkpoints)-1].TreeSnapshot()
				} else {
					tree = controlValue(engine.CaptureTree(t.Context(), process.ID()))
				}
				paused := tree.ProcessSnapshots()[0]
				if got, ok := paused.WaitID(); !ok || got != waitID {
					t.Fatal("pause lost the unanswered wait")
				}
				if kind, ok := paused.WaitKind(); !ok || kind != WaitKindExternal {
					t.Fatal("pause lost the wait kind")
				}
				if err := process.Kill(t.Context(), "handoff"); err != nil {
					t.Fatal(err)
				}
				awaitResult(t, process)
				if err := process.Join(t.Context()); err != nil {
					t.Fatal(err)
				}
				config.TreeCommitter = newSnapshotTestCommitter(tree)
				restoredEngine := controlValue(NewEngine(config))
				t.Cleanup(func() {
					if err := restoredEngine.Close(context.WithoutCancel(t.Context())); err != nil {
						t.Errorf("restored Close: %v", err)
					}
				})
				process = controlValue(restoredEngine.RestoreTree(t.Context(), deployment, controlValue(ParseTreeSnapshot(tree.JSON()))))
				answer := controlValue(NewSignalRequest(controlValue(ParseSignalID("signal:answer")), waitID, []byte(`{"kind":"answer","value":"approved"}`)))
				deliver := func() {
					t.Helper()
					if accepted, err := process.DeliverSignals(t.Context(), answer); !accepted || err != nil {
						t.Fatalf("answer=%t %v", accepted, err)
					}
				}
				if answerBeforeResume {
					deliver()
					snapshot := inspectProcessSnapshot(t, process)
					if _, waiting := snapshot.WaitID(); waiting || snapshot.Status() != StatusPaused {
						t.Fatal("answer did not clear only the wait")
					}
					if _, err := ParseProcessSnapshot(snapshot.JSON()); err != nil {
						t.Fatal(err)
					}
				}
				if err := process.Resume(t.Context()); err != nil {
					t.Fatal(err)
				}
				if !answerBeforeResume {
					waitForStatus(t, process, StatusWaiting)
					deliver()
				}
				result := awaitResult(t, process)
				if err := process.Join(t.Context()); err != nil {
					t.Fatal(err)
				}
				output, _ := result.Output()
				if result.Status() != StatusCompleted || controlValue(output.Decode[engineTestOutput]()).Value != "approved" {
					t.Fatalf("resumed outcome: %+v", result)
				}
			})
		}
	}
}

func TestPauseDiscardsUnadoptedWaitWithoutConsumingItsSignal(t *testing.T) {
	runtime, process := newChildCompletionTestProcess(t)
	process.deployment = engineTestDeployment(t, newEngineTestDefinition(t, "engine.wait", "wait"), nil)
	process.handle.deploymentRef = process.deployment.DeploymentRef()
	input := controlValue(EncodePayload(engineTestInput{Value: "pause"}))
	execution, state, _, err := initializeExecution(t.Context(), process.deployment.Definition(), input)
	if err != nil {
		t.Fatal(err)
	}
	process.execution, process.committedExecutionState = execution, state
	runtime.startStep(process)
	runtime.applyCompletion(<-runtime.completions)
	runtime.advancePrepared(process)
	if runtime.commit != nil {
		runtime.applyTreeCommitCompletion(<-runtime.commitDone)
	}
	runtime.advancePrepared(process)
	if runtime.commit != nil {
		runtime.applyTreeCommitCompletion(<-runtime.commitDone)
	}
	before := controlValue(process.capture())
	runtime.startStep(process)
	runtime.applyCompletion(<-runtime.completions)
	if process.prepared == nil || process.prepared.Intent.Kind() != TransitionKindWait {
		t.Fatal("test did not reach the prepared Wait boundary")
	}
	waitID, _ := process.prepared.Intent.WaitID()
	response := make(chan processResponse, 1)
	runtime.applyProcessCommand(process, processCommand{kind: commandPause, reason: "inspect", response: response})
	if err := (<-response).err; err != nil {
		t.Fatal(err)
	}
	runtime.advancePrepared(process)
	if runtime.commit != nil {
		runtime.applyTreeCommitCompletion(<-runtime.commitDone)
	}
	if process.status != StatusPaused || process.prepared != nil || process.currentWaitID.Valid() {
		t.Fatal("accepted Pause was trapped behind Wait")
	}
	runtime.applyCompletion(<-runtime.completions)
	after := controlValue(process.capture())
	if !bytes.Equal(before.CommittedExecutionState().Payload(), after.CommittedExecutionState().Payload()) || process.committedSteps != before.state.CommittedSteps || len(process.mailbox.pending()) != 1 {
		t.Fatal("discarded Wait consumed input or installed candidate state")
	}
	if _, err := runtime.captureTree(); err != nil {
		t.Fatalf("paused tree is not restorable: %v", err)
	}
	runtime.applyProcessCommand(process, processCommand{kind: commandResume, response: response})
	if err := (<-response).err; err != nil {
		t.Fatal(err)
	}
	runtime.startStep(process)
	runtime.applyCompletion(<-runtime.completions)
	runtime.advancePrepared(process)
	if runtime.commit != nil {
		runtime.applyTreeCommitCompletion(<-runtime.commitDone)
	}
	if process.status != StatusWaiting || process.currentWaitID != waitID {
		t.Fatal("Resume did not re-establish the same wait")
	}
	signal := mustMailboxSignal(t, "signal:answer", waitID, []byte(`{"kind":"answer","value":"approved"}`))
	if events, err := runtime.admitSignals(process, []Signal{signal}, signalSourceExternal); err != nil || len(events) != 1 {
		t.Fatalf("answer events = %d, error = %v", len(events), err)
	}
	runtime.startStep(process)
	runtime.applyCompletion(<-runtime.completions)
	runtime.advancePrepared(process)
	if runtime.commit != nil {
		runtime.applyTreeCommitCompletion(<-runtime.commitDone)
	}
	if process.status != StatusCompleted || controlValue(process.finalOutput.Decode[engineTestOutput]()).Value != "approved" {
		t.Fatal("resumed wait lost its answer")
	}
}
