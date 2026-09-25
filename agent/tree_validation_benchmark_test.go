package agent

import (
	"fmt"
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
	for _, sample := range []struct {
		name         string
		processes    int
		stateBytes   int
		opaqueChange bool
	}{
		{name: "control_374KiB", processes: 1, stateBytes: 374 << 10},
		{name: "root_8MiB_framework", processes: 1, stateBytes: 8 << 20},
		{name: "root_8MiB_opaque", processes: 1, stateBytes: 8 << 20, opaqueChange: true},
		{name: "root_30MiB_framework", processes: 1, stateBytes: 30 << 20},
		{name: "distributed_8MiB_opaque", processes: 32, stateBytes: 256 << 10, opaqueChange: true},
		{name: "processes_1000_framework", processes: 1000, stateBytes: 1 << 10},
	} {
		b.Run(sample.name, func(b *testing.B) {
			owner, leaf := benchmarkMutableSnapshotTree(b, sample.processes, sample.stateBytes)
			before := leaf.committedExecutionState
			after := before
			const changedBytes = 1 << 10
			if sample.opaqueChange {
				after = controlValue(EncodeExecutionState("benchmark", benchmarkOpaqueText(sample.stateBytes+changedBytes)))
			}
			// Alternate a fixed append and reduction. Growing with b.N would
			// change the workload as the benchmark calibrates its iteration count.
			appendState := true
			b.ReportAllocs()
			for b.Loop() {
				if appendState {
					leaf.committedExecutionState, leaf.pauseReason = after, "after"
				} else {
					leaf.committedExecutionState, leaf.pauseReason = before, "before"
				}
				appendState = !appendState
				var err error
				benchmarkTreeSnapshotSink, err = owner.captureTree()
				if err != nil {
					b.Fatal(err)
				}
			}
			fullBytes, processBytes := len(benchmarkTreeSnapshotSink.JSON()), len(leaf.snapshot.JSON())
			b.ReportMetric(float64(fullBytes), "snapshot_bytes")
			b.ReportMetric(float64(processBytes), "changed_process_bytes")
			b.ReportMetric(float64(processBytes)/float64(fullBytes), "changed_process_fraction")
			if sample.opaqueChange {
				b.ReportMetric(changedBytes, "opaque_changed_bytes")
			}
		})
	}
}

func benchmarkMutableSnapshotTree(b *testing.B, count, stateBytes int) (*treeRuntime, *processState) {
	b.Helper()
	owner := newWaitingSnapshotTree(b, count)
	state := controlValue(EncodeExecutionState("benchmark", benchmarkOpaqueText(stateBytes)))
	for _, process := range owner.processes {
		process.committedExecutionState = state
		process.status, process.pauseReason = StatusPaused, "before"
		process.currentWaitID = WaitID{}
		process.mailbox = newSignalMailbox()
	}
	if _, err := owner.captureTree(); err != nil {
		b.Fatal(err)
	}
	leaf := owner.processes[owner.rootID]
	if count > 1 {
		leaf = owner.processes[owner.childrenByParent[owner.rootID][0]]
	}
	return owner, leaf
}

func BenchmarkTreeCaptureGrowthReduction(b *testing.B) {
	owner, root := benchmarkMutableSnapshotTree(b, 1, 1<<20)
	text := benchmarkOpaqueText(4 << 20)
	states := make([]ExecutionState, 0, 8)
	// Each cycle retains 1, 2, 3, 4, 1, 2, 3, then 4 MiB. The reduction
	// is measured in the same complete-state protocol as the growing steps.
	for range 2 {
		for size := 1; size <= 4; size++ {
			states = append(states, controlValue(EncodeExecutionState("benchmark", text[:size<<20])))
		}
	}
	var cumulativeBytes, finalBytes uint64
	b.ReportAllocs()
	for b.Loop() {
		cumulativeBytes = 0
		for _, state := range states {
			root.committedExecutionState = state
			snapshot, err := owner.captureTree()
			if err != nil {
				b.Fatal(err)
			}
			benchmarkTreeSnapshotSink = snapshot
			finalBytes = uint64(snapshot.EncodedSize())
			cumulativeBytes += finalBytes
		}
	}
	b.ReportMetric(float64(len(states)), "captures/op")
	b.ReportMetric(float64(cumulativeBytes), "snapshot_bytes/op")
	b.ReportMetric(float64(finalBytes), "final_snapshot_bytes")
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
