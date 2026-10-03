package agent

import (
	"bytes"
	"errors"
	"testing"
)

func TestRepeatedCaptureTracksControlSignalsAndReservations(t *testing.T) {
	runtime := newWaitingSnapshotTree(t, 2)
	process := runtime.members.get(runtime.rootID)
	before, err := process.capture()
	if err != nil {
		t.Fatal(err)
	}
	previous := before
	captureChange := func() processSnapshotWire {
		t.Helper()
		snapshot, err := process.capture()
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(previous.JSON(), snapshot.JSON()) {
			t.Fatal("capture retained superseded protocol state")
		}
		parsed, err := parseTestProcessSnapshot(snapshot.JSON())
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(parsed.JSON(), snapshot.JSON()) {
			t.Fatal("capture differs from strict parsing")
		}
		previous = snapshot
		wire, err := snapshot.wire()
		if err != nil {
			t.Fatal(err)
		}
		return wire
	}
	if err := process.requestPause("operator pause"); err != nil {
		t.Fatal(err)
	}
	if wire := captureChange(); wire.PendingControl.PauseReason != "operator pause" {
		t.Fatal("pending pause is absent")
	}
	if !process.applyPendingPause() {
		t.Fatal("pause not applied")
	}
	if wire := captureChange(); wire.status() != StatusPaused || wire.PauseReason != "operator pause" {
		t.Fatal("pause is absent")
	}
	if err := process.resume(); err != nil {
		t.Fatal(err)
	}
	if wire := captureChange(); wire.status() != StatusRunning || wire.PauseReason != "" {
		t.Fatal("resume is absent")
	}
	signal := mustMailboxSignal(t, "signal:capture", WaitID{}, []byte(`{"value":"new"}`))
	if accepted, err := admitTestSignals(process, runtime.treeLimits, []Signal{signal}, signalSourceExternal); err != nil || !accepted {
		t.Fatalf("signal admission = %t, %v", accepted, err)
	}
	if wire := captureChange(); wire.usage().AcceptedSignals != 1 || len(wire.Mailbox.Signals) != 1 {
		t.Fatal("accepted signal is absent")
	}
	if before.Status() != StatusRunning || len(before.SignalReceipts()) != 0 {
		t.Fatal("later changes mutated an earlier immutable capture")
	}
	process.committedExecutionState = ExecutionState{}
	if _, err := process.capture(); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("invalid state bypassed capture validation: %v", err)
	}
}

func TestDurabilityFailureDiscardsOnlyUnacknowledgedChildren(t *testing.T) {
	runtime := newWaitingSnapshotTree(t, 2)
	runtime.writer.committer = &recordingTreeCommitter{}
	root := runtime.members.get(runtime.rootID)
	acknowledged, err := root.capture()
	if err != nil {
		t.Fatal(err)
	}
	wire, err := acknowledged.wire()
	if err != nil {
		t.Fatal(err)
	}
	acknowledged, err = newProcessSnapshot(wire)
	if err != nil {
		t.Fatal(err)
	}
	incarnation := newTreeIncarnationID()
	head, err := newTreeSnapshot(treeSnapshotWire{TreeLimits: runtime.treeLimits, IncarnationID: incarnation, ProcessSnapshots: []ProcessSnapshot{acknowledged}})
	if err != nil {
		t.Fatal(err)
	}
	runtime.writer.identity = incarnation
	runtime.writer.establish(head)
	cause := errors.New("child checkpoint was not acknowledged")
	runtime.failRuntime(cause, ProcessID{}, EffectID{})
	if runtime.members.len() != 1 || runtime.members.get(runtime.rootID) != root {
		t.Fatal("prospective child retained a published lifecycle")
	}
	_, err = root.handle.outcome()
	failure, ok := errors.AsType[*RuntimeError](err)
	if !ok || !errors.Is(failure, cause) || failure.HeadDigest() != head.Digest() {
		t.Fatalf("runtime failure = %v", err)
	}
	runtime.failRuntime(cause, ProcessID{}, EffectID{})
}

func TestRepeatedCaptureTracksEffectSettlement(t *testing.T) {
	runtime, process := newChildCompletionTestProcess(t)
	effect, err := NewDispatcherEffect([]byte(`{"operation":"capture"}`))
	if err != nil {
		t.Fatal(err)
	}
	transition, err := Continue(0, effect)
	if err != nil {
		t.Fatal(err)
	}
	if failure := prepareTestStep(process, runtime.treeLimits, stepJobResult{
		transition: transition, candidate: process.execution, candidateState: process.committedExecutionState,
	}); failure != nil {
		t.Fatal(failure.cause)
	}
	record := &process.prepared.Effects[0]
	if beginErr := record.begin(); beginErr != nil {
		t.Fatal(beginErr)
	}
	pending, err := process.capture()
	if err != nil {
		t.Fatal(err)
	}
	settlement, err := NewSettlement(record.ID, SettlementStatusSucceeded, []byte(`{"result":"retained"}`))
	if err != nil {
		t.Fatal(err)
	}
	if settleErr := record.settle(settlement, nil); settleErr != nil {
		t.Fatal(settleErr)
	}
	settled, err := process.capture()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(pending.JSON(), settled.JSON()) {
		t.Fatal("capture retained the pending Effect after settlement")
	}
	parsed, err := parseTestProcessSnapshot(settled.JSON())
	if err != nil {
		t.Fatal(err)
	}
	wire, err := parsed.wire()
	if err != nil {
		t.Fatal(err)
	}
	if got := wire.Prepared.Effects[0].settlement(); got == nil || got.Status() != SettlementStatusSucceeded ||
		!bytes.Equal(got.Payload(), settlement.Payload()) {
		t.Fatalf("settlement evidence changed: %+v", got)
	}
	if wire, err := pending.wire(); err != nil || wire.Prepared.Effects[0].settlement() != nil {
		t.Fatalf("settlement mutated the earlier capture: %v", err)
	}
}

func TestChildDebitUnderflowPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("child debit underflow was silently accepted")
		}
	}()
	resourceAmounts{Steps: 3, Effects: 2, Signals: 1}.subtract(resourceAmounts{Steps: 1, Effects: 3, Signals: 1})
}

// memberChildAllocation reads the debits tree membership attributes to a
// Process, which holds none before it joins a tree.
func memberChildAllocation(process *processState) resourceAmounts {
	if runtime := process.handle.runtime.Load(); runtime != nil {
		return runtime.members.childAllocation(process.handle.processID)
	}
	return resourceAmounts{}
}

func prepareTestStep(process *processState, limits TreeLimits, result stepJobResult) *stepFailure {
	candidate, failure := process.prepareStep(result, limits, memberChildAllocation(process))
	if failure == nil {
		process.adoptCandidate(candidate)
	}
	return failure
}

func admitTestSignals(process *processState, limits TreeLimits, signals []Signal, source signalSource) (bool, error) {
	candidate, err := process.prepareSignals(signals, source, limits, memberChildAllocation(process))
	if err != nil || candidate == nil {
		return false, err
	}
	process.adoptCandidate(candidate)
	return true, nil
}

func TestPreparedCandidatesDoNotMutateTheirSource(t *testing.T) {
	runtime, process := newChildCompletionTestProcess(t)
	before := controlValue(process.capture())
	signal := mustMailboxSignal(t, "signal:candidate", WaitID{}, []byte(`{}`))
	candidate, err := process.prepareSignals([]Signal{signal}, signalSourceExternal, runtime.treeLimits, resourceAmounts{})
	if err != nil || candidate == nil {
		t.Fatalf("candidate: %v", err)
	}
	if after := controlValue(process.capture()); !bytes.Equal(before.JSON(), after.JSON()) {
		t.Fatal("unadopted signals changed the source Process")
	}
	process.adoptCandidate(candidate)
	if process.mailbox.pendingCount() != 1 {
		t.Fatal("candidate adoption lost the signal")
	}
	transition := controlValue(Continue(0, controlValue(NewDispatcherEffect([]byte(`{}`)))))
	step, failure := process.prepareStep(stepJobResult{transition: transition, candidateState: process.committedExecutionState}, runtime.treeLimits, resourceAmounts{})
	if failure != nil {
		t.Fatal(failure.cause)
	}
	if process.prepared != nil || process.counters.PreparedEffects != 0 {
		t.Fatal("unadopted Step changed execution state")
	}
	process.adoptCandidate(step)
	other := process.candidate()
	if err := other.prepared.Effects[0].begin(); err != nil {
		t.Fatal(err)
	}
	if err := other.mailbox.commit(1); err != nil {
		t.Fatal(err)
	}
	if process.prepared.Effects[0].phase() != effectPhasePlanned || process.mailbox.pendingCount() != 1 {
		t.Fatal("candidate shared mutable protocol state with its source")
	}
}
