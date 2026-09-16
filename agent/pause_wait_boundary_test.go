package agent

import (
	"bytes"
	"testing"
)

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
	runtime.advancePrepared(process)
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
	if process.status != StatusWaiting || process.currentWaitID != waitID {
		t.Fatal("Resume did not re-establish the same wait")
	}
	signal := mustMailboxSignal(t, "signal:answer", waitID, []byte(`{"kind":"answer","value":"approved"}`))
	if accepted, err := runtime.admitSignals(process, []Signal{signal}, signalSourceExternal); err != nil || !accepted {
		t.Fatalf("answer = %t, %v", accepted, err)
	}
	runtime.startStep(process)
	runtime.applyCompletion(<-runtime.completions)
	runtime.advancePrepared(process)
	if process.status != StatusCompleted || controlValue(process.finalOutput.Decode[engineTestOutput]()).Value != "approved" {
		t.Fatal("resumed wait lost its answer")
	}
}
