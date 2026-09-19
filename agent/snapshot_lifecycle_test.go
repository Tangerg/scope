package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
)

func TestStartRejectsUnrepresentableSnapshotBeforePublication(t *testing.T) {
	for _, durable := range []bool{false, true} {
		for _, treeQuota := range []bool{false, true} {
			for _, maximum := range []uint64{0, 10_000} {
				t.Run(fmt.Sprintf("durable=%t/tree=%t/bytes=%d", durable, treeQuota, maximum), func(t *testing.T) {
					store := &recordingTreeDurability{}
					config := EngineConfig{}
					if durable {
						config.TreeDurability = store
					}
					if treeQuota {
						config.TreeLimits.MaxSnapshotBytes = NewQuota(maximum)
					} else {
						config.Limits.MaxSnapshotBytes = NewQuota(maximum)
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
						t.Fatal("rejected Start published a Process or durable head")
					}
					assertNoPendingProcessStarts(t, engine)
				})
			}
		}
	}
}

func TestSnapshotAdmissionPreservesTerminationAtCapacity(t *testing.T) {
	for _, durable := range []bool{false, true} {
		for _, treeQuota := range []bool{false, true} {
			for _, kill := range []bool{false, true} {
				t.Run(fmt.Sprintf("durable=%t/tree=%t/kill=%t", durable, treeQuota, kill), func(t *testing.T) {
					store := &recordingTreeDurability{}
					config := EngineConfig{}
					if durable {
						config.TreeDurability = store
					}
					if treeQuota {
						config.TreeLimits.MaxSnapshotBytes = NewQuota(256 << 10)
					} else {
						config.Limits.MaxSnapshotBytes = NewQuota(256 << 10)
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
						if durable {
							checkpoints := store.treeCheckpoints()
							return checkpoints[len(checkpoints)-1].TreeSnapshot()
						}
						return controlValue(engine.CaptureTree(t.Context(), process.ID()))
					}
					payload := controlValue(json.Marshal(strings.Repeat("x", 16<<10)))
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
					if result.Status() != want || result.Termination().Reason() != reason {
						t.Fatalf("termination = %s, reason bytes = %d", result.Status(), len(result.Termination().Reason()))
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
					if restoredResult := controlValue(restored.Await(t.Context())); restoredResult.Termination().Reason() != reason || restoredResult.Status() != want {
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
			runtime := newWaitingSnapshotTree(t, 2)
			parent := runtime.processes[runtime.rootID]
			var child *processState
			for _, member := range runtime.processes {
				if treeQuota {
					member.treeLimits.MaxSnapshotBytes = NewQuota(528 << 10)
					member.handle.treeLimits = member.treeLimits
				} else {
					member.limits.MaxSnapshotBytes = NewQuota(320 << 10)
				}
				if member != parent {
					child = member
				}
			}
			child.installTermination(controlValue((terminationFacts{outcome: completedOutcome()}).resolve()),
				controlValue(EncodePayload(childTestOutput{CompletedKeys: []string{strings.Repeat("x", 200<<10)}})), child.startedAt)
			child.mailbox.closeAllWaits()
			signal := controlValue(newSignal(controlValue(ParseSignalID("signal:padding")), WaitID{}, controlValue(json.Marshal(strings.Repeat("x", 150<<10)))))
			if _, err := runtime.admitSignals(parent, []Signal{signal}, signalSourceExternal); err != nil {
				t.Fatal(err)
			}
			effect := controlValue(NewChildWaitEffect(ChildWaitSpec{
				Key: controlValue(ParseWaitKey("child.result")), Boundary: ChildWaitBoundaryResult,
				Children: []ProcessID{child.handle.processID}, Condition: AllChildren(),
			}))
			if failure := prepareTestStep(parent, stepJobResult{
				transition: controlValue(Continue(0, effect)), candidate: parent.execution, candidateState: parent.committedExecutionState,
			}); failure != nil {
				t.Fatalf("preparation: %+v", failure)
			}
			runtime.advancePrepared(parent)
			before := controlValue(runtime.captureTree())
			if err := runtime.finalizePrepared(parent); !errors.Is(err, ErrResourceLimitExceeded) {
				t.Fatalf("oversized immediate result = %v", err)
			}
			after := controlValue(runtime.captureTree())
			if before.Digest() != after.Digest() || len(runtime.childWaits) != 0 {
				t.Fatal("rejected finalization changed Process facts or wait registrations")
			}
			runtime.advancePrepared(parent)
			failure, failed := parent.termination.Failure()
			if !failed || failure.Code() != failureCodeEngineLimitChildWaitSignal {
				t.Fatalf("oversized completion did not terminate explicitly: %+v", failure)
			}
			_ = controlValue(runtime.captureTree())
		})
	}
}

func TestRestoreRejectsSnapshotWithoutLifecycleCapacityBeforeActivation(t *testing.T) {
	for _, durable := range []bool{false, true} {
		for _, treeQuota := range []bool{false, true} {
			t.Run(fmt.Sprintf("durable=%t/tree=%t", durable, treeQuota), func(t *testing.T) {
				runtime := newWaitingSnapshotTree(t, 1)
				root := runtime.processes[runtime.rootID]
				if treeQuota {
					root.treeLimits.MaxSnapshotBytes = NewQuota(10_000)
				} else {
					root.limits.MaxSnapshotBytes = NewQuota(10_000)
				}
				store := &recordingTreeDurability{}
				config := EngineConfig{}
				if durable {
					runtime.incarnation = newTreeIncarnationID()
					config.TreeDurability = store
				}
				tree := controlValue(runtime.captureTree())
				engine := controlValue(NewEngine(config))
				defer mustCloseEngine(t, engine)
				process, err := engine.RestoreTree(t.Context(), root.deployment, tree)
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
	for _, durable := range []bool{false, true} {
		t.Run(fmt.Sprintf("durable=%t", durable), func(t *testing.T) {
			config := EngineConfig{
				Limits:     Limits{MaxSnapshotBytes: NewQuota(64 << 10)},
				TreeLimits: TreeLimits{MaxSnapshotBytes: NewQuota(64 << 10)},
			}
			runtime := newWaitingSnapshotTree(t, 1)
			root := runtime.processes[runtime.rootID]
			root.limits.MaxSnapshotBytes = config.Limits.MaxSnapshotBytes
			root.treeLimits.MaxSnapshotBytes = config.TreeLimits.MaxSnapshotBytes
			root.handle.treeLimits = root.treeLimits
			root.installTermination(controlValue((terminationFacts{outcome: completedOutcome()}).resolve()),
				controlValue(EncodePayload(childTestOutput{})), root.startedAt)
			if durable {
				runtime.incarnation = newTreeIncarnationID()
				config.TreeDurability = &recordingTreeDurability{}
			}
			tree := controlValue(ParseTreeSnapshot(controlValue(runtime.captureTree()).JSON()))
			engine := controlValue(NewEngine(config))
			defer mustCloseEngine(t, engine)
			started, err := engine.Start(t.Context(), root.deployment, controlValue(EncodePayload(childTestInput{Mode: "leaf"})))
			if started != nil || !errors.Is(err, ErrResourceLimitExceeded) {
				t.Fatalf("live Start below reservation = %v, %v", started, err)
			}
			restored := controlValue(engine.RestoreTree(t.Context(), root.deployment, tree))
			if err := restored.Join(t.Context()); err != nil {
				t.Fatal(err)
			}
			if result := controlValue(restored.Await(t.Context())); result.Status() != StatusCompleted {
				t.Fatalf("terminal restoration = %s", result.Status())
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
			process := runtime.processes[runtime.rootID]
			if treeQuota {
				process.treeLimits.MaxSnapshotBytes = NewQuota(512 << 10)
			} else {
				process.limits.MaxSnapshotBytes = NewQuota(512 << 10)
			}
			dispatch := controlValue(NewDispatcherEffect(json.RawMessage(`{}`)))
			wait := controlValue(NewWaitEffect(controlValue(ParseWaitKey("after-dispatch")), json.RawMessage(`{}`)))
			if failure := prepareTestStep(process, stepJobResult{
				transition: controlValue(Continue(0, dispatch, wait)), candidate: process.execution, candidateState: process.committedExecutionState,
			}); failure != nil {
				t.Fatalf("preparation: %+v", failure)
			}
			if err := process.prepared.Effects[0].begin(); err != nil {
				t.Fatal(err)
			}
			payload := controlValue(json.Marshal(strings.Repeat("x", 16<<10)))
			acceptedCount := 0
			for ; acceptedCount < 64; acceptedCount++ {
				signal := controlValue(newSignal(controlValue(ParseSignalID(fmt.Sprintf("signal:failure-capacity-%d", acceptedCount))), WaitID{}, payload))
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
			unknown := controlValue(NewSettlement(record.ID, SettlementStatusUnknown, json.RawMessage(nullJSON)))
			if err := record.settle(unknown, errors.New("connection lost")); err != nil {
				t.Fatal(err)
			}
			diagnostic := *record.Diagnostic
			message := strings.Repeat("<", MaxDiagnosticBytes)
			process.recordFailure(FailureKindExecution, strings.Repeat("x", maxFailureCodeBytes), errors.New(message))
			runtime.terminatePreparedProcess(process)
			tree, err := runtime.captureTree()
			if err != nil {
				t.Fatalf("failure exhausted admitted snapshot capacity: %v", err)
			}
			snapshot := controlValue(ParseTreeSnapshot(tree.JSON())).ProcessSnapshots()[0]
			result, terminal := snapshot.Result()
			if !terminal || result.Status() != StatusFailed || result.Termination().Reason() != message {
				t.Fatal("failure reason was lost or rewritten")
			}
			ids := result.Termination().UnresolvedEffectIDs()
			if len(ids) != 1 || ids[0] != unknown.EffectID() || len(snapshot.SignalReceipts()) != acceptedCount {
				t.Fatal("termination lost unresolved Effect or admitted Signal evidence")
			}
			if retained, ok := snapshot.EffectDiagnostic(ids[0]); !ok || retained != diagnostic {
				t.Fatal("termination lost the uncertain dispatch diagnostic")
			}
		})
	}
}

func TestOversizedSettlementPreservesRecoverableAdmissionBoundary(t *testing.T) {
	for _, durable := range []bool{false, true} {
		for _, treeQuota := range []bool{false, true} {
			t.Run(fmt.Sprintf("durable=%t/tree=%t", durable, treeQuota), func(t *testing.T) {
				store := &recordingTreeDurability{}
				config := EngineConfig{}
				if durable {
					config.TreeDurability = store
				}
				if treeQuota {
					config.TreeLimits.MaxSnapshotBytes = NewQuota(512 << 10)
				} else {
					config.Limits.MaxSnapshotBytes = NewQuota(512 << 10)
				}
				var calls atomic.Int32
				payload := controlValue(json.Marshal(engineTestMessage{Kind: "result", Value: strings.Repeat("x", 400<<10)}))
				dispatcher := effectFailureTestDispatcher{dispatch: func(request EffectRequest) (Settlement, error) {
					calls.Add(1)
					return NewSettlement(request.ID(), SettlementStatusSucceeded, payload)
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
					t.Fatalf("oversized settlement: result=%s Await=%v Join=%v", result.Status(), awaitErr, joinErr)
				}
				ids := runtimeErr.UnresolvedEffectIDs()
				if len(ids) != 1 || calls.Load() != 1 {
					t.Fatalf("unresolved identities=%v dispatches=%d", ids, calls.Load())
				}
				var tree TreeSnapshot
				if durable {
					boundaries := store.effectBoundaries()
					if len(boundaries) != 1 || boundaries[0].Kind() != EffectBoundaryKindPending {
						t.Fatal("unrepresentable settlement advanced the durable boundary")
					}
					tree = boundaries[0].TreeSnapshot()
					if runtimeErr.HeadDigest() != tree.Digest() {
						t.Fatal("runtime fault lost the acknowledged head")
					}
				} else {
					tree = controlValue(engine.CaptureTree(t.Context(), process.ID()))
					if runtimeErr.HeadDigest().Valid() || runtimeErr.IncarnationID().Valid() {
						t.Fatal("ephemeral fault invented durable authority")
					}
				}
				tree = controlValue(ParseTreeSnapshot(tree.JSON()))
				snapshot := tree.ProcessSnapshots()[0]
				if snapshot.Status().Terminal() || len(snapshot.Settlements()) != 0 {
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
				settlement := controlValue(NewSettlement(ids[0], SettlementStatusSucceeded, json.RawMessage(`{"kind":"result","value":"reconciled"}`)))
				if err := restored.ResolveUnknownEffect(t.Context(), settlement); err != nil {
					t.Fatal(err)
				}
				if err := restored.Join(t.Context()); err != nil {
					t.Fatal(err)
				}
				if final := controlValue(restored.Await(t.Context())); final.Status() != StatusCompleted || calls.Load() != 1 {
					t.Fatal("explicit reconciliation did not complete without replay")
				}
			})
		}
	}
}

func TestDispatchPermissionRequiresUncertainOutcomeCapacity(t *testing.T) {
	for _, durable := range []bool{false, true} {
		for _, treeQuota := range []bool{false, true} {
			t.Run(fmt.Sprintf("durable=%t/tree=%t", durable, treeQuota), func(t *testing.T) {
				store := &recordingTreeDurability{}
				config := EngineConfig{}
				if durable {
					config.TreeDurability = store
				}
				if treeQuota {
					config.TreeLimits.MaxSnapshotBytes = NewQuota(512 << 10)
				} else {
					config.Limits.MaxSnapshotBytes = NewQuota(512 << 10)
				}
				effect := controlValue(NewDispatcherEffect(controlValue(json.Marshal(strings.Repeat("x", 350<<10)))))
				definition := &capacityDefinition{
					engineTestDefinition: newEngineTestDefinition(t, "engine.effect", "effect"),
					effects:              []Effect{effect},
				}
				var calls atomic.Int32
				dispatcher := effectFailureTestDispatcher{dispatch: func(request EffectRequest) (Settlement, error) {
					calls.Add(1)
					return NewSettlement(request.ID(), SettlementStatusSucceeded, json.RawMessage(`{"kind":"result","value":"dispatched"}`))
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
				if snapshot.state.Prepared == nil || snapshot.state.Prepared.Effects[0].Phase != effectPhasePlanned {
					t.Fatal("rejected permission changed the planned Effect evidence")
				}
			})
		}
	}
}
