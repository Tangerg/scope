package agent

import (
	"bytes"
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

func TestStartRejectsUnrepresentableSnapshotBeforePublication(t *testing.T) {
	for _, recording := range []bool{false, true} {
		for _, treeQuota := range []bool{false, true} {
			for _, maximum := range []uint64{0, 10_000} {
				t.Run(fmt.Sprintf("recording=%t/tree=%t/bytes=%d", recording, treeQuota, maximum), func(t *testing.T) {
					store := &recordingTreeCommitter{}
					config := EngineConfig{TreeCommitter: NewMemoryTreeCommitter()}
					if recording {
						config.TreeCommitter = store
					}
					if treeQuota {
						config.TreeLimits.MaxSnapshotBytes = NewQuota(maximum)
					} else {
						config.TreeLimits.MaxProcessSnapshotBytes = NewQuota(maximum)
					}
					engine := controlValue(NewEngine(config))
					defer mustCloseEngine(t, engine)
					process, err := engine.Start(t.Context(), newChildTestDeployment(t), controlValue(EncodePayload(childTestInput{Mode: "leaf"})))
					if process != nil {
						_ = process.Kill(t.Context(), "test complete")
						_ = process.Join(context.WithoutCancel(t.Context()))
					}
					if process != nil || !errors.Is(err, ErrResourceLimitExceeded) {
						t.Fatalf("unrepresentable Start = %v, %v", process, err)
					}
					if len(engine.processes) != 0 || len(store.treeCheckpoints()) != 0 {
						t.Fatal("rejected Start published a Process or committed head")
					}
					assertNoPendingProcessStarts(t, engine)
				})
			}
		}
	}
}

func TestSnapshotAdmissionPreservesTerminationAtCapacity(t *testing.T) {
	for _, recording := range []bool{false, true} {
		for _, treeQuota := range []bool{false, true} {
			for _, kill := range []bool{false, true} {
				t.Run(fmt.Sprintf("recording=%t/tree=%t/kill=%t", recording, treeQuota, kill), func(t *testing.T) {
					store := &recordingTreeCommitter{}
					config := EngineConfig{TreeCommitter: NewMemoryTreeCommitter()}
					if recording {
						config.TreeCommitter = store
					}
					if treeQuota {
						config.TreeLimits.MaxSnapshotBytes = NewQuota(256 << 10)
					} else {
						config.TreeLimits.MaxProcessSnapshotBytes = NewQuota(256 << 10)
					}
					engine := controlValue(NewEngine(config))
					defer mustCloseEngine(t, engine)
					deployment := newChildTestDeployment(t)
					process := controlValue(engine.Start(t.Context(), deployment, controlValue(EncodePayload(childTestInput{Mode: "leaf_pause"}))))
					defer func() {
						_ = process.Kill(context.WithoutCancel(t.Context()), "test complete")
						_ = process.Join(context.WithoutCancel(t.Context()))
					}()
					waitForStatus(t, process, StatusPaused)
					capture := func() TreeSnapshot {
						if recording {
							checkpoints := store.treeCheckpoints()
							return checkpoints[len(checkpoints)-1].TreeSnapshot()
						}
						return controlValue(engine.CaptureTree(t.Context(), process.Relation().ProcessID()))
					}
					payload := controlValue(jsonv2.Marshal(strings.Repeat("x", 16<<10)))
					acceptedCount := 0
					for ; acceptedCount < 32; acceptedCount++ {
						before := capture()
						request := controlValue(NewSignalRequest(controlValue(ParseSignalID(fmt.Sprintf("signal:capacity-%d", acceptedCount))), WaitID{}, payload))
						accepted, err := process.DeliverSignals(t.Context(), request)
						if err == nil && accepted {
							continue
						}
						if accepted || !errors.Is(err, ErrResourceLimitExceeded) {
							t.Fatalf("capacity refusal = %t, %v", accepted, err)
						}
						after := capture()
						if before.Digest() != after.Digest() {
							t.Fatal("refused Signal changed the tree")
						}
						break
					}
					if acceptedCount == 0 || acceptedCount == 32 {
						t.Fatalf("unexpected capacity: %d admitted Signals", acceptedCount)
					}
					reason := strings.Repeat("<", maxTerminationReasonBytes)
					want := StatusCanceled
					if kill {
						want = StatusKilled
						if err := process.Kill(t.Context(), reason); err != nil {
							t.Fatal(err)
						}
					} else if err := process.RequestCancellation(t.Context(), reason); err != nil {
						t.Fatal(err)
					}
					if err := process.Join(t.Context()); err != nil {
						t.Fatalf("admitted control could not drain: %v", err)
					}
					result := controlValue(process.Await(t.Context()))
					if result.Termination().Status() != want || result.Termination().Reason() != reason {
						t.Fatalf("termination = %s, reason bytes = %d", result.Termination().Status(), len(result.Termination().Reason()))
					}
					tree := capture()
					tree = controlValue(ParseTreeSnapshot(tree.JSON()))
					if len(tree.ProcessSnapshots()[0].SignalReceipts()) != acceptedCount {
						t.Fatal("termination discarded admitted Signal evidence")
					}
					restoredEngine := controlValue(NewEngine(config))
					defer mustCloseEngine(t, restoredEngine)
					restored := controlValue(restoredEngine.RestoreTree(t.Context(), deployment, tree))
					if err := restored.Join(t.Context()); err != nil {
						t.Fatal(err)
					}
					if restoredResult := controlValue(restored.Await(t.Context())); restoredResult.Termination().Reason() != reason || restoredResult.Termination().Status() != want {
						t.Fatal("restoration changed the acknowledged termination")
					}
				})
			}
		}
	}
}

func TestImmediateChildWaitCapacityRejectionIsAtomic(t *testing.T) {
	for _, treeQuota := range []bool{false, true} {
		t.Run(fmt.Sprintf("tree=%t", treeQuota), func(t *testing.T) {
			runtime, parent := immediateChildWaitScenario(t, func(limits *TreeLimits) {
				limits.MaxProcessSnapshotBytes = NewQuota(1 << 30)
			})
			// The current tree fits exactly, so the answer's growth must exceed it.
			if treeQuota {
				runtime.treeLimits.MaxProcessSnapshotBytes = Quota{}
				runtime.treeLimits.MaxSnapshotBytes = NewQuota(smallestAdmittingTreeQuota(runtime))
			} else {
				runtime.treeLimits.MaxProcessSnapshotBytes = NewQuota(controlValue(parent.snapshotAdmissionSize(runtime.treeLimits)))
			}
			before := controlValue(runtime.captureTree())
			if failure := runtime.finalizePrepared(parent); failure == nil || !errors.Is(failure.cause, ErrResourceLimitExceeded) {
				t.Fatalf("oversized immediate result = %+v", failure)
			}
			after := controlValue(runtime.captureTree())
			if before.Digest() != after.Digest() || len(parent.mailbox.openChildWaits()) != 0 {
				t.Fatal("rejected finalization changed Process facts or opened child waits")
			}
			runtime.advancePrepared(parent)
			failure, failed := parent.finish.Termination.Failure()
			if !failed || failure.Code() != failureCodeEngineLimitChildWaitSignal {
				t.Fatalf("oversized completion did not terminate explicitly: %+v", failure)
			}
			_ = controlValue(runtime.captureTree())
		})
	}
}

// immediateChildWaitScenario prepares a root Step that waits for children the
// tree already finished, so finalization admits the answer immediately. The
// answer names each child, so several children make it outgrow the Step it
// replaces.
func immediateChildWaitScenario(t *testing.T, limit func(*TreeLimits)) (*treeRuntime, *processState) {
	t.Helper()
	runtime := newWaitingSnapshotTree(t, 4)
	limit(&runtime.treeLimits)
	parent := runtime.members.get(runtime.rootID)
	children := runtime.members.childrenOf(runtime.rootID)
	for _, id := range children {
		child := runtime.members.get(id)
		child.installTermination(controlValue((terminationInputs{outcome: completedOutcome(controlValue(EncodePayload(childTestOutput{CompletedKeys: []string{"done"}})))}).resolve()), child.handle.startedAt)
	}
	effect := controlValue(NewChildWaitEffect(ChildWaitSpec{
		Key: controlValue(ParseWaitKey("child.result")), Boundary: ChildWaitBoundaryResult,
		Children: slices.Clone(children), Condition: AllChildren(),
	}))
	if failure := prepareTestStep(parent, runtime.treeLimits, stepJobResult{
		transition: controlValue(Continue(0, effect)), candidate: parent.execution, candidateState: parent.committedExecutionState,
	}); failure != nil {
		t.Fatalf("preparation: %+v", failure)
	}
	runtime.advancePrepared(parent)
	return runtime, parent
}

func smallestAdmittingTreeQuota(runtime *treeRuntime) uint64 {
	low, high := uint64(0), uint64(1<<30)
	for low < high {
		middle := low + (high-low)/2
		runtime.treeLimits.MaxSnapshotBytes = NewQuota(middle)
		if runtime.validateSnapshotCapacity() == nil {
			high = middle
		} else {
			low = middle + 1
		}
	}
	return low
}

func TestRestoreRejectsSnapshotWithoutLifecycleCapacityBeforeActivation(t *testing.T) {
	for _, recording := range []bool{false, true} {
		for _, treeQuota := range []bool{false, true} {
			t.Run(fmt.Sprintf("recording=%t/tree=%t", recording, treeQuota), func(t *testing.T) {
				runtime := newWaitingSnapshotTree(t, 1)
				root := runtime.members.get(runtime.rootID)
				if treeQuota {
					runtime.treeLimits.MaxSnapshotBytes = NewQuota(10_000)
				} else {
					runtime.treeLimits.MaxProcessSnapshotBytes = NewQuota(10_000)
				}
				store := &recordingTreeCommitter{}
				config := EngineConfig{TreeCommitter: NewMemoryTreeCommitter()}
				if recording {
					runtime.writer.identity = newTreeIncarnationID()
					config.TreeCommitter = store
				}
				tree := controlValue(runtime.captureTree())
				engine := controlValue(NewEngine(config))
				defer mustCloseEngine(t, engine)
				process, err := engine.RestoreTree(t.Context(), root.deployment(), tree)
				if process != nil {
					_ = process.Kill(t.Context(), "test complete")
					_ = process.Join(context.WithoutCancel(t.Context()))
				}
				if process != nil || !errors.Is(err, ErrResourceLimitExceeded) {
					t.Fatalf("under-reserved restoration = %v, %v", process, err)
				}
				if len(engine.processes) != 0 || len(engine.restoredProcesses) != 0 || len(engine.restoredChildren) != 0 || len(store.treeActivations()) != 0 {
					t.Fatal("rejected restoration published Processes or activated a writer")
				}
			})
		}
	}
}

func TestTerminalTreeRestoresBelowLiveSnapshotReservation(t *testing.T) {
	for _, recording := range []bool{false, true} {
		t.Run(fmt.Sprintf("recording=%t", recording), func(t *testing.T) {
			config := EngineConfig{TreeCommitter: NewMemoryTreeCommitter(),

				TreeLimits: TreeLimits{MaxSnapshotBytes: NewQuota(64 << 10), MaxProcessSnapshotBytes: NewQuota(64 << 10)},
			}
			runtime := newWaitingSnapshotTree(t, 1)
			root := runtime.members.get(runtime.rootID)
			runtime.treeLimits.MaxProcessSnapshotBytes = config.TreeLimits.MaxProcessSnapshotBytes
			runtime.treeLimits.MaxSnapshotBytes = config.TreeLimits.MaxSnapshotBytes
			root.installTermination(controlValue((terminationInputs{outcome: completedOutcome(controlValue(EncodePayload(childTestOutput{})))}).resolve()), root.handle.startedAt)
			if recording {
				runtime.writer.identity = newTreeIncarnationID()
				config.TreeCommitter = &recordingTreeCommitter{}
			}
			tree := controlValue(ParseTreeSnapshot(controlValue(runtime.captureTree()).JSON()))
			config.TreeCommitter = newSnapshotTestCommitter(tree)
			engine := controlValue(NewEngine(config))
			defer mustCloseEngine(t, engine)
			started, err := engine.Start(t.Context(), root.deployment(), controlValue(EncodePayload(childTestInput{Mode: "leaf"})))
			if started != nil || !errors.Is(err, ErrResourceLimitExceeded) {
				t.Fatalf("live Start below reservation = %v, %v", started, err)
			}
			restored := controlValue(engine.RestoreTree(t.Context(), root.deployment(), tree))
			if err := restored.Join(t.Context()); err != nil {
				t.Fatal(err)
			}
			if result := controlValue(restored.Await(t.Context())); result.Termination().Status() != StatusCompleted {
				t.Fatalf("terminal restoration = %s", result.Termination().Status())
			}
			if !bytes.Equal(inspectProcessSnapshot(t, restored).JSON(), tree.ProcessSnapshots()[0].JSON()) {
				t.Fatal("terminal restoration changed captured Process facts")
			}
		})
	}
}

func TestSnapshotAdmissionPreservesFailureAndUnresolvedEvidence(t *testing.T) {
	for _, treeQuota := range []bool{false, true} {
		t.Run(fmt.Sprintf("tree=%t", treeQuota), func(t *testing.T) {
			runtime := newWaitingSnapshotTree(t, 1)
			process := runtime.members.get(runtime.rootID)
			if treeQuota {
				runtime.treeLimits.MaxSnapshotBytes = NewQuota(512 << 10)
			} else {
				runtime.treeLimits.MaxProcessSnapshotBytes = NewQuota(512 << 10)
			}
			dispatch := controlValue(NewDispatcherEffect(json.RawMessage(`{}`)))
			wait := controlValue(NewWaitEffect(controlValue(ParseWaitKey("after-dispatch"))))
			if failure := prepareTestStep(process, runtime.treeLimits, stepJobResult{
				transition: controlValue(Continue(0, dispatch, wait)), candidate: process.execution, candidateState: process.committedExecutionState,
			}); failure != nil {
				t.Fatalf("preparation: %+v", failure)
			}
			if err := process.prepared.Effects[0].begin(); err != nil {
				t.Fatal(err)
			}
			payload := controlValue(jsonv2.Marshal(strings.Repeat("x", 16<<10)))
			acceptedCount := 0
			for ; acceptedCount < 64; acceptedCount++ {
				signal := controlValue(NewSignal(controlValue(ParseSignalID(fmt.Sprintf("signal:failure-capacity-%d", acceptedCount))), WaitID{}, payload))
				if _, err := runtime.admitSignals(process, []Signal{signal}, signalSourceExternal); err != nil {
					if !errors.Is(err, ErrResourceLimitExceeded) {
						t.Fatal(err)
					}
					break
				}
			}
			if acceptedCount == 0 || acceptedCount == 64 {
				t.Fatalf("unexpected capacity: %d admitted Signals", acceptedCount)
			}
			record := &process.prepared.Effects[0]
			unknown := controlValue(NewSettlement(SettlementStatusUnknown, json.RawMessage(nullJSON)))
			if err := record.settle(unknown, errors.New("connection lost")); err != nil {
				t.Fatal(err)
			}
			diagnostic := *record.diagnostic()
			message := strings.Repeat("<", MaxDiagnosticBytes)
			process.recordFailure(FailureKindExecution, strings.Repeat("x", maxQualifiedNameBytes), errors.New(message))
			runtime.terminatePreparedProcess(process)
			tree, err := runtime.captureTree()
			if err != nil {
				t.Fatalf("failure exhausted admitted snapshot capacity: %v", err)
			}
			snapshot := controlValue(ParseTreeSnapshot(tree.JSON())).ProcessSnapshots()[0]
			result, terminal := snapshot.Result()
			if !terminal || result.Termination().Status() != StatusFailed || result.Termination().Reason() != message {
				t.Fatal("failure reason was lost or rewritten")
			}
			ids := result.Termination().UnresolvedEffectIDs()
			if len(ids) != 1 || ids[0] != record.ID || len(snapshot.SignalReceipts()) != acceptedCount {
				t.Fatal("termination lost unresolved Effect or admitted Signal evidence")
			}
			if retained, ok := snapshot.EffectDiagnostic(ids[0]); !ok || retained != diagnostic {
				t.Fatal("termination lost the uncertain dispatch diagnostic")
			}
		})
	}
}

func TestOversizedSettlementPreservesRecoverableAdmissionBoundary(t *testing.T) {
	for _, recording := range []bool{false, true} {
		for _, treeQuota := range []bool{false, true} {
			t.Run(fmt.Sprintf("recording=%t/tree=%t", recording, treeQuota), func(t *testing.T) {
				store := &recordingTreeCommitter{}
				config := EngineConfig{TreeCommitter: NewMemoryTreeCommitter()}
				if recording {
					config.TreeCommitter = store
				}
				if treeQuota {
					config.TreeLimits.MaxSnapshotBytes = NewQuota(512 << 10)
				} else {
					config.TreeLimits.MaxProcessSnapshotBytes = NewQuota(512 << 10)
				}
				var calls atomic.Int32
				payload := controlValue(jsonv2.Marshal(engineTestMessage{Kind: "result", Value: strings.Repeat("x", 400<<10)}))
				dispatcher := effectFailureTestDispatcher{dispatch: func(request EffectRequest) (Settlement, error) {
					calls.Add(1)
					return NewSettlement(SettlementStatusSucceeded, payload)
				}}
				definition := newEngineTestDefinition(t, "engine.effect", "effect")
				deployment := engineTestDeployment(t, definition, dispatcher)
				engine := controlValue(NewEngine(config))
				defer mustCloseEngine(t, engine)
				process := controlValue(engine.Start(t.Context(), deployment, controlValue(EncodePayload(engineTestInput{Value: "bounded"}))))
				joinErr := process.Join(t.Context())
				result, awaitErr := process.Await(t.Context())
				runtimeErr, stopped := errors.AsType[*RuntimeError](awaitErr)
				if !stopped || result.Valid() || !errors.Is(joinErr, ErrResourceLimitExceeded) || !errors.Is(awaitErr, ErrResourceLimitExceeded) {
					t.Fatalf("oversized settlement: result=%s Await=%v Join=%v", result.Termination().Status(), awaitErr, joinErr)
				}
				ids := runtimeErr.UnresolvedEffectIDs()
				if len(ids) != 1 || calls.Load() != 1 {
					t.Fatalf("unresolved identities=%v dispatches=%d", ids, calls.Load())
				}
				var tree TreeSnapshot
				if recording {
					boundaries := store.effectBoundaries()
					if len(boundaries) != 1 || boundaries[0].Kind() != EffectBoundaryKindPending {
						t.Fatal("unrepresentable settlement advanced the committed boundary")
					}
					tree = boundaries[0].TreeSnapshot()
					if runtimeErr.HeadDigest() != tree.Digest() {
						t.Fatal("runtime fault lost the acknowledged head")
					}
				} else {
					var found bool
					var err error
					tree, found, err = config.TreeCommitter.(*MemoryTreeCommitter).LoadTree(t.Context(), process.Relation().ProcessID())
					if err != nil || !found || runtimeErr.HeadDigest() != tree.Digest() || !runtimeErr.IncarnationID().Valid() {
						t.Fatalf("fault head: %v", err)
					}
				}
				tree = controlValue(ParseTreeSnapshot(tree.JSON()))
				snapshot := tree.ProcessSnapshots()[0]
				if snapshot.Status().Terminal() || len(maps.Collect(snapshot.Settlements())) != 0 {
					t.Fatal("capacity refusal adopted a settlement or fabricated termination")
				}
				restoredEngine := controlValue(NewEngine(config))
				defer mustCloseEngine(t, restoredEngine)
				restored := controlValue(restoredEngine.RestoreTree(t.Context(), deployment, tree))
				defer func() {
					_ = restored.Kill(context.WithoutCancel(t.Context()), "test complete")
					_ = restored.Join(context.WithoutCancel(t.Context()))
				}()
				unknown := waitForUnknownSettlement(t, restored).UnknownEffectIDs()
				if len(unknown) != 1 || unknown[0] != ids[0] || calls.Load() != 1 {
					t.Fatal("recovery replayed an uncertain Effect or changed its identity")
				}
				settlement := controlValue(NewSettlement(SettlementStatusSucceeded, json.RawMessage(`{"kind":"result","value":"reconciled"}`)))
				if err := restored.ResolveUnknownEffect(t.Context(), unknown[0], settlement); err != nil {
					t.Fatal(err)
				}
				if err := restored.Join(t.Context()); err != nil {
					t.Fatal(err)
				}
				if final := controlValue(restored.Await(t.Context())); final.Termination().Status() != StatusCompleted || calls.Load() != 1 {
					t.Fatal("explicit reconciliation did not complete without replay")
				}
			})
		}
	}
}

func TestDispatchPermissionRequiresUncertainOutcomeCapacity(t *testing.T) {
	for _, recording := range []bool{false, true} {
		for _, treeQuota := range []bool{false, true} {
			t.Run(fmt.Sprintf("recording=%t/tree=%t", recording, treeQuota), func(t *testing.T) {
				store := &recordingTreeCommitter{}
				config := EngineConfig{TreeCommitter: NewMemoryTreeCommitter()}
				if recording {
					config.TreeCommitter = store
				}
				if treeQuota {
					config.TreeLimits.MaxSnapshotBytes = NewQuota(512 << 10)
				} else {
					config.TreeLimits.MaxProcessSnapshotBytes = NewQuota(512 << 10)
				}
				effect := controlValue(NewDispatcherEffect(controlValue(jsonv2.Marshal(strings.Repeat("x", 350<<10)))))
				definition := &capacityDefinition{
					engineTestDefinition: newEngineTestDefinition(t, "engine.effect", "effect"),
					effects:              []Effect{effect},
				}
				var calls atomic.Int32
				dispatcher := effectFailureTestDispatcher{dispatch: func(request EffectRequest) (Settlement, error) {
					calls.Add(1)
					return NewSettlement(SettlementStatusSucceeded, json.RawMessage(`{"kind":"result","value":"dispatched"}`))
				}}
				engine := controlValue(NewEngine(config))
				defer mustCloseEngine(t, engine)
				process := controlValue(engine.Start(t.Context(), engineTestDeployment(t, definition, dispatcher),
					controlValue(EncodePayload(engineTestInput{Value: "bounded"}))))
				if err := process.Join(t.Context()); err != nil {
					t.Fatal(err)
				}
				result := controlValue(process.Await(t.Context()))
				failure, failed := result.Termination().Failure()
				if !failed || failure.Code() != failureCodeEngineLimitSnapshot || result.Usage().PreparedEffects != 1 {
					t.Fatalf("pending admission: failure=%+v usage=%+v", failure, result.Usage())
				}
				if calls.Load() != 0 || len(store.effectBoundaries()) != 0 || len(result.Termination().UnresolvedEffectIDs()) != 0 {
					t.Fatal("rejected permission dispatched or invented uncertain execution")
				}
				snapshot := inspectProcessSnapshot(t, process)
				if snapshot.state.Prepared == nil || snapshot.state.Prepared.Effects[0].phase() != effectPhasePlanned {
					t.Fatal("rejected permission changed the planned Effect evidence")
				}
			})
		}
	}
}
