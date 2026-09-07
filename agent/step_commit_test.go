package agent

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestStepCannotConsumeBudgetReservedAtUint64Boundary(t *testing.T) {
	const maxUint64 = ^uint64(0)
	processID, err := ParseProcessID("process:resource-boundary")
	if err != nil {
		t.Fatal(err)
	}
	controller := newProcessController(
		rootProcessRelation(processID), DeploymentRef{},
		Budget{Steps: maxUint64, Effects: maxUint64, Signals: maxUint64},
		CapabilitySet{}, DefaultTreeLimits(), time.Now(), StatusRunning,
	)
	loop := &processState{
		engine:         &Engine{},
		controller:     controller,
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

	schedulingFailure := loop.stepSchedulingFailure()
	if schedulingFailure == nil {
		t.Fatal("step scheduling failure is nil")
	}
	loop.fail(schedulingFailure.kind, schedulingFailure.code, schedulingFailure.cause)

	if loop.status != StatusFailed {
		t.Fatalf("status = %s, want %s", loop.status, StatusFailed)
	}
	failure, present := loop.termination.Failure()
	if !present || failure.Code() != "engine.limit.steps" {
		t.Fatalf("failure = %+v, present = %t", failure, present)
	}
}

func TestPreparedStepFinalizationCountsEveryImmediateChildSignal(t *testing.T) {
	limits := Limits{
		MaxSteps: 10, MaxEffects: 10, MaxSignals: 3, MaxPendingSignals: 10,
	}
	loop := &processState{
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
		loop: loop,
		prepared: &preparedStep{wire: preparedStepWire{
			Effects: make([]preparedEffectWire, 2),
		}},
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
		loop:     &processState{pendingControl: pendingControl{kill: kill}},
		prepared: &preparedStep{wire: preparedStepWire{Transition: transition}},
	}
	if err := finalization.prepareTransition(); err != nil {
		t.Fatal(err)
	}
	if finalization.transition.status != StatusKilled {
		t.Fatalf("resolved status=%s, want %s", finalization.transition.status, StatusKilled)
	}
	if finalization.transition.finalOutput.Valid() {
		t.Fatal("superseded completion output survived Kill priority")
	}
}
