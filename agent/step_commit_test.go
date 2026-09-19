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
		rootProcessRelation(processID), DeploymentRef{},
		Budget{Steps: NewQuota(maxUint64), Effects: NewQuota(maxUint64), Signals: NewQuota(maxUint64)},
		CapabilitySet{}, DefaultTreeLimits(), time.Now())
	process := &processState{
		handle:             handle,
		status:             StatusRunning,
		committedSteps:     maxUint64 - 1,
		allocatedResources: resourceAmounts{Steps: 1, Effects: 1, Signals: 1},
		mailbox:            newSignalMailbox(),
		limits:             Limits{MaxPendingSignals: maxUint64, Budget: Budget{Steps: NewQuota(maxUint64), Effects: NewQuota(maxUint64), Signals: NewQuota(maxUint64)}},
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
	runtime := newWaitingSnapshotTree(t, 3)
	parent := runtime.processes[runtime.rootID]
	parent.limits.Budget.Signals = NewQuota(23)
	var effects []Effect
	for _, child := range orderedProcesses(runtime.processes) {
		if child == parent {
			continue
		}
		child.installTermination(controlValue((terminationFacts{outcome: completedOutcome()}).resolve()),
			controlValue(EncodePayload(childTestOutput{})), child.startedAt)
		child.mailbox.closeAllWaits()
		effects = append(effects, controlValue(NewChildWaitEffect(ChildWaitSpec{
			Key:      controlValue(ParseWaitKey(fmt.Sprintf("result-%d", len(effects)))),
			Boundary: ChildWaitBoundaryResult, Children: []ProcessID{child.handle.processID}, Condition: AllChildren(),
		})))
	}
	failure := prepareTestStep(parent, stepJobResult{
		transition: controlValue(Continue(0, effects...)), candidate: parent.execution, candidateState: parent.committedExecutionState,
	})
	if failure != nil {
		t.Fatalf("preparation: %+v", failure)
	}
	for range effects {
		runtime.advancePrepared(parent)
	}
	before := controlValue(runtime.captureTree())
	if err := runtime.finalizePrepared(parent); !errors.Is(err, ErrResourceLimitExceeded) {
		t.Fatalf("immediate child Signals = %v, want %v", err, ErrResourceLimitExceeded)
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
	if finalization.commit.status != StatusKilled {
		t.Fatalf("resolved status=%s, want %s", finalization.commit.status, StatusKilled)
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
		parent.deployment.DeploymentRef(), parent.limits.Budget, parent.capabilities,
		parent.treeLimits, parent.startedAt)
	runtime.addProcess(newProcessState(handle, parent.deployment, parent.execution,
		parent.committedExecutionState, parent.startedAt, parent.limits))
	missingID, _ := ParseProcessID("process:missing-child")
	var specs []ChildWaitSpec
	var effects []Effect
	for index, id := range []ProcessID{childID, childID, missingID} {
		key, _ := ParseWaitKey(fmt.Sprintf("worker-result-%d", index))
		spec := ChildWaitSpec{Key: key, Children: []ProcessID{id},
			Boundary: ChildWaitBoundaryResult, Condition: AllChildren()}
		effect, err := NewChildWaitEffect(spec)
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
