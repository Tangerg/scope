package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"
)

func TestDrainedTreeSnapshotOutcomeOrder(t *testing.T) {
	snapshot := drainedSnapshotFixture(t, 4)
	wait := snapshot.state.ChildWaits[0]
	root := snapshot.state.ProcessSnapshots[0].state
	record := root.Mailbox.Signals[len(root.Mailbox.Signals)-1]
	satisfied := controlValue(ParseChildWaitSatisfied(controlValue(newSignal(record.ID, wait.WaitID, record.Payload))))
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
			payload := childWaitSatisfiedWire{Operation: childSignalWaitSatisfied, Key: wait.Spec.Key, Boundary: ChildWaitBoundaryDrained}
			for _, outcome := range outcomes {
				payload.Outcomes = append(payload.Outcomes, outcome.wire())
			}
			record.Payload = controlValue(normalizeJSON(controlValue(json.Marshal(payload)), MaxPayloadBytes))
			record.PayloadDigest = ComputeDigest(record.Payload)
			process.Mailbox.Signals[len(process.Mailbox.Signals)-1] = record
			candidate.ProcessSnapshots[0] = controlValue(newProcessSnapshot(process))
			encoded := controlValue(json.Marshal(candidate))
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
	parent := wire.ProcessSnapshots[0].Relation()
	for index, snapshot := range wire.ProcessSnapshots {
		process := snapshot.state
		process.TreeLimits.MaxDepth = 16
		process.Limits.Budget = Budget{}
		process.AllocatedResources = resourceAmounts{}
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
	wait := &wire.ChildWaits[0]
	wait.Spec.Children = []ProcessID{child.ProcessID}
	spec := controlValue(wait.Spec.value())
	root := wire.ProcessSnapshots[0].state
	root.Mailbox.Signals = slices.Clone(root.Mailbox.Signals)
	opening := controlValue(normalizeJSON(controlValue(encodeChildWaitOpened(spec)), MaxPayloadBytes))
	root.Mailbox.Signals[0].PayloadDigest = ComputeDigest(opening)
	signal := controlValue(encodeChildWaitSatisfied(wait.WaitID, spec.Key, spec.Boundary, []ChildOutcome{{key: key, result: result, boundary: spec.Boundary}}))
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
	leaf.Status, leaf.PauseReason = StatusPaused, "unfinished descendant"
	leaf.Termination, leaf.FinishedAt, leaf.Output = nil, nil, Payload{}
	wire.ProcessSnapshots[index] = controlValue(newProcessSnapshot(leaf))
	if _, err := ParseTreeSnapshot(controlValue(json.Marshal(wire))); !errors.Is(err, ErrInvalidTreeSnapshot) {
		t.Fatalf("drained outcome accepted active descendant: %v", err)
	}
}

func retainedWaitsSnapshotFixture(t testing.TB, count int) TreeSnapshot {
	t.Helper()
	wire := drainedSnapshotFixture(t, 2).state.clone()
	root := wire.ProcessSnapshots[0].state
	original := wire.ChildWaits[0]
	originalSignal := root.Mailbox.Signals[1]
	outcomes := controlValue(ParseChildWaitSatisfied(controlValue(newSignal(originalSignal.ID, original.WaitID, originalSignal.Payload)))).Outcomes()
	root.Limits.Budget, root.AllocatedResources = Budget{}, resourceAmounts{}
	mailbox := newSignalMailbox()
	wire.ChildWaits = nil
	for index := range count {
		signal := mustMailboxSignal(t, fmt.Sprintf("signal:history-%d", index), WaitID{}, []byte(`{}`))
		if _, err := mailbox.enqueue(StatusPaused, signal, signalSourceExternal); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := mailbox.commit(uint32(count)); err != nil {
		t.Fatal(err)
	}
	for index := range count {
		waitID := controlValue(ParseWaitID(fmt.Sprintf("wait:retained-%d", index)))
		spec := controlValue(original.Spec.value())
		spec.Key = controlValue(ParseWaitKey(fmt.Sprintf("retained-%d", index)))
		opening := mustMailboxSignal(t, fmt.Sprintf("signal:engine:retained-%d", index), waitID, controlValue(encodeChildWaitOpened(spec)))
		if err := mailbox.openWait(spec.Key, opening, WaitKindChildren); err != nil {
			t.Fatal(err)
		}
		wire.ChildWaits = append(wire.ChildWaits, childWaitSnapshotWire{ParentProcessID: root.ProcessID, WaitID: waitID, Spec: spec.wire()})
	}
	if _, err := mailbox.commit(uint32(count)); err != nil {
		t.Fatal(err)
	}
	for _, wait := range wire.ChildWaits {
		signal := controlValue(encodeChildWaitSatisfied(wait.WaitID, wait.Spec.Key, wait.Spec.Boundary, outcomes))
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
	wire := snapshot.state.clone()
	wire.ChildWaits[1].Spec.Boundary = ChildWaitBoundaryResult
	if _, err := ParseTreeSnapshot(controlValue(json.Marshal(wire))); !errors.Is(err, ErrInvalidTreeSnapshot) {
		t.Fatalf("changed wait escaped its retained signals: %v", err)
	}
}

func TestDrainedSnapshotAcceptsOrderedQuorumSubset(t *testing.T) {
	wire := drainedSnapshotFixture(t, 4).state.clone()
	root := wire.ProcessSnapshots[0].state
	wait := &wire.ChildWaits[0]
	spec := controlValue(wait.Spec.value())
	spec.Condition = controlValue(ChildQuorum(2))
	wait.Spec = spec.wire()
	root.Mailbox.Signals = slices.Clone(root.Mailbox.Signals)
	opening := controlValue(normalizeJSON(controlValue(encodeChildWaitOpened(spec)), MaxPayloadBytes))
	root.Mailbox.Signals[0].PayloadDigest = ComputeDigest(opening)
	record := &root.Mailbox.Signals[1]
	satisfied := controlValue(ParseChildWaitSatisfied(controlValue(newSignal(record.ID, wait.WaitID, record.Payload))))
	signal := controlValue(encodeChildWaitSatisfied(wait.WaitID, spec.Key, spec.Boundary, []ChildOutcome{satisfied.outcomes[1], satisfied.outcomes[3]}))
	record.Payload, record.PayloadDigest = signal.Payload(), ComputeDigest(signal.Payload())
	wire.ProcessSnapshots[0] = controlValue(newProcessSnapshot(root))
	if _, err := ParseTreeSnapshot(controlValue(json.Marshal(wire))); err != nil {
		t.Fatalf("ordered quorum subset rejected: %v", err)
	}
}

func TestTreeSnapshotRejectsDepthBeyondCapturedLimit(t *testing.T) {
	snapshot := deepDrainedSnapshotFixture(t)
	if _, err := ParseTreeSnapshot(snapshot.JSON()); err != nil {
		t.Fatal(err)
	}
	wire := snapshot.state.clone()
	for i := range wire.ProcessSnapshots {
		wire.ProcessSnapshots[i].state.TreeLimits.MaxDepth = 15
		wire.ProcessSnapshots[i].data = controlValue(json.Marshal(wire.ProcessSnapshots[i].state))
	}
	if _, err := ParseTreeSnapshot(controlValue(json.Marshal(wire))); !errors.Is(err, ErrInvalidTreeSnapshot) {
		t.Fatalf("over-depth tree accepted: %v", err)
	}
	leaf := wire.ProcessSnapshots[len(wire.ProcessSnapshots)-1]
	if _, err := ParseProcessSnapshot(leaf.JSON()); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("over-depth process accepted: %v", err)
	}
}

func TestPreparedChildWaitRecoveryRequiresDirectChildren(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		runtime := newWaitingSnapshotTree(t, 2)
		root := runtime.processes[runtime.rootID]
		child := runtime.childrenByParent[runtime.rootID][0]
		if invalid {
			child = newProcessID()
		}
		effect := controlValue(NewChildWaitEffect(ChildWaitSpec{Key: controlValue(ParseWaitKey("children")), Children: []ProcessID{child}, Boundary: ChildWaitBoundaryDrained, Condition: AllChildren()}))
		transition := controlValue(Continue(0, effect))
		if failure := prepareTestStep(root, stepJobResult{transition: transition, candidateState: root.committedExecutionState}); failure != nil {
			t.Fatal(failure.cause)
		}
		_, err := runtime.captureTree()
		if invalid && !errors.Is(err, ErrInvalidTreeSnapshot) || !invalid && err != nil {
			t.Fatalf("invalid=%t error=%v", invalid, err)
		}
	}
}
