package agent

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// One active root advances while its waiting children remain unchanged. The
// fixture uses validated snapshots and excludes execution and storage latency.
func BenchmarkWaitingTreeCapture(b *testing.B) {
	for _, count := range []int{1, 10, 100, 1000} {
		b.Run(fmt.Sprintf("processes_%d", count), func(b *testing.B) {
			runtime := newWaitingSnapshotTree(b, count)
			root := runtime.processes[runtime.rootID]
			b.ReportAllocs()
			for b.Loop() {
				root.committedSteps++
				root.usage.CommittedSteps++
				var err error
				benchmarkTreeSnapshotSink, err = runtime.captureTree()
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkTreeDurabilityFailure(b *testing.B) {
	for _, count := range []int{1, 10, 100, 1000} {
		b.Run(fmt.Sprintf("processes_%d", count), func(b *testing.B) {
			runtime := newWaitingSnapshotTree(b, count)
			snapshot, err := runtime.captureTree()
			if err != nil {
				b.Fatal(err)
			}
			cause := errors.New("storage failed")
			b.ReportAllocs()
			for b.Loop() {
				b.StopTimer()
				runtime.fault = nil
				runtime.head = &treeHead{snapshot: snapshot, advanced: make(chan struct{})}
				clear(runtime.joinCandidates)
				for _, process := range runtime.processes {
					process.handle.outcomePublished = make(chan struct{})
					process.handle.bookkeepingDone = make(chan struct{})
				}
				b.StartTimer()
				runtime.failDurability(cause, ProcessID{}, EffectID{})
			}
		})
	}
}

func newWaitingSnapshotTree(t testing.TB, count int) *treeRuntime {
	t.Helper()
	engine, err := NewEngine(EngineConfig{
		Limits:     Limits{MaxSteps: 1 << 50, MaxEffects: 1 << 50, MaxSignals: 1 << 50, MaxPendingSignals: 1000},
		TreeLimits: TreeLimits{MaxDepth: 1, MaxChildren: uint32(count), MaxActiveChildren: uint32(count), MaxTreeProcesses: uint32(count)},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := engine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
			t.Error(closeErr)
		}
	})
	deployment := newChildTestDeployment(t)
	input, err := EncodeInput(childTestInput{Mode: "leaf"})
	if err != nil {
		t.Fatal(err)
	}
	execution, state, _, err := initializeExecution(deployment.Definition(), input)
	if err != nil {
		t.Fatal(err)
	}
	rootID, err := newProcessID()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Round(0).UTC()
	handle := newProcessHandleState(rootProcessRelation(rootID), deployment.DeploymentRef(), engine.limits.budget(), engine.capabilities, engine.treeLimits, now, StatusRunning)
	root := newProcessState(handle, deployment, execution, state, now, engine.limits)
	processes := []*processState{root}
	for index := 1; index < count; index++ {
		id, err := newProcessID()
		if err != nil {
			t.Fatal(err)
		}
		key, err := ParseChildKey(fmt.Sprintf("waiting-%d", index))
		if err != nil {
			t.Fatal(err)
		}
		budget := Budget{Steps: 10, Effects: 10, Signals: 10}
		limits, err := budget.limits(engine.limits.MaxPendingSignals)
		if err != nil {
			t.Fatal(err)
		}
		handle := newProcessHandleState(childProcessRelation(id, root.handle.relation, key), deployment.DeploymentRef(), budget, engine.capabilities, engine.treeLimits, now, StatusWaiting)
		handle.childRequestDigest = ComputeDigest([]byte(key.String()))
		child := newProcessState(handle, deployment, execution, state, now, limits)
		waitID, err := ParseWaitID(fmt.Sprintf("wait:waiting-%d", index))
		if err != nil {
			t.Fatal(err)
		}
		waitKey, err := ParseWaitKey("external")
		if err != nil {
			t.Fatal(err)
		}
		signalID, err := ParseSignalID(fmt.Sprintf("signal:waiting-%d", index))
		if err != nil {
			t.Fatal(err)
		}
		signal, err := newSignal(signalID, waitID, []byte(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		if err := child.mailbox.openWait(waitKey, signal, true); err != nil {
			t.Fatal(err)
		}
		child.status, child.currentWaitID = StatusWaiting, waitID
		child.usage.AcceptedSignals = 1
		var ok bool
		root.reservedBudget, ok = root.reservedBudget.add(budget)
		if !ok {
			t.Fatal("child allocation overflow")
		}
		processes = append(processes, child)
	}
	runtime := newTreeRuntime(engine, rootID, t.Context(), processes...)
	if _, err := runtime.captureTree(); err != nil {
		t.Fatal(err)
	}
	return runtime
}
