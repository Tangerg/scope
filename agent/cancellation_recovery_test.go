package agent

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
)

func TestCancellationRevokesAcknowledgedButUnusedDispatchPermission(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		durability := &blockingPendingEffectDurability{
			recordingTreeDurability: &recordingTreeDurability{},
			entered:                 make(chan struct{}), release: make(chan struct{}),
		}
		release := sync.OnceFunc(func() { close(durability.release) })
		defer release()
		dispatcher := &engineTestDispatcher{policy: ReplayPolicySameIdentity}
		definition := newEngineTestDefinition(t, "engine.effect", "effect")
		deployment := engineTestDeployment(t, definition, dispatcher)
		engine, err := NewEngine(EngineConfig{TreeDurability: durability})
		if err != nil {
			t.Fatal(err)
		}
		input, _ := EncodeInput(engineTestInput{Value: "never dispatched"})
		process, err := engine.Start(t.Context(), deployment, input)
		if err != nil {
			t.Fatal(err)
		}
		<-durability.entered
		canceled := make(chan error, 1)
		go func() { canceled <- process.Kill(t.Context(), "revoke unused permission") }()
		synctest.Wait()
		release()
		if killErr := <-canceled; killErr != nil {
			t.Fatal(killErr)
		}
		result := mustAwait(t, process)
		if result.Status() != StatusKilled || len(result.Termination().UnresolvedEffectIDs()) != 0 {
			t.Fatalf("unused permission became uncertain: %+v", result.Termination())
		}
		if calls := dispatcher.calls.Load(); calls != 0 {
			t.Errorf("revoked permission dispatched %d calls", calls)
		}
		wire, err := inspectProcessSnapshot(t, process).wire()
		if err != nil {
			t.Fatal(err)
		}
		if wire.Prepared == nil || wire.Prepared.Effects[0].Phase != effectPhasePlanned || wire.Prepared.Effects[0].Settlement != nil {
			t.Errorf("unused dispatch evidence = %+v", wire.Prepared)
		}
		boundaries := durability.effectBoundaries()
		if len(boundaries) != 1 || boundaries[0].Kind() != EffectBoundaryPending {
			t.Errorf("unused permission created a settlement: %+v", boundaries)
		}
		mustCloseEngine(t, engine)
	})
}

func TestRestoredCancellationNeverReplaysAnUncertainDispatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		durability := &recordingTreeDurability{}
		dispatcher := &cancellationDispatcher{
			entered: make(chan EffectRequest, 1), canceled: make(chan struct{}),
			release: make(chan struct{}), status: SettlementStatusSucceeded,
		}
		release := sync.OnceFunc(func() { close(dispatcher.release) })
		defer release()
		definition := newEngineTestDefinition(t, "engine.effect", "effect")
		deployment := engineTestDeployment(t, definition, dispatcher)
		engine, err := NewEngine(EngineConfig{TreeDurability: durability})
		if err != nil {
			t.Fatal(err)
		}
		input, _ := EncodeInput(engineTestInput{Value: "recover cancellation"})
		process, err := engine.Start(t.Context(), deployment, input)
		if err != nil {
			t.Fatal(err)
		}
		request := <-dispatcher.entered
		if killErr := process.Kill(t.Context(), "canceled before runtime replacement"); killErr != nil {
			t.Fatal(killErr)
		}
		<-dispatcher.canceled
		signalID, _ := ParseSignalID("signal:record-cancellation-cut")
		signal, _ := NewSignalRequest(signalID, WaitID{}, json.RawMessage(`{"retained":true}`))
		if accepted, deliveryErr := process.DeliverSignals(t.Context(), signal); deliveryErr != nil || !accepted {
			t.Fatalf("input admission = %t, %v", accepted, deliveryErr)
		}
		checkpoints := durability.treeCheckpoints()
		snapshot := checkpoints[len(checkpoints)-1].TreeSnapshot()
		wire, err := snapshot.ProcessSnapshots()[0].wire()
		if err != nil {
			t.Fatal(err)
		}
		if wire.PendingControl.KillReason == "" || wire.Prepared == nil || wire.Prepared.Effects[0].Phase != effectPhasePending {
			t.Fatalf("missing pending cancellation boundary: %+v", wire)
		}
		release()
		_ = mustAwait(t, process)
		mustCloseEngine(t, engine)
		for _, policy := range []ReplayPolicy{ReplayPolicyNever, ReplayPolicySameIdentity} {
			recoveredDispatcher := &cancellationRecoveryDispatcher{policy: policy}
			recoveredDurability := &recordingTreeDurability{}
			recoveredEngine, err := NewEngine(EngineConfig{TreeDurability: recoveredDurability})
			if err != nil {
				t.Fatal(err)
			}
			recovered, err := recoveredEngine.RestoreTree(t.Context(), engineTestDeployment(t, definition, recoveredDispatcher), snapshot)
			if err != nil {
				t.Fatal(err)
			}
			result := mustAwait(t, recovered)
			unresolved := result.Termination().UnresolvedEffectIDs()
			if result.Status() != StatusKilled || len(unresolved) != 1 || unresolved[0] != request.ID() {
				t.Fatalf("recovered termination = %+v", result.Termination())
			}
			if recoveredDispatcher.calls.Load() != 0 || recoveredDispatcher.queries.Load() != 0 {
				t.Errorf("canceled recovery called Dispatcher: calls=%d policy=%d", recoveredDispatcher.calls.Load(), recoveredDispatcher.queries.Load())
			}
			boundaries := recoveredDurability.effectBoundaries()
			if len(boundaries) != 1 || boundaries[0].Kind() != EffectBoundarySettled {
				t.Fatalf("recovery boundaries = %+v", boundaries)
			}
			settlement, _ := boundaries[0].Settlement()
			if settlement.Status() != SettlementStatusUnknown || settlement.EffectID() != request.ID() {
				t.Errorf("recovery settlement = %+v", settlement)
			}
			mustCloseEngine(t, recoveredEngine)
		}
	})
}

type blockingPendingEffectDurability struct {
	*recordingTreeDurability
	entered chan struct{}
	release chan struct{}
}

func (b *blockingPendingEffectDurability) CommitEffect(ctx context.Context, boundary EffectBoundary) error {
	if boundary.Kind() == EffectBoundaryPending {
		close(b.entered)
		<-b.release
	}
	return b.recordingTreeDurability.CommitEffect(ctx, boundary)
}

type cancellationRecoveryDispatcher struct {
	policy  ReplayPolicy
	calls   atomic.Uint32
	queries atomic.Uint32
}

func (c *cancellationRecoveryDispatcher) Dispatch(_ context.Context, request EffectRequest, _ DeltaEmitter) (Settlement, error) {
	c.calls.Add(1)
	return NewSettlement(request.ID(), SettlementStatusSucceeded, json.RawMessage(`{}`))
}

func (c *cancellationRecoveryDispatcher) ReplayPolicy(Effect) ReplayPolicy {
	c.queries.Add(1)
	return c.policy
}
