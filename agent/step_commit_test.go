package agent

import (
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
	handle := newProcessHandle(
		rootProcessRelation(processID), Deployment{}, Digest{},
		Budget{Steps: NewQuota(maxUint64), Effects: NewQuota(maxUint64), Signals: NewQuota(maxUint64)},
		CapabilitySet{}, time.Now())
	process := &processState{handle: handle, committedSteps: maxUint64 - 1, mailbox: newSignalMailbox()}

	schedulingFailure := process.stepSchedulingFailure(resourceAmounts{Steps: 1, Effects: 1, Signals: 1})
	if schedulingFailure == nil {
		t.Fatal("step scheduling failure is nil")
	}
	runtime := &treeRuntime{engine: &Engine{}}
	runtime.failProcess(process, schedulingFailure.kind, schedulingFailure.code, schedulingFailure.cause)

	if process.status() != StatusFailed {
		t.Fatalf("status = %s, want %s", process.status(), StatusFailed)
	}
	failure, present := process.termination.Failure()
	if !present || failure.Code() != "engine.limit.steps" {
		t.Fatalf("failure = %+v, present = %t", failure, present)
	}
}

func TestPreparedStepFinalizationCountsEveryImmediateChildSignal(t *testing.T) {
	runtime := newWaitingSnapshotTree(t, 3)
	parent := runtime.members.get(runtime.rootID)
	parent.handle.budget.Signals = NewQuota(23)
	var effects []Effect
	for _, child := range runtime.members.ordered() {
		if child == parent {
			continue
		}
		child.installTermination(controlValue((terminationInputs{outcome: completedOutcome()}).resolve()),
			controlValue(EncodePayload(childTestOutput{})), child.handle.startedAt)
		child.mailbox.closeAllWaits()
		effects = append(effects, controlValue(NewChildWaitEffect(ChildWaitSpec{
			Key:      controlValue(ParseWaitKey(fmt.Sprintf("result-%d", len(effects)))),
			Boundary: ChildWaitBoundaryResult, Children: []ProcessID{child.handle.processID}, Condition: AllChildren(),
		})))
	}
	failure := prepareTestStep(parent, runtime.treeLimits, stepJobResult{
		transition: controlValue(Continue(0, effects...)), candidate: parent.execution, candidateState: parent.committedExecutionState,
	})
	if failure != nil {
		t.Fatalf("preparation: %+v", failure)
	}
	for range effects {
		runtime.advancePrepared(parent)
	}
	before := controlValue(runtime.captureTree())
	if failure := runtime.finalizePrepared(parent); failure == nil || !errors.Is(failure.cause, ErrResourceLimitExceeded) ||
		failure.kind != FailureKindExecution || failure.code != failureCodeEngineLimitChildWaitSignal {
		t.Fatalf("immediate child Signals = %+v, want %v", failure, ErrResourceLimitExceeded)
	}
	after := controlValue(runtime.captureTree())
	if before.Digest() != after.Digest() {
		t.Fatal("rejected completion batch changed the tree")
	}
}

func TestPreparedCompletionDoesNotRetainOutputWhenKillWins(t *testing.T) {
	output, err := EncodePayload(struct {
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
		prepared: &preparedStep{Intent: transition},
	}
	if err := finalization.prepareTransition(time.Now()); err != nil {
		t.Fatal(err)
	}
	if finalization.commit.termination.Status() != StatusKilled {
		t.Fatalf("resolved status=%s, want %s", finalization.commit.termination.Status(), StatusKilled)
	}
	if finalization.commit.finalOutput.Valid() {
		t.Fatal("superseded completion output survived Kill priority")
	}
}

func TestRejectedFinalizationReleasesEveryNewChildWait(t *testing.T) {
	runtime, parent := newChildCompletionTestProcess(t)
	childID := parent.handle.processID.effectID(1, 0).childProcessID()
	childKey, _ := ParseChildKey("worker")
	handle := newProcessHandle(
		childProcessRelation(childID, parent.handle.relation, childKey),
		parent.deployment(), Digest{}, parent.handle.budget, parent.handle.capabilities, parent.handle.startedAt)
	runtime.addProcess(newProcessState(handle, parent.execution,
		parent.committedExecutionState))
	missingID, _ := ParseProcessID("process:missing-child")
	var effects []Effect
	for index, id := range []ProcessID{childID, childID, missingID} {
		key, _ := ParseWaitKey(fmt.Sprintf("worker-result-%d", index))
		spec := ChildWaitSpec{Key: key, Children: []ProcessID{id},
			Boundary: ChildWaitBoundaryResult, Condition: AllChildren()}
		effect, err := NewChildWaitEffect(spec)
		if err != nil {
			t.Fatal(err)
		}
		effects = append(effects, effect)
	}
	transition, err := Continue(0, effects...)
	if err != nil {
		t.Fatal(err)
	}
	prepared := &preparedStep{Intent: transition}
	for index, effect := range effects {
		record := preparedEffect{
			ID: parent.handle.processID.effectID(2, index), Effect: effect, Phase: effectPhasePending,
		}
		if err := record.settleFramework(); err != nil {
			t.Fatal(err)
		}
		prepared.Effects = append(prepared.Effects, record)
	}
	parent.prepared = prepared
	if failure := runtime.finalizePrepared(parent); failure == nil || !errors.Is(failure.cause, ErrInvalidChildWait) ||
		failure.kind != FailureKindContract || failure.code != failureCodeEngineFinalizeInvalid {
		t.Fatalf("invalid child wait finalization = %+v", failure)
	}
	if parent.prepared != prepared || parent.committedSteps != 0 || parent.mailbox.pendingCount() != 0 {
		t.Fatal("rejected finalization adopted candidate state")
	}
	if len(parent.mailbox.openChildWaits()) != 0 {
		t.Fatal("rejected finalization opened child waits")
	}
}

func TestRejectedFinalizationPreservesExistingChildWait(t *testing.T) {
	runtime, parent := newChildCompletionTestProcess(t)
	childID := parent.handle.processID.effectID(1, 0).childProcessID()
	childKey := controlValue(ParseChildKey("worker"))
	handle := newProcessHandle(
		childProcessRelation(childID, parent.handle.relation, childKey),
		parent.deployment(), Digest{}, parent.handle.budget, parent.handle.capabilities, parent.handle.startedAt)
	runtime.addProcess(newProcessState(handle, parent.execution,
		parent.committedExecutionState))
	spec := ChildWaitSpec{Key: controlValue(ParseWaitKey("worker-result")), Children: []ProcessID{childID},
		Boundary: ChildWaitBoundaryResult, Condition: AllChildren()}
	record := preparedEffect{
		ID: parent.handle.processID.effectID(2, 0), Effect: controlValue(NewChildWaitEffect(spec)), Phase: effectPhasePending,
	}
	if err := record.settleFramework(); err != nil {
		t.Fatal(err)
	}
	parent.prepared = &preparedStep{Intent: controlValue(Continue(0, record.Effect)), Effects: preparedEffects{record}}
	waitID := record.ID.waitID()
	openTestChildWait(t, &parent.mailbox, "signal:engine:existing", waitID, spec)
	if failure := runtime.finalizePrepared(parent); failure == nil || !errors.Is(failure.cause, errWaitState) {
		t.Fatalf("duplicate child wait finalization = %+v", failure)
	}
	if opened := parent.mailbox.openChildWaits(); len(opened) != 1 || opened[0].waitID != waitID {
		t.Fatal("rejected finalization removed a wait it did not open")
	}
}
