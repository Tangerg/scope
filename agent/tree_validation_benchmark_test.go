package agent

import (
	"fmt"
	"strings"
	"testing"
)

func drainedSnapshotFixture(t testing.TB, count int) TreeSnapshot {
	t.Helper()
	runtime := newWaitingSnapshotTree(t, count+1)
	root := runtime.processes[runtime.rootID]
	children := runtime.childrenByParent[runtime.rootID]
	outcomes := make([]ChildOutcome, 0, len(children))
	termination := controlValue((terminationFacts{outcome: completedOutcome()}).resolve())
	output := controlValue(EncodePayload(childTestOutput{}))
	for _, id := range children {
		child := runtime.processes[id]
		child.mailbox = newSignalMailbox()
		child.installTermination(termination, output, child.startedAt)
		key, _ := child.handle.relation.ChildKey()
		outcomes = append(outcomes, ChildOutcome{key: key, result: child.result(), boundary: ChildWaitBoundaryDrained})
	}
	waitID := controlValue(ParseWaitID("wait:drained-benchmark"))
	spec := ChildWaitSpec{Key: controlValue(ParseWaitKey("children")), Children: children, Boundary: ChildWaitBoundaryDrained, Condition: AllChildren()}
	opening := mustMailboxSignal(t, "signal:engine:drained-benchmark", waitID, controlValue(encodeChildWaitOpened(spec)))
	if err := root.mailbox.openWait(spec.Key, opening, WaitKindChildren); err != nil {
		t.Fatal(err)
	}
	if _, err := root.mailbox.commit(1); err != nil {
		t.Fatal(err)
	}
	if _, err := root.mailbox.enqueue(StatusWaiting, controlValue(encodeChildWaitSatisfied(waitID, spec.Key, spec.Boundary, outcomes)), signalSourceChildWait); err != nil {
		t.Fatal(err)
	}
	root.status, root.pauseReason = StatusPaused, "retain child outcomes"
	runtime.childWaits[root.handle.processID] = map[WaitID]*childWaitRegistration{waitID: {waitID: waitID, spec: spec}}
	snapshot, err := runtime.captureTree()
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func BenchmarkDrainedTreeSnapshot(b *testing.B) {
	for _, count := range []int{1, 100, 1000, 10000} {
		b.Run(fmt.Sprintf("children_%d", count), func(b *testing.B) {
			snapshot := drainedSnapshotFixture(b, count)
			b.Run("validate", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					validation, err := newTreeSnapshotValidation(snapshot.state)
					if err != nil {
						b.Fatal(err)
					}
					if err := validation.validate(); err != nil {
						b.Fatal(err)
					}
				}
			})
			b.Run("parse", func(b *testing.B) {
				data := snapshot.JSON()
				b.ReportAllocs()
				for b.Loop() {
					if _, err := ParseTreeSnapshot(data); err != nil {
						b.Fatal(err)
					}
				}
			})
		})
	}
}

func BenchmarkTreeCaptureLeafMutation(b *testing.B) {
	for _, count := range []int{1, 100, 1000} {
		for _, size := range []int{1 << 10, 64 << 10, 1 << 20} {
			// Keep the fixture below 64 MiB of retained state.
			if count*size > 64<<20 {
				continue
			}
			b.Run(fmt.Sprintf("processes_%d/state_%d", count, size), func(b *testing.B) {
				owner := newWaitingSnapshotTree(b, count)
				state := controlValue(EncodeExecutionState("benchmark", strings.Repeat("x", size)))
				var leaf *processState
				for _, process := range owner.processes {
					process.committedExecutionState = state
					process.status, process.pauseReason = StatusPaused, "before"
					process.currentWaitID = WaitID{}
					process.mailbox = newSignalMailbox()
					if count == 1 || process.handle.processID != owner.rootID {
						leaf = process
					}
				}
				if _, err := owner.captureTree(); err != nil {
					b.Fatal(err)
				}
				b.ReportAllocs()
				for b.Loop() {
					if leaf.pauseReason == "before" {
						leaf.pauseReason = "after"
					} else {
						leaf.pauseReason = "before"
					}
					if _, err := owner.captureTree(); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

func BenchmarkMemoryTreeCommitterRetention(b *testing.B) {
	owner := newWaitingSnapshotTree(b, 1)
	initial := controlValue(owner.captureTree())
	root := owner.processes[owner.rootID]
	root.status, root.pauseReason = StatusPaused, "first"
	first := controlValue(owner.captureTree())
	root.pauseReason = "second"
	second := controlValue(owner.captureTree())
	start := controlValue(newTreeCheckpoint(1, TreeCheckpointKindStart, Digest{}, initial))
	for _, count := range []int{1, 100, 1000, 10000} {
		b.Run(fmt.Sprintf("commits_%d", count), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				store := NewMemoryTreeCommitter()
				if err := store.CommitCheckpoint(b.Context(), start); err != nil {
					b.Fatal(err)
				}
				previous := initial
				for index := range count {
					next := first
					if index%2 != 0 {
						next = second
					}
					checkpoint := controlValue(newTreeCheckpoint(uint64(index+2), TreeCheckpointKindParked, previous.Digest(), next))
					if err := store.CommitCheckpoint(b.Context(), checkpoint); err != nil {
						b.Fatal(err)
					}
					previous = next
				}
				if len(store.facts) != count+1 || len(store.heads) != 1 {
					b.Fatal("lost replay facts or retained extra heads")
				}
			}
			b.ReportMetric(float64(count+1), "retained_facts")
		})
	}
}

func BenchmarkDeepDrainedTreeSnapshot(b *testing.B) {
	snapshot := deepDrainedSnapshotFixture(b)
	data := snapshot.JSON()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := ParseTreeSnapshot(data); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkTreeSnapshotRetainedWaits(b *testing.B) {
	for _, count := range []int{1, 100, 1000} {
		b.Run(fmt.Sprintf("waits_%d", count), func(b *testing.B) {
			snapshot := retainedWaitsSnapshotFixture(b, count)
			b.ReportAllocs()
			for b.Loop() {
				validation := controlValue(newTreeSnapshotValidation(snapshot.state))
				if err := validation.validate(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkRestoreDrainedTreeSnapshot(b *testing.B) {
	for _, count := range []int{1, 100, 1000} {
		b.Run(fmt.Sprintf("children_%d", count), func(b *testing.B) {
			snapshot := drainedSnapshotFixture(b, count)
			deployment := newChildTestDeployment(b)
			b.ReportAllocs()
			for b.Loop() {
				b.StopTimer()
				engine := controlValue(NewEngine(EngineConfig{TreeCommitter: &recordingTreeCommitter{}}))
				b.StartTimer()
				process, err := engine.RestoreTree(b.Context(), deployment, snapshot)
				b.StopTimer()
				if err != nil {
					b.Fatal(err)
				}
				if err := process.Kill(b.Context(), "benchmark complete"); err != nil {
					b.Fatal(err)
				}
				if err := process.Join(b.Context()); err != nil {
					b.Fatal(err)
				}
				if err := engine.ReleaseTree(b.Context(), process.ID()); err != nil {
					b.Fatal(err)
				}
				if err := engine.Close(b.Context()); err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
			}
		})
	}
}
