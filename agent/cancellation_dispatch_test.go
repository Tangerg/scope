package agent

import (
	"bytes"
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Tangerg/scope/agent/internal/jsonwire"
)

func TestTerminationCancelsDispatchAndStopsPreparedBatch(t *testing.T) {
	for _, recording := range []bool{false, true} {
		for _, status := range []SettlementStatus{SettlementStatusSucceeded, SettlementStatusFailed, SettlementStatusUnknown} {
			name := "memory"
			if recording {
				name = "recording"
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
					config := EngineConfig{TreeCommitter: NewMemoryTreeCommitter()}
					if recording {
						config.TreeCommitter = &recordingTreeCommitter{}
					}
					engine, err := NewEngine(config)
					if err != nil {
						t.Fatal(err)
					}
					definition := newEngineTestDefinition(t, "engine.batch", "batch")
					deployment := engineTestDeployment(t, definition, dispatcher)
					input, _ := EncodePayload(engineTestInput{Value: "cancel batch"})
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
					if result.Termination().Status() != StatusKilled {
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
					if !recording && status == SettlementStatusUnknown {
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
					if settled.phase() != effectPhaseSettled || settled.settlement().Status() != status ||
						planned.phase() != effectPhasePlanned || planned.settlement() != nil {
						t.Errorf("interrupted Effects = %+v", wire.Prepared.Effects)
					}
					state, err := jsonwire.Decode[engineTestState](wire.CommittedExecutionState.Payload())
					if err != nil || state.Phase != "ready" || wire.CommittedSteps != 0 ||
						wire.Mailbox.SignalCursor != 0 || wire.usage().AcceptedSignals != 0 || wire.usage().PreparedEffects != 2 {
						t.Errorf("interrupted candidate changed committed facts: state=%+v usage=%+v cursor=%d error=%v", state, wire.usage(), wire.Mailbox.SignalCursor, err)
					}
					if status != SettlementStatusUnknown && string(settled.settlement().Payload()) != `{"done":true}` {
						t.Errorf("settlement payload = %s", settled.settlement().Payload())
					}
					tree := interruptedTreeSnapshot(t, engine, process, config)
					// Preparing the batch asked for each Effect's policy; restoring
					// terminal evidence must not ask again.
					preparedQueries := dispatcher.replayQueries.Load()
					restoredEngine, err := NewEngine(config)
					if err != nil {
						t.Fatal(err)
					}
					restored, err := restoredEngine.RestoreTree(t.Context(), deployment, tree)
					if err != nil {
						t.Fatal(err)
					}
					if got := mustAwait(t, restored); got.Termination().Status() != result.Termination().Status() || got.Usage() != result.Usage() {
						t.Fatalf("restored result = %+v, want %+v", got, result)
					}
					resolution, _ := NewSettlement(SettlementStatusSucceeded, json.RawMessage(`{}`))
					for _, terminal := range []*Process{process, restored} {
						if err := terminal.ResolveUnknownEffect(t.Context(), settled.ID, resolution); !errors.Is(err, ErrProcessFinished) {
							t.Errorf("terminal resolution error = %v", err)
						}
						if current := inspectProcessSnapshot(t, terminal); !bytes.Equal(current.JSON(), snapshot.JSON()) {
							t.Errorf("terminal evidence changed after restoration or adjudication: %s", current.JSON())
						}
					}
					if got := dispatcher.replayQueries.Load() - preparedQueries; got != 0 {
						t.Errorf("terminal restoration queried Effect policy %d times", got)
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
				engine, err := NewEngine(EngineConfig{TreeCommitter: NewMemoryTreeCommitter()})
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(t.Context(), time.Second)
				defer cancel()
				deployment := engineTestDeployment(t, newEngineTestDefinition(t, "engine.effect", "effect"), dispatcher)
				input, _ := EncodePayload(engineTestInput{Value: "host termination"})
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
				if result.Termination().Status() != wantStatus || result.Termination().Cause() != wantCause {
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
			wire.Prepared.Effects[0].progress = &effectProgress{}
		},
	} {
		wire, err := snapshot.wire()
		if err != nil {
			t.Fatal(err)
		}
		change(&wire)
		data, err := jsonv2.Marshal(wire)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := parseTestProcessSnapshot(data); !errors.Is(err, ErrInvalidSnapshot) {
			t.Errorf("%s accepted: %v", name, err)
		}
	}
	// The prepared Effects own the unresolved identities; the committed
	// termination has no member that could store a copy.
	var document map[string]any
	if err := jsonv2.Unmarshal(snapshot.JSON(), &document); err != nil {
		t.Fatal(err)
	}
	termination := document["finish"].(map[string]any)["termination"].(map[string]any)
	wire := controlValue(snapshot.wire())
	termination["unresolved_effect_ids"] = []string{wire.Prepared.Effects.unknownEffectIDs()[0].String()}
	if _, err := parseTestProcessSnapshot(controlValue(jsonv2.Marshal(document))); !errors.Is(err, ErrInvalidSnapshot) {
		t.Errorf("stored termination uncertainty accepted: %v", err)
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
		return NewSettlement(SettlementStatusSucceeded, json.RawMessage(`{"done":true}`))
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
	return NewSettlement(c.status, json.RawMessage(`{"done":true}`))
}

func (c *cancellationDispatcher) Policy(Effect) EffectPolicy {
	c.replayQueries.Add(1)
	return EffectPolicy{Replay: ReplayPolicySameIdentity}
}

func interruptedTreeSnapshot(t *testing.T, engine *Engine, process *Process, config EngineConfig) TreeSnapshot {
	t.Helper()

	tree, err := engine.CaptureTree(t.Context(), process.Relation().RootID())
	if err != nil {
		t.Fatal(err)
	}
	return tree
}
