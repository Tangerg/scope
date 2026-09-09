package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestTerminationCancelsDispatchAndStopsPreparedBatch(t *testing.T) {
	for _, durable := range []bool{false, true} {
		for _, status := range []SettlementStatus{SettlementStatusSucceeded, SettlementStatusFailed, SettlementStatusUnknown} {
			name := "ephemeral"
			if durable {
				name = "durable"
			}
			name += "/" + status.String()
			t.Run(name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					dispatcher := &cancellationDispatcher{
						entered: make(chan EffectRequest, 2), canceled: make(chan struct{}),
						release: make(chan struct{}), status: status,
					}
					release := sync.OnceFunc(func() { close(dispatcher.release) })
					defer release()
					config := EngineConfig{}
					if durable {
						config.TreeDurability = &recordingTreeDurability{}
					}
					engine, err := NewEngine(config)
					if err != nil {
						t.Fatal(err)
					}
					definition := newEngineTestDefinition(t, "engine.batch", "batch")
					deployment := engineTestDeployment(t, definition, dispatcher)
					input, _ := EncodeInput(engineTestInput{Value: "cancel batch"})
					process, err := engine.Start(t.Context(), deployment, input)
					if err != nil {
						t.Fatal(err)
					}
					first := <-dispatcher.entered
					if killErr := process.Kill(t.Context(), "stop accepted work"); killErr != nil {
						t.Fatal(killErr)
					}
					synctest.Wait()
					select {
					case <-dispatcher.canceled:
					default:
						t.Error("terminal intent did not reach the active Dispatch context")
					}
					if inspectProcessSnapshot(t, process).Status().Terminal() {
						t.Error("Process became terminal before the Dispatcher returned its settlement")
					}
					release()
					result := mustAwait(t, process)
					if result.Status() != StatusKilled {
						t.Fatalf("termination = %+v", result.Termination())
					}
					select {
					case later := <-dispatcher.entered:
						t.Errorf("later Effect %s started after cancellation", later.ID())
					default:
					}
					unresolved := result.Termination().UnresolvedEffectIDs()
					if status == SettlementStatusUnknown {
						if len(unresolved) != 1 || unresolved[0] != first.ID() {
							t.Errorf("unresolved Effects = %v, want %s", unresolved, first.ID())
						}
					} else if len(unresolved) != 0 {
						t.Errorf("definite return became uncertain: %v", unresolved)
					}
					snapshot := inspectProcessSnapshot(t, process)
					if !durable && status == SettlementStatusUnknown {
						assertInterruptedSnapshotValidation(t, snapshot)
					}
					wire, err := snapshot.wire()
					if err != nil {
						t.Fatal(err)
					}
					if wire.Prepared == nil || len(wire.Prepared.Effects) != 2 {
						t.Fatalf("interrupted batch evidence = %+v", wire.Prepared)
					}
					settled, planned := wire.Prepared.Effects[0], wire.Prepared.Effects[1]
					if settled.Phase != effectPhaseSettled || settled.Settlement.Status() != status ||
						planned.Phase != effectPhasePlanned || planned.Settlement != nil {
						t.Errorf("interrupted Effects = %+v", wire.Prepared.Effects)
					}
					state, err := wireJSON.decode[engineTestState](wire.CommittedExecutionState.Payload())
					if err != nil || state.Phase != "ready" || wire.CommittedSteps != 0 ||
						wire.Mailbox.SignalCursor != 0 || wire.Usage.AcceptedSignals != 0 || wire.Usage.PreparedEffects != 2 {
						t.Errorf("interrupted candidate changed committed facts: state=%+v usage=%+v cursor=%d error=%v", state, wire.Usage, wire.Mailbox.SignalCursor, err)
					}
					if status != SettlementStatusUnknown && string(settled.Settlement.Payload()) != `{"done":true}` {
						t.Errorf("settlement payload = %s", settled.Settlement.Payload())
					}
					tree := interruptedTreeSnapshot(t, engine, process, config)
					restoredEngine, err := NewEngine(config)
					if err != nil {
						t.Fatal(err)
					}
					restored, err := restoredEngine.RestoreTree(t.Context(), deployment, tree)
					if err != nil {
						t.Fatal(err)
					}
					if got := mustAwait(t, restored); got.Status() != result.Status() || got.Usage() != result.Usage() {
						t.Fatalf("restored result = %+v, want %+v", got, result)
					}
					resolution, _ := NewSettlement(first.ID(), SettlementStatusSucceeded, json.RawMessage(`{}`))
					for _, terminal := range []*Process{process, restored} {
						if err := terminal.ResolveUnknownEffect(t.Context(), resolution); !errors.Is(err, ErrProcessFinished) {
							t.Errorf("terminal resolution error = %v", err)
						}
						if current := inspectProcessSnapshot(t, terminal); !bytes.Equal(current.JSON(), snapshot.JSON()) {
							t.Errorf("terminal evidence changed after restoration or adjudication: %s", current.JSON())
						}
					}
					if got := dispatcher.replayQueries.Load(); got != 0 {
						t.Errorf("terminal restoration queried replay policy %d times", got)
					}
					mustCloseEngine(t, restoredEngine)
					mustCloseEngine(t, engine)
				})
			})
		}
	}
}

func TestHostTerminationCancelsActiveDispatch(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		name := "cancellation"
		if deadline {
			name = "deadline"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				dispatcher := &cancellationDispatcher{
					entered: make(chan EffectRequest, 1), canceled: make(chan struct{}),
					release: make(chan struct{}), status: SettlementStatusSucceeded,
				}
				release := sync.OnceFunc(func() { close(dispatcher.release) })
				defer release()
				engine, err := NewEngine(EngineConfig{})
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(t.Context(), time.Second)
				defer cancel()
				deployment := engineTestDeployment(t, newEngineTestDefinition(t, "engine.effect", "effect"), dispatcher)
				input, _ := EncodeInput(engineTestInput{Value: "host termination"})
				process, err := engine.Start(ctx, deployment, input)
				if err != nil {
					t.Fatal(err)
				}
				<-dispatcher.entered
				if deadline {
					time.Sleep(time.Second)
				} else {
					cancel()
				}
				<-dispatcher.canceled
				release()
				result := mustAwait(t, process)
				wantStatus, wantCause := StatusCanceled, TerminationCauseHostCancellation
				if deadline {
					wantStatus, wantCause = StatusTimedOut, TerminationCauseHostDeadline
				}
				if result.Status() != wantStatus || result.Termination().Cause() != wantCause {
					t.Errorf("host termination = %+v", result.Termination())
				}
				mustCloseEngine(t, engine)
			})
		})
	}
}

func assertInterruptedSnapshotValidation(t *testing.T, snapshot ProcessSnapshot) {
	t.Helper()
	for name, change := range map[string]func(*processSnapshotWire){
		"pending terminal effect": func(wire *processSnapshotWire) {
			wire.Prepared.Effects[0].Phase = effectPhasePending
			wire.Prepared.Effects[0].Settlement = nil
		},
		"missing uncertainty evidence": func(wire *processSnapshotWire) {
			wire.Prepared = nil
		},
		"missing termination uncertainty": func(wire *processSnapshotWire) {
			termination := wire.Termination.withUnresolvedEffectIDs(nil)
			wire.Termination = &termination
		},
	} {
		wire, err := snapshot.wire()
		if err != nil {
			t.Fatal(err)
		}
		change(&wire)
		data, err := json.Marshal(wire)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ParseProcessSnapshot(data); !errors.Is(err, ErrInvalidSnapshot) {
			t.Errorf("%s accepted: %v", name, err)
		}
	}
}

type cancellationDispatcher struct {
	entered       chan EffectRequest
	canceled      chan struct{}
	release       chan struct{}
	status        SettlementStatus
	frontier      uint32
	replayQueries atomic.Uint32
}

func (c *cancellationDispatcher) Dispatch(
	ctx context.Context,
	request EffectRequest,
	_ DeltaEmitter,
) (Settlement, error) {
	c.entered <- request
	if request.BatchIndex() != c.frontier {
		return NewSettlement(request.ID(), SettlementStatusSucceeded, json.RawMessage(`{"done":true}`))
	}
	select {
	case <-ctx.Done():
		close(c.canceled)
		<-c.release
	case <-c.release:
	}
	if c.status == SettlementStatusUnknown {
		return Settlement{}, errors.New("external outcome is uncertain")
	}
	return NewSettlement(request.ID(), c.status, json.RawMessage(`{"done":true}`))
}

func (c *cancellationDispatcher) ReplayPolicy(Effect) ReplayPolicy {
	c.replayQueries.Add(1)
	return ReplayPolicySameIdentity
}

func interruptedTreeSnapshot(t *testing.T, engine *Engine, process *Process, config EngineConfig) TreeSnapshot {
	t.Helper()
	if config.TreeDurability != nil {
		checkpoints := config.TreeDurability.(*recordingTreeDurability).treeCheckpoints()
		return checkpoints[len(checkpoints)-1].TreeSnapshot()
	}
	tree, err := engine.CaptureTree(t.Context(), process.Relation().RootID())
	if err != nil {
		t.Fatal(err)
	}
	return tree
}
