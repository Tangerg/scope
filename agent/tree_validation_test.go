package agent

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"testing"
)

func TestDrainedTreeSnapshotOutcomeOrder(t *testing.T) {
	snapshot := drainedSnapshotFixture(t, 4)
	wait := snapshot.state.ProcessSnapshots[0].openChildWaits[0]
	root := snapshot.state.ProcessSnapshots[0].state
	record := root.Mailbox.Signals[len(root.Mailbox.Signals)-1]
	satisfied := controlValue(ParseChildWaitSatisfied(controlValue(NewSignal(record.ID, wait.waitID, record.Payload))))
	for _, test := range []struct {
		name   string
		change func([]ChildOutcome) []ChildOutcome
	}{
		{"reversed", func(outcomes []ChildOutcome) []ChildOutcome { slices.Reverse(outcomes); return outcomes }},
		{"duplicate", func(outcomes []ChildOutcome) []ChildOutcome { outcomes[1] = outcomes[0]; return outcomes }},
		{"foreign child", func(outcomes []ChildOutcome) []ChildOutcome {
			outcomes[0].result.processID = newProcessID()
			return outcomes
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := snapshot.state.clone()
			process := root
			process.Mailbox.Signals = slices.Clone(root.Mailbox.Signals)
			outcomes := test.change(slices.Clone(satisfied.outcomes))
			payload := childWaitSatisfiedWire{Operation: childWaitSignalSatisfied, Key: wait.spec.Key, Boundary: ChildWaitBoundaryDrained}
			for _, outcome := range outcomes {
				payload.Outcomes = append(payload.Outcomes, outcome.wire())
			}
			record.Payload = controlValue(normalizeJSON(controlValue(jsonv2.Marshal(payload)), MaxPayloadBytes))
			record.PayloadDigest = ComputeDigest(record.Payload)
			process.Mailbox.Signals[len(process.Mailbox.Signals)-1] = record
			candidate.ProcessSnapshots[0] = controlValue(newProcessSnapshot(process))
			encoded := controlValue(jsonv2.Marshal(candidate))
			if _, err := ParseTreeSnapshot(encoded); !errors.Is(err, ErrInvalidTreeSnapshot) {
				t.Fatalf("invalid outcomes accepted: %v", err)
			}
		})
	}
	parsed := controlValue(ParseTreeSnapshot(snapshot.JSON()))
	if parsed.Digest() != snapshot.Digest() {
		t.Fatal("canonical round trip changed snapshot")
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
			process.Relation = parent.wire()
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
	root.Mailbox.Signals[1].Payload, root.Mailbox.Signals[1].PayloadDigest = signal.Payload(), ComputeDigest(signal.Payload())
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

func TestTreeSnapshotKeepsWaitSignalsSeparated(t *testing.T) {
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
	if _, err := ParseTreeSnapshot(encoded); !errors.Is(err, ErrInvalidTreeSnapshot) {
		t.Fatalf("changed wait escaped its retained signals: %v", err)
	}
}

// reopenTestChildWait rewrites a captured opening as though it had announced spec.
func reopenTestChildWait(t testing.TB, record *signalRecordWire, spec ChildWaitSpec) {
	t.Helper()
	record.Opens = &waitOpeningWire{Spec: new(spec.wire())}
	record.PayloadDigest = controlValue(childWaitOpenedDigest(spec))
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
	record.Payload, record.PayloadDigest = signal.Payload(), ComputeDigest(signal.Payload())
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
