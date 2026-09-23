package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Captures one unchanged root and its waiting children. The
// fixture uses validated snapshots and excludes execution and storage latency.
func BenchmarkWaitingTreeCapture(b *testing.B) {
	for _, count := range []int{1, 10, 100, 1000} {
		b.Run(fmt.Sprintf("processes_%d", count), func(b *testing.B) {
			runtime := newWaitingSnapshotTree(b, count)
			b.ReportAllocs()
			for b.Loop() {
				var err error
				benchmarkTreeSnapshotSink, err = runtime.captureTree()
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// Measure admission separately from capture: signal admission encodes its
// candidate before the shared tree capacity check; child publication adds one
// freshly initialized Process to that check. Neither iteration advances usage.
func BenchmarkTreeAdmission(b *testing.B) {
	for _, count := range []int{1, 10, 100, 1000} {
		b.Run(fmt.Sprintf("processes_%d", count), func(b *testing.B) {
			for _, quota := range []struct {
				name    string
				process Quota
				tree    Quota
			}{
				{name: "unlimited"},
				{name: "process_quota", process: NewQuota(1 << 20)},
				{name: "tree_quota", tree: NewQuota(uint64(count+1) << 20)},
			} {
				b.Run(quota.name, func(b *testing.B) {
					runtime := newWaitingSnapshotTree(b, count)
					for _, process := range runtime.processes {
						process.limits.MaxSnapshotBytes = quota.process
						process.treeLimits.MaxSnapshotBytes = quota.tree
						process.handle.treeLimits = process.treeLimits
					}
					root := runtime.processes[runtime.rootID]
					signal := controlValue(NewSignal(controlValue(ParseSignalID("signal:benchmark-admission")), WaitID{}, []byte(`{"value":"input"}`)))
					b.Run("signal", func(b *testing.B) {
						b.ReportAllocs()
						for b.Loop() {
							candidate, err := root.prepareSignals([]Signal{signal}, signalSourceExternal)
							if err != nil {
								b.Fatal(err)
							}
							if err := runtime.validateSnapshotCapacity(candidate); err != nil {
								b.Fatal(err)
							}
						}
					})
					relation := childProcessRelation(newProcessID(), root.handle.relation, controlValue(ParseChildKey("admitted")))
					handle := newProcessHandle(relation, root.deployment.DeploymentRef(), root.limits.Budget, root.capabilities, root.treeLimits, root.startedAt)
					handle.childRequestDigest = ComputeDigest([]byte("benchmark-child"))
					child := newProcessState(handle, root.deployment, root.execution, root.committedExecutionState, root.startedAt, root.limits)
					b.Run("child_publication_capacity", func(b *testing.B) {
						b.ReportAllocs()
						for b.Loop() {
							if err := runtime.validateSnapshotCapacity(root, child); err != nil {
								b.Fatal(err)
							}
						}
					})
				})
			}
		})
	}
}

func BenchmarkTreeCommitterFailure(b *testing.B) {
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
				runtime.head = snapshot
				clear(runtime.joinCandidates)
				for _, process := range runtime.processes {
					process.handle.outcomePublished = make(chan struct{})
					process.handle.bookkeepingDone = make(chan struct{})
				}
				b.StartTimer()
				runtime.failRuntime(cause, ProcessID{}, EffectID{})
			}
		})
	}
}

func newWaitingSnapshotTree(t testing.TB, count int) *treeRuntime {
	t.Helper()
	engine, err := NewEngine(EngineConfig{TreeCommitter: NewMemoryTreeCommitter(),
		Limits:     Limits{MaxPendingSignals: 1000, Budget: Budget{Steps: NewQuota(uint64(count)*10 + 100), Effects: NewQuota(uint64(count)*10 + 100), Signals: NewQuota(uint64(count)*10 + 100)}},
		TreeLimits: TreeLimits{MaxDepth: 1, MaxChildren: NewQuota(uint64(count)), MaxActiveChildren: uint32(count), MaxTreeProcesses: NewQuota(uint64(count))},
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
	input, err := EncodePayload(childTestInput{Mode: "leaf"})
	if err != nil {
		t.Fatal(err)
	}
	execution, state, _, err := initializeExecution(t.Context(), deployment.Definition(), input)
	if err != nil {
		t.Fatal(err)
	}
	rootID := newProcessID()
	now := time.Now().Round(0).UTC()
	handle := newProcessHandle(rootProcessRelation(rootID), deployment.DeploymentRef(), engine.limits.Budget, engine.capabilities, engine.treeLimits, now)
	root := newProcessState(handle, deployment, execution, state, now, engine.limits)
	processes := []*processState{root}
	for index := 1; index < count; index++ {
		id := newProcessID()
		key, err := ParseChildKey(fmt.Sprintf("waiting-%d", index))
		if err != nil {
			t.Fatal(err)
		}
		budget := Budget{Steps: NewQuota(10), Effects: NewQuota(10), Signals: NewQuota(10)}
		limits := engine.limits
		limits.Budget = budget
		handle := newProcessHandle(childProcessRelation(id, root.handle.relation, key), deployment.DeploymentRef(), budget, engine.capabilities, engine.treeLimits, now)
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
		signalID, err := ParseSignalID(fmt.Sprintf("signal:engine:waiting-%d", index))
		if err != nil {
			t.Fatal(err)
		}
		signal, err := NewSignal(signalID, waitID, []byte(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		if err := child.mailbox.openWait(waitKey, signal, WaitKindExternal); err != nil {
			t.Fatal(err)
		}
		child.status, child.currentWaitID = StatusWaiting, waitID
		debit, ok := root.limits.Budget.allocation(budget)
		if !ok {
			t.Fatal("invalid allocation")
		}
		root.allocatedResources, ok = root.allocatedResources.add(debit)
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

func BenchmarkIdleDurableTreeInspection(b *testing.B) {
	for _, count := range []int{1, 100, 1000} {
		b.Run(fmt.Sprintf("processes_%d", count), func(b *testing.B) {
			runtime := newWaitingSnapshotTree(b, count)
			root := runtime.processes[runtime.rootID]
			root.status, root.pauseReason = StatusPaused, "inspection benchmark"
			runtime.engine.committer = &recordingTreeCommitter{}
			incarnation := newTreeIncarnationID()
			runtime.incarnation = incarnation
			snapshot, err := runtime.captureTree()
			if err != nil {
				b.Fatal(err)
			}
			runtime.establishHead(incarnation, snapshot)
			ctx, cancel := context.WithCancel(b.Context())
			go runtime.run(ctx)
			b.Cleanup(func() { cancel(); <-runtime.done })
			for {
				inspection, err := runtime.inspect(b.Context())
				if err != nil {
					b.Fatal(err)
				}
				idle := true
				for _, process := range inspection.Processes {
					if process.Work != ProcessWorkIdle {
						idle = false
					}
				}
				if idle {
					break
				}
			}
			b.ReportAllocs()
			for b.Loop() {
				inspection, err := runtime.inspect(b.Context())
				if err != nil || len(inspection.Processes) != count {
					b.Fatalf("inspection: %v", err)
				}
			}
		})
	}
}

func BenchmarkTreeAdmissionRetainedState(b *testing.B) {
	for _, count := range []int{10, 100} {
		for _, stateBytes := range []int{32, 64 << 10} {
			for _, history := range []int{0, 8} {
				for _, limited := range []bool{false, true} {
					b.Run(fmt.Sprintf("members_%d/state_%d/history_%d/tree_quota_%t", count, stateBytes, history, limited), func(b *testing.B) {
						runtime := newWaitingSnapshotTree(b, count)
						for _, process := range runtime.processes {
							process.committedExecutionState = controlValue(EncodeExecutionState("benchmark", strings.Repeat("x", stateBytes)))
							process.status = StatusPaused
							process.pauseReason = "benchmark"
							process.currentWaitID = WaitID{}
							process.mailbox = newSignalMailbox()
							for index := range history {
								signal := controlValue(NewSignal(controlValue(ParseSignalID(fmt.Sprintf("signal:history-%d", index))), WaitID{}, []byte(`{}`)))
								process.mailbox.acceptRecord(newSignalRecord(signal, false))
							}
							if _, err := process.mailbox.commit(uint32(history)); err != nil {
								b.Fatal(err)
							}
							if limited {
								process.treeLimits.MaxSnapshotBytes = NewQuota(1 << 30)
								process.handle.treeLimits = process.treeLimits
							}
						}
						root := runtime.processes[runtime.rootID]
						b.ReportAllocs()
						for b.Loop() {
							if err := runtime.validateSnapshotCapacity(root); err != nil {
								b.Fatal(err)
							}
						}
					})
				}
			}
		}
	}
}
