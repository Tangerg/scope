package agent

import (
	"bytes"
	"errors"
	"testing"
)

func TestRepeatedCaptureTracksControlSignalsAndReservations(t *testing.T) {
	runtime := newWaitingSnapshotTree(t, 2)
	process := runtime.processes[runtime.rootID]
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
		parsed, err := ParseProcessSnapshot(snapshot.JSON())
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
	if wire := captureChange(); wire.Status != StatusPaused || wire.PauseReason != "operator pause" {
		t.Fatal("pause is absent")
	}
	if err := process.resume(); err != nil {
		t.Fatal(err)
	}
	if wire := captureChange(); wire.Status != StatusRunning || wire.PauseReason != "" {
		t.Fatal("resume is absent")
	}
	signal := mustMailboxSignal(t, "signal:capture", WaitID{}, []byte(`{"value":"new"}`))
	if accepted, err := process.admitSignals([]Signal{signal}, signalSourceExternal); err != nil || !accepted {
		t.Fatalf("signal admission = %t, %v", accepted, err)
	}
	if wire := captureChange(); wire.Usage.AcceptedSignals != 1 || len(wire.Mailbox.Signals) != 1 {
		t.Fatal("accepted signal is absent")
	}
	budget := Budget{Steps: 1, Effects: 1, Signals: 1}
	if !process.reserveProvisionalChildBudget(budget) {
		t.Fatal("budget not reserved")
	}
	if err := process.commitProvisionalChildBudget(budget); err != nil {
		t.Fatal(err)
	}
	if wire := captureChange(); wire.ReservedBudget.Steps != 11 {
		t.Fatal("committed budget is absent")
	}
	process.releaseCommittedChildBudget(budget)
	if wire := captureChange(); wire.ReservedBudget.Steps != 10 {
		t.Fatal("released budget is retained")
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
	root := runtime.processes[runtime.rootID]
	acknowledged, err := root.capture()
	if err != nil {
		t.Fatal(err)
	}
	wire, err := acknowledged.wire()
	if err != nil {
		t.Fatal(err)
	}
	wire.ReservedBudget = Budget{}
	acknowledged, err = newProcessSnapshot(wire)
	if err != nil {
		t.Fatal(err)
	}
	head, err := newTreeSnapshot(treeSnapshotWire{RootID: runtime.rootID, ProcessSnapshots: []ProcessSnapshot{acknowledged}})
	if err != nil {
		t.Fatal(err)
	}
	runtime.head = &treeHead{snapshot: head, advanced: make(chan struct{})}
	cause := errors.New("child checkpoint was not acknowledged")
	runtime.failDurability(cause, ProcessID{}, EffectID{})
	if len(runtime.processes) != 1 || runtime.processes[runtime.rootID] != root {
		t.Fatal("prospective child retained a published lifecycle")
	}
	_, err = root.handle.outcome()
	failure, ok := errors.AsType[*RuntimeError](err)
	if !ok || !errors.Is(failure, cause) || failure.HeadDigest() != head.Digest() {
		t.Fatalf("runtime failure = %v", err)
	}
	runtime.failDurability(cause, ProcessID{}, EffectID{})
}

func TestRepeatedCaptureTracksEffectSettlement(t *testing.T) {
	_, process := newChildCompletionTestProcess(t)
	effect, err := NewDispatcherEffect([]byte(`{"operation":"capture"}`))
	if err != nil {
		t.Fatal(err)
	}
	transition, err := Continue(0, effect)
	if err != nil {
		t.Fatal(err)
	}
	if failure := process.prepareStepResult(stepJobResult{
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
	if settleErr := record.settle(settlement); settleErr != nil {
		t.Fatal(settleErr)
	}
	settled, err := process.capture()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(pending.JSON(), settled.JSON()) {
		t.Fatal("capture retained the pending Effect after settlement")
	}
	parsed, err := ParseProcessSnapshot(settled.JSON())
	if err != nil {
		t.Fatal(err)
	}
	wire, err := parsed.wire()
	if err != nil {
		t.Fatal(err)
	}
	if got := wire.Prepared.Effects[0].Settlement; got == nil || got.Status() != SettlementStatusSucceeded ||
		!bytes.Equal(got.Payload(), settlement.Payload()) {
		t.Fatalf("settlement evidence changed: %+v", got)
	}
	if wire, err := pending.wire(); err != nil || wire.Prepared.Effects[0].Settlement != nil {
		t.Fatalf("settlement mutated the earlier capture: %v", err)
	}
}
