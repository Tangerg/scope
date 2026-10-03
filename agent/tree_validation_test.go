package agent

import (
	"bytes"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"slices"
	"testing"
)

func TestDrainedTreeSnapshotRendersAnswersFromTheirChildren(t *testing.T) {
	snapshot := drainedSnapshotFixture(t, 4)
	root := snapshot.state.ProcessSnapshots[0]
	last := len(root.state.Mailbox.Signals) - 1
	for _, test := range []struct {
		name   string
		change func([]signalRecordDocument)
	}{
		{"reversed", func(records []signalRecordDocument) { slices.Reverse(records[last].Answered) }},
		{"duplicate", func(records []signalRecordDocument) { records[last].Answered[1] = records[last].Answered[0] }},
		{"foreign child", func(records []signalRecordDocument) { records[last].Answered[0] = newProcessID() }},
		{"missing children", func(records []signalRecordDocument) { records[last].Answered = nil }},
		{"stored payload", func(records []signalRecordDocument) { records[last].Payload = root.state.Mailbox.Signals[last].Payload }},
		{"stored identity", func(records []signalRecordDocument) {
			records[last].ID = new(records[last].WaitID.childWaitSignalID())
		}},
		{"unnamed opening", func(records []signalRecordDocument) { records[0].ID = nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			encoded := treeJSONWithDocument(t, snapshot, root.ProcessID(), func(document *processSnapshotDocument) {
				test.change(document.Mailbox.Signals)
			})
			if _, err := ParseTreeSnapshot(encoded); !errors.Is(err, ErrInvalidTreeSnapshot) {
				t.Fatalf("invalid answer accepted: %v", err)
			}
		})
	}
	parsed := controlValue(ParseTreeSnapshot(snapshot.JSON()))
	if parsed.Digest() != snapshot.Digest() {
		t.Fatal("canonical round trip changed snapshot")
	}
	rendered := parsed.ProcessSnapshots()[0].state.Mailbox.Signals[last].Payload
	if !bytes.Equal(rendered, root.state.Mailbox.Signals[last].Payload) {
		t.Fatal("decoded answer differs from the admitted answer")
	}
}

func deepDrainedSnapshotFixture(t testing.TB) TreeSnapshot {
	t.Helper()
	wire := drainedSnapshotFixture(t, 16).state.clone()
	wire.TreeLimits.MaxDepth = 16
	parent := wire.ProcessSnapshots[0].Relation()
	for index, snapshot := range wire.ProcessSnapshots {
		process := snapshot.state
		process.Budget = Budget{}
		if index > 0 {
			key, _ := snapshot.Relation().ChildKey()
			parent = childProcessRelation(snapshot.ProcessID(), parent, key)
			process.Relation = parent
		}
		wire.ProcessSnapshots[index] = controlValue(newProcessSnapshot(process))
	}
	child := wire.ProcessSnapshots[1].state
	key, _ := wire.ProcessSnapshots[1].Relation().ChildKey()
	result := controlValue((resultWire{ProcessID: child.ProcessID, StartedAt: child.StartedAt, FinishedAt: *child.FinishedAt, Output: child.Output, Termination: *child.Termination, Usage: child.usage()}).value())
	wait := wire.ProcessSnapshots[0].openChildWaits[0]
	spec := wait.spec.clone()
	spec.Children = []ProcessID{child.ProcessID}
	root := wire.ProcessSnapshots[0].state
	root.Mailbox.Signals = slices.Clone(root.Mailbox.Signals)
	reopenTestChildWait(t, &root.Mailbox.Signals[0], spec)
	signal := controlValue(encodeChildWaitSatisfied(wait.waitID, spec.Key, spec.Boundary, []ChildOutcome{{key: key, result: result, boundary: spec.Boundary}}))
	root.Mailbox.Signals[1].Payload = signal.Payload()
	wire.ProcessSnapshots[0] = controlValue(newProcessSnapshot(root))
	return controlValue(newTreeSnapshot(wire))
}

func TestDrainedSnapshotRejectsActiveDeepDescendant(t *testing.T) {
	snapshot := deepDrainedSnapshotFixture(t)
	if _, err := ParseTreeSnapshot(snapshot.JSON()); err != nil {
		t.Fatal(err)
	}
	wire := snapshot.state.clone()
	index := len(wire.ProcessSnapshots) - 1
	leaf := wire.ProcessSnapshots[index].state
	leaf.PauseReason = "unfinished descendant"
	leaf.Termination, leaf.FinishedAt, leaf.Output = nil, nil, Payload{}
	wire.ProcessSnapshots[index] = controlValue(newProcessSnapshot(leaf))
	if _, err := ParseTreeSnapshot(controlValue(jsonv2.Marshal(wire))); !errors.Is(err, ErrInvalidTreeSnapshot) {
		t.Fatalf("drained outcome accepted active descendant: %v", err)
	}
}

func retainedWaitsSnapshotFixture(t testing.TB, count int) TreeSnapshot {
	t.Helper()
	wire := drainedSnapshotFixture(t, 2).state.clone()
	root := wire.ProcessSnapshots[0].state
	original := wire.ProcessSnapshots[0].openChildWaits[0]
	originalSignal := root.Mailbox.Signals[1]
	outcomes := controlValue(ParseChildWaitSatisfied(controlValue(NewSignal(originalSignal.ID, original.waitID, originalSignal.Payload)))).Outcomes()
	root.Budget = Budget{}
	mailbox := newSignalMailbox()
	for index := range count {
		signal := mustMailboxSignal(t, fmt.Sprintf("signal:history-%d", index), WaitID{}, []byte(`{}`))
		if _, err := mailbox.enqueue(StatusPaused, signal, signalSourceExternal); err != nil {
			t.Fatal(err)
		}
	}
	if err := mailbox.commit(uint32(count)); err != nil {
		t.Fatal(err)
	}
	var retained []ChildWaitOpened
	for index := range count {
		waitID := controlValue(ParseWaitID(fmt.Sprintf("wait:retained-%d", index)))
		spec := original.spec.clone()
		spec.Key = controlValue(ParseWaitKey(fmt.Sprintf("retained-%d", index)))
		openTestChildWait(t, &mailbox, fmt.Sprintf("signal:engine:retained-%d", index), waitID, spec)
		retained = append(retained, ChildWaitOpened{waitID: waitID, spec: spec})
	}
	if err := mailbox.commit(uint32(count)); err != nil {
		t.Fatal(err)
	}
	for _, wait := range retained {
		signal := controlValue(encodeChildWaitSatisfied(wait.waitID, wait.spec.Key, wait.spec.Boundary, outcomes))
		if _, err := mailbox.enqueue(StatusPaused, signal, signalSourceChildWait); err != nil {
			t.Fatal(err)
		}
	}
	root.Mailbox = mailbox.wire()
	wire.ProcessSnapshots[0] = controlValue(newProcessSnapshot(root))
	return controlValue(newTreeSnapshot(wire))
}

func TestTreeSnapshotRendersEachAnswerFromItsOwnWait(t *testing.T) {
	snapshot := retainedWaitsSnapshotFixture(t, 3)
	if _, err := ParseTreeSnapshot(snapshot.JSON()); err != nil {
		t.Fatal(err)
	}
	root := snapshot.state.ProcessSnapshots[0]
	encoded := treeJSONWithProcess(t, snapshot, root.ProcessID(), func(wire *processSnapshotWire) {
		for index, record := range wire.Mailbox.Signals {
			if record.Opens != nil && record.Opens.Spec != nil && record.Opens.Spec.Key.String() == "retained-1" {
				spec := controlValue(record.Opens.Spec.value())
				spec.Boundary = ChildWaitBoundaryResult
				reopenTestChildWait(t, &wire.Mailbox.Signals[index], spec)
			}
		}
	})
	parsed := controlValue(ParseTreeSnapshot(encoded))
	boundaries := make(map[string]ChildWaitBoundary)
	for _, receipt := range parsed.ProcessSnapshots()[0].SignalReceipts() {
		if pending, ok := receipt.PendingSignal(); ok {
			satisfied := controlValue(ParseChildWaitSatisfied(pending))
			boundaries[satisfied.Key().String()] = satisfied.Boundary()
		}
	}
	want := map[string]ChildWaitBoundary{
		"retained-0": ChildWaitBoundaryDrained, "retained-1": ChildWaitBoundaryResult, "retained-2": ChildWaitBoundaryDrained,
	}
	if !maps.Equal(boundaries, want) {
		t.Fatalf("answer boundaries = %v, want %v", boundaries, want)
	}
}

// reopenTestChildWait rewrites a captured opening as though it had announced spec.
func reopenTestChildWait(t testing.TB, record *signalRecordWire, spec ChildWaitSpec) {
	t.Helper()
	record.Opens = &waitOpeningWire{Spec: new(spec.wire())}
}

func TestDrainedSnapshotAcceptsOrderedQuorumSubset(t *testing.T) {
	wire := drainedSnapshotFixture(t, 4).state.clone()
	root := wire.ProcessSnapshots[0].state
	wait := wire.ProcessSnapshots[0].openChildWaits[0]
	spec := wait.spec.clone()
	spec.Condition = controlValue(ChildQuorum(2))
	root.Mailbox.Signals = slices.Clone(root.Mailbox.Signals)
	reopenTestChildWait(t, &root.Mailbox.Signals[0], spec)
	record := &root.Mailbox.Signals[1]
	satisfied := controlValue(ParseChildWaitSatisfied(controlValue(NewSignal(record.ID, wait.waitID, record.Payload))))
	signal := controlValue(encodeChildWaitSatisfied(wait.waitID, spec.Key, spec.Boundary, []ChildOutcome{satisfied.outcomes[1], satisfied.outcomes[3]}))
	record.Payload = signal.Payload()
	wire.ProcessSnapshots[0] = controlValue(newProcessSnapshot(root))
	if _, err := ParseTreeSnapshot(controlValue(jsonv2.Marshal(wire))); err != nil {
		t.Fatalf("ordered quorum subset rejected: %v", err)
	}
}

func TestTreeSnapshotRejectsDepthBeyondCapturedLimit(t *testing.T) {
	snapshot := deepDrainedSnapshotFixture(t)
	if _, err := ParseTreeSnapshot(snapshot.JSON()); err != nil {
		t.Fatal(err)
	}
	wire := snapshot.state.clone()
	wire.TreeLimits.MaxDepth = 15
	if _, err := ParseTreeSnapshot(controlValue(wire.encode())); !errors.Is(err, ErrInvalidTreeSnapshot) {
		t.Fatalf("over-depth tree accepted: %v", err)
	}
}

func TestPreparedChildWaitRecoveryRequiresDirectChildren(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		runtime := newWaitingSnapshotTree(t, 2)
		root := runtime.members.get(runtime.rootID)
		child := runtime.members.childrenOf(runtime.rootID)[0]
		if invalid {
			child = newProcessID()
		}
		effect := controlValue(NewChildWaitEffect(ChildWaitSpec{Key: controlValue(ParseWaitKey("children")), Children: []ProcessID{child}, Boundary: ChildWaitBoundaryDrained, Condition: AllChildren()}))
		transition := controlValue(Continue(0, effect))
		if failure := prepareTestStep(root, runtime.treeLimits, stepJobResult{transition: transition, candidateState: root.committedExecutionState}); failure != nil {
			t.Fatal(failure.cause)
		}
		_, err := runtime.captureTree()
		if invalid && !errors.Is(err, ErrInvalidTreeSnapshot) || !invalid && err != nil {
			t.Fatalf("invalid=%t error=%v", invalid, err)
		}
	}
}
