package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestStepCannotConsumeBudgetReservedAtUint64Boundary(t *testing.T) {
	const maxUint64 = ^uint64(0)
	processID, err := ParseProcessID("process:resource-boundary")
	if err != nil {
		t.Fatal(err)
	}
	handle := newProcessHandleState(
		rootProcessRelation(processID), DeploymentRef{},
		Budget{Steps: maxUint64, Effects: maxUint64, Signals: maxUint64},
		CapabilitySet{}, DefaultTreeLimits(), time.Now(), StatusRunning,
	)
	process := &processState{
		handle:         handle,
		status:         StatusRunning,
		committedSteps: maxUint64 - 1,
		limits: Limits{
			MaxSteps: maxUint64, MaxEffects: maxUint64,
			MaxSignals: maxUint64, MaxPendingSignals: maxUint64,
		},
		budget:         Budget{Steps: maxUint64, Effects: maxUint64, Signals: maxUint64},
		reservedBudget: Budget{Steps: 1, Effects: 1, Signals: 1},
		mailbox:        newSignalMailbox(),
	}

	schedulingFailure := process.stepSchedulingFailure()
	if schedulingFailure == nil {
		t.Fatal("step scheduling failure is nil")
	}
	runtime := &treeRuntime{engine: &Engine{}}
	runtime.failProcess(process, schedulingFailure.kind, schedulingFailure.code, schedulingFailure.cause)

	if process.status != StatusFailed {
		t.Fatalf("status = %s, want %s", process.status, StatusFailed)
	}
	failure, present := process.termination.Failure()
	if !present || failure.Code() != "engine.limit.steps" {
		t.Fatalf("failure = %+v, present = %t", failure, present)
	}
}

func TestPreparedStepFinalizationCountsEveryImmediateChildSignal(t *testing.T) {
	limits := Limits{
		MaxSteps: 10, MaxEffects: 10, MaxSignals: 3, MaxPendingSignals: 10,
	}
	process := &processState{
		limits: limits,
		budget: budgetFromLimits(limits),
	}
	mailbox := newSignalMailbox()
	firstWait, _ := ParseWaitID("wait:first")
	secondWait, _ := ParseWaitID("wait:second")
	firstKey, _ := ParseWaitKey("first")
	secondKey, _ := ParseWaitKey("second")
	if err := mailbox.openWait(firstKey, mustMailboxSignal(t, "signal:first-opened", firstWait, json.RawMessage(`{}`)), false); err != nil {
		t.Fatal(err)
	}
	if err := mailbox.openWait(secondKey, mustMailboxSignal(t, "signal:second-opened", secondWait, json.RawMessage(`{}`)), false); err != nil {
		t.Fatal(err)
	}
	firstSignalID, _ := ParseSignalID("signal:first")
	secondSignalID, _ := ParseSignalID("signal:second")
	firstSignal, _ := newSignal(firstSignalID, firstWait, json.RawMessage(`{}`))
	secondSignal, _ := newSignal(secondSignalID, secondWait, json.RawMessage(`{}`))
	finalization := &preparedStepFinalization{
		process:               process,
		prepared:              &preparedStep{Effects: make([]preparedEffect, 2)},
		mailbox:               mailbox,
		immediateChildSignals: []Signal{firstSignal, secondSignal},
	}

	err := finalization.enqueueImmediateChildSignals()
	if !errors.Is(err, ErrResourceLimitExceeded) {
		t.Fatalf("enqueue immediate child Signals error = %v, want %v", err, ErrResourceLimitExceeded)
	}
	if pending := finalization.mailbox.pendingCount(); pending != 3 {
		t.Fatalf("pending immediate child Signals = %d, want 3 including both opening Signals before cumulative limit", pending)
	}
}

func TestPreparedCompletionDoesNotRetainOutputWhenKillWins(t *testing.T) {
	output, err := EncodeOutput(struct {
		Value string `json:"value"`
	}{Value: "superseded"})
	if err != nil {
		t.Fatal(err)
	}
	transition, err := Complete(0, output)
	if err != nil {
		t.Fatal(err)
	}
	kill, err := newKillIntent("operator requested kill")
	if err != nil {
		t.Fatal(err)
	}
	finalization := &preparedStepFinalization{
		process:  &processState{pendingControl: pendingControl{kill: kill}},
		prepared: &preparedStep{Transition: transition},
	}
	if err := finalization.prepareTransition(time.Now()); err != nil {
		t.Fatal(err)
	}
	if finalization.transition.status != StatusKilled {
		t.Fatalf("resolved status=%s, want %s", finalization.transition.status, StatusKilled)
	}
	if finalization.transition.finalOutput.Valid() {
		t.Fatal("superseded completion output survived Kill priority")
	}
}

func TestRejectedFinalizationReleasesEveryNewChildWait(t *testing.T) {
	runtime, parent := newChildCompletionTestProcess(t)
	childID := deriveChildProcessID(deriveEffectID(parent.handle.processID, 1, 0))
	childKey, _ := ParseChildKey("worker")
	handle := newProcessHandleState(
		childProcessRelation(childID, parent.handle.relation, childKey),
		parent.deployment.DeploymentRef(), parent.budget, parent.capabilities,
		parent.treeLimits, parent.startedAt, StatusRunning,
	)
	runtime.addProcess(newProcessState(handle, parent.deployment, parent.execution,
		parent.committedExecutionState, parent.startedAt, parent.limits))
	missingID, _ := ParseProcessID("process:missing-child")
	var specs []ChildWaitSpec
	var effects []Effect
	for index, id := range []ProcessID{childID, childID, missingID} {
		key, _ := ParseWaitKey(fmt.Sprintf("worker-result-%d", index))
		spec := ChildWaitSpec{Key: key, Children: []ProcessID{id},
			Boundary: ChildWaitBoundaryResult, Condition: AllChildren()}
		effect, err := WaitForChildren(spec)
		if err != nil {
			t.Fatal(err)
		}
		specs = append(specs, spec)
		effects = append(effects, effect)
	}
	transition, err := Continue(0, effects...)
	if err != nil {
		t.Fatal(err)
	}
	prepared := &preparedStep{Transition: transition}
	for index, effect := range effects {
		record := preparedEffect{
			ID: deriveEffectID(parent.handle.processID, 2, index), Effect: effect, Phase: effectPhasePending,
		}
		if err := record.settleFramework(); err != nil {
			t.Fatal(err)
		}
		prepared.Effects = append(prepared.Effects, record)
	}
	parent.prepared = prepared
	if err := runtime.finalizePrepared(parent); !errors.Is(err, ErrInvalidChildWait) {
		t.Fatalf("invalid child wait finalization error = %v", err)
	}
	if parent.prepared != prepared || parent.committedSteps != 0 || parent.mailbox.pendingCount() != 0 {
		t.Fatal("rejected finalization adopted candidate state")
	}
	for index, spec := range specs[:2] {
		waitID := *prepared.Effects[index].WaitID
		if _, _, err := runtime.registerChildWait(parent.handle.processID, waitID, spec); err != nil {
			t.Fatalf("rejected finalization retained registration %d: %v", index, err)
		}
		runtime.unregisterChildWait(parent.handle.processID, waitID)
	}
}
