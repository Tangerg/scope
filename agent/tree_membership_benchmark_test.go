package agent

import (
	"context"
	"fmt"
	"testing"
)

// These owner operations exclude execution and storage so their traversal cost
// remains visible even when the tree's terminal outcomes are already known.
func BenchmarkTreeOwnerTraversal(b *testing.B) {
	for _, sample := range []treeSnapshotBenchmarkCase{
		{mode: "binary:3", processCount: 15, maxDepth: 3},
		{mode: "binary:5", processCount: 63, maxDepth: 5},
		{mode: "binary:7", processCount: 255, maxDepth: 7},
	} {
		snapshot := benchmarkCompletedTree(b, sample)
		b.Run(fmt.Sprintf("processes_%d", sample.processCount), func(b *testing.B) {
			runtime := benchmarkRestoredOwner(b, snapshot)
			b.Run("stop", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					runtime.stopProcessTree(runtime.processes[runtime.rootID])
				}
			})
			b.Run("join", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					b.StopTimer()
					for _, process := range runtime.processes {
						process.handle.joined = make(chan struct{})
					}
					b.StartTimer()
					if !runtime.publishJoins() || !runtime.processes[runtime.rootID].handle.joinDone() {
						b.Fatal("owner did not publish the complete tree join")
					}
				}
			})
			b.Run("capture", func(b *testing.B) {
				runtime.publishJoins()
				b.ReportAllocs()
				for b.Loop() {
					var err error
					benchmarkTreeSnapshotSink, err = runtime.captureTree()
					if err != nil {
						b.Fatal(err)
					}
				}
			})
		})
	}
}

func benchmarkRestoredOwner(b *testing.B, snapshot TreeSnapshot) *treeRuntime {
	b.Helper()
	engine, err := NewEngine(EngineConfig{})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if closeErr := engine.Close(context.WithoutCancel(b.Context())); closeErr != nil {
			b.Error(closeErr)
		}
	})
	deployment := newChildTestDeployment(b)
	processes := make([]*processState, 0, len(snapshot.ProcessSnapshots()))
	for _, captured := range snapshot.ProcessSnapshots() {
		handle, process, _, restoreErr := prepareRestoredProcess(false, deployment, captured)
		if restoreErr != nil {
			b.Fatal(restoreErr)
		}
		handle.publishResult(process.result())
		handle.finishBookkeeping()
		processes = append(processes, process)
	}
	return newTreeRuntime(engine, snapshot.RootID(), b.Context(), processes...)
}

func BenchmarkReleaseTreeAmongRetainedProcesses(b *testing.B) {
	snapshot := benchmarkCompletedTree(b, treeSnapshotBenchmarkCase{mode: "leaf", processCount: 1, maxDepth: 1})
	for _, count := range []int{1, 1024, 16384} {
		b.Run(fmt.Sprintf("retained_%d", count), func(b *testing.B) {
			runtime := benchmarkRestoredOwner(b, snapshot)
			engine := runtime.engine
			close(runtime.done)
			for range count {
				id, err := newProcessID()
				if err != nil {
					b.Fatal(err)
				}
				root := runtime.processes[runtime.rootID]
				handle := newProcessHandleState(rootProcessRelation(id), root.handle.deploymentRef,
					root.budget, root.capabilities, root.treeLimits, root.startedAt, StatusCompleted)
				result := root.result()
				result.processID = id
				handle.publishResult(result)
				handle.finishBookkeeping()
				handle.finishJoin(nil)
				engine.processes[id] = handle
			}
			b.ReportAllocs()
			for b.Loop() {
				b.StopTimer()
				runtime.processes[runtime.rootID].handle.runtime.Store(runtime)
				engine.trees[runtime.rootID] = runtime
				engine.processes[runtime.rootID] = runtime.processes[runtime.rootID].handle
				b.StartTimer()
				if err := engine.ReleaseTree(b.Context(), runtime.rootID); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
