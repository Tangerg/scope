package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"
	"testing/synctest"
	"time"
)

func TestUnknownResolutionSeparatesAttemptsFromCommittedFacts(t *testing.T) {
	for _, mode := range []string{"ephemeral", "durable", "lost_acknowledgment"} {
		for _, replay := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/replay_%t", mode, replay), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					listener := &recordingEventListener{}
					config := EngineConfig{EventListeners: []EventListener{listener}}
					durability := &inspectionDurability{
						recordingTreeDurability: &recordingTreeDurability{},
						effectKind:              EffectBoundaryResolved,
						entered:                 make(chan inspectionCommit, 1), release: make(chan struct{}),
					}
					if mode != "ephemeral" {
						config.TreeDurability = durability
					}
					if mode == "lost_acknowledgment" {
						durability.failure = errors.New("resolution acknowledgment lost")
					}
					defer durability.unblock()
					engine, err := NewEngine(config)
					if err != nil {
						t.Fatal(err)
					}
					defer mustCloseEngine(t, engine)
					calls := 0
					firstAttemptDone := make(chan struct{})
					dispatcher := replayTestDispatcher{policy: ReplayPolicySameIdentity, dispatch: func(_ context.Context, request EffectRequest) (Settlement, error) {
						calls++
						if calls == 1 {
							time.Sleep(3 * time.Millisecond)
							close(firstAttemptDone)
							return Settlement{}, errors.New("uncertain")
						}
						time.Sleep(7 * time.Millisecond)
						return NewSettlement(request.ID(), SettlementStatusSucceeded, []byte(`{"kind":"result","value":"confirmed"}`))
					}}
					deployment := engineTestDeployment(t, newEngineTestDefinition(t, "engine.effect", "effect"), dispatcher)
					input, err := EncodePayload(engineTestInput{Value: "original"})
					if err != nil {
						t.Fatal(err)
					}
					process, err := engine.Start(t.Context(), deployment, input)
					if err != nil {
						t.Fatal(err)
					}
					<-firstAttemptDone
					synctest.Wait()
					ids := inspectProcessSnapshot(t, process).UnknownEffectIDs()
					if len(ids) != 1 {
						t.Fatal(ids)
					}
					time.Sleep(time.Hour)
					result := make(chan error, 1)
					go func() {
						if replay {
							result <- process.ReplayUnknownEffect(t.Context(), ids[0])
							return
						}
						settlement, settlementErr := NewSettlement(ids[0], SettlementStatusSucceeded, []byte(`{"kind":"result","value":"confirmed"}`))
						if settlementErr != nil {
							result <- settlementErr
							return
						}
						result <- process.ResolveUnknownEffect(t.Context(), settlement)
					}()
					if mode != "ephemeral" {
						<-durability.entered
						for _, event := range listener.snapshot() {
							if event.Name() == EventEffectResolved {
								t.Fatal("resolution published before acknowledgment")
							}
						}
						time.Sleep(50 * time.Millisecond)
						durability.unblock()
					}
					if err := <-result; !errors.Is(err, durability.failure) {
						t.Fatalf("resolution: %v", err)
					}
					if _, err := process.Await(t.Context()); !errors.Is(err, durability.failure) {
						t.Fatalf("await: %v", err)
					}
					if err := process.Join(t.Context()); !errors.Is(err, durability.failure) {
						t.Fatalf("join: %v", err)
					}
					var names []string
					var durations []time.Duration
					var settlements []SettlementStatus
					for _, event := range listener.snapshot() {
						if event.Name() != EventEffectStarted && event.Name() != EventEffectFinished && event.Name() != EventEffectResolved {
							continue
						}
						names = append(names, event.Name())
						encoded, err := json.Marshal(event)
						if err != nil {
							t.Fatal(err)
						}
						var decoded Event
						if err := json.Unmarshal(encoded, &decoded); err != nil {
							t.Fatal(err)
						}
						id, present := decoded.EffectID()
						if !present || id != ids[0] {
							t.Fatal("observation changed Effect identity")
						}
						if fact, ok := decoded.EffectFinished(); ok {
							durations = append(durations, fact.Duration())
							settlements = append(settlements, fact.SettlementStatus())
						}
						if fact, ok := decoded.EffectResolved(); ok {
							if !fact.Valid() || fact.Target() != EffectTargetDispatcher || fact.SettlementStatus() != SettlementStatusSucceeded || decoded.Phase() != EventPhaseCommitted || bytes.Contains(decoded.Payload(), []byte("duration")) {
								t.Fatalf("invalid resolution fact: %s", decoded.Payload())
							}
						}
					}
					wantNames := []string{EventEffectStarted, EventEffectFinished}
					wantDurations := []time.Duration{3 * time.Millisecond}
					wantSettlements := []SettlementStatus{SettlementStatusUnknown}
					wantCalls := 1
					if replay {
						wantNames = append(wantNames, EventEffectStarted, EventEffectFinished)
						wantDurations = append(wantDurations, 7*time.Millisecond)
						wantSettlements = append(wantSettlements, SettlementStatusSucceeded)
						wantCalls++
					}
					if durability.failure == nil {
						wantNames = append(wantNames, EventEffectResolved)
					}
					if !slices.Equal(names, wantNames) || !slices.Equal(durations, wantDurations) || !slices.Equal(settlements, wantSettlements) || calls != wantCalls {
						t.Fatalf("events=%v durations=%v settlements=%v calls=%d", names, durations, settlements, calls)
					}
				})
			})
		}
	}
}
