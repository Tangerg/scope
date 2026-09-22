package agent

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
)

type concurrentDeltaDispatcher struct {
	payloads []json.RawMessage
}

func (c concurrentDeltaDispatcher) Dispatch(_ context.Context, request EffectRequest, emit DeltaEmitter) (Settlement, error) {
	start := make(chan struct{})
	var workers sync.WaitGroup
	for _, payload := range c.payloads {
		workers.Go(func() {
			<-start
			if emit != nil {
				emit(payload)
			}
		})
	}
	close(start)
	workers.Wait()
	return NewSettlement(request.ID(), SettlementStatusSucceeded, json.RawMessage(`{"kind":"result","value":"done"}`))
}

func (c concurrentDeltaDispatcher) ReplayPolicy(Effect) ReplayPolicy { return ReplayPolicyNever }

func TestConcurrentDeltaEmitterDeliversIncreasingSequences(t *testing.T) {
	const count = 64
	payloads := make([]json.RawMessage, count)
	for index := range count {
		payload, err := jsonv2.Marshal(struct {
			Index int    `json:"index"`
			Text  string `json:"text"`
		}{Index: index, Text: strings.Repeat("x", (index%4)*32*1024)})
		if err != nil {
			t.Fatal(err)
		}
		payloads[index] = payload
	}
	var received []Delta
	engine, err := NewEngine(EngineConfig{TreeCommitter: NewMemoryTreeCommitter(),
		DeltaBufferCapacity: count,
		DeltaListeners: []DeltaListener{DeltaListenerFunc(func(_ context.Context, delta Delta) {
			received = append(received, delta)
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mustCloseEngine(t, engine) })
	definition := newEngineTestDefinition(t, "engine.effect", "effect")
	deployment := engineTestDeployment(t, definition, concurrentDeltaDispatcher{payloads: payloads})
	input, err := EncodePayload(engineTestInput{Value: "stream"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Run(t.Context(), deployment, input)
	if err != nil || result.Status() != StatusCompleted || result.Usage().DroppedDeltas != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if err := engine.FlushDeltas(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(received) != count {
		t.Fatalf("received %d deltas, want %d", len(received), count)
	}
	seen := make(map[int]bool, count)
	for index, delta := range received {
		if delta.EffectSequence() != uint64(index+1) {
			t.Fatalf("delivery %d has sequence %d", index, delta.EffectSequence())
		}
		var payload struct {
			Index int    `json:"index"`
			Text  string `json:"text"`
		}
		if err := jsonv2.Unmarshal(delta.Payload(), &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Index < 0 || payload.Index >= count || seen[payload.Index] ||
			payload.Text != strings.Repeat("x", (payload.Index%4)*32*1024) {
			t.Fatalf("invalid or duplicate payload %d", payload.Index)
		}
		seen[payload.Index] = true
	}
}

type optionalDeltaDispatcher struct {
	observed bool
}

func (o optionalDeltaDispatcher) Dispatch(_ context.Context, request EffectRequest, emit DeltaEmitter) (Settlement, error) {
	if (emit != nil) != o.observed {
		return Settlement{}, errors.New("unexpected observation admission")
	}
	if emit != nil {
		emit(json.RawMessage(`{"text":"observed"}`))
	}
	return NewSettlement(request.ID(), SettlementStatusSucceeded, json.RawMessage(`{"kind":"result","value":"done"}`))
}

func (o optionalDeltaDispatcher) ReplayPolicy(Effect) ReplayPolicy { return ReplayPolicyNever }

func TestEngineOnlySuppliesEmitterWithListeners(t *testing.T) {
	for _, observed := range []bool{false, true} {
		config := EngineConfig{TreeCommitter: NewMemoryTreeCommitter()}
		var delivered int
		if observed {
			config.DeltaListeners = []DeltaListener{DeltaListenerFunc(func(context.Context, Delta) { delivered++ })}
		}
		engine, err := NewEngine(config)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { mustCloseEngine(t, engine) })
		definition := newEngineTestDefinition(t, "engine.effect", "effect")
		deployment := engineTestDeployment(t, definition, optionalDeltaDispatcher{observed: observed})
		input, err := EncodePayload(engineTestInput{Value: "stream"})
		if err != nil {
			t.Fatal(err)
		}
		result, err := engine.Run(t.Context(), deployment, input)
		if err != nil || result.Status() != StatusCompleted || result.Usage().DroppedDeltas != 0 {
			t.Fatalf("observed=%v result=%+v err=%v", observed, result, err)
		}
		if err := engine.FlushDeltas(t.Context()); err != nil {
			t.Fatal(err)
		}
		if observed && delivered != 1 || !observed && delivered != 0 {
			t.Fatalf("delivered = %d", delivered)
		}
	}
}

type replayDeltaDispatcher struct {
	listenerEntered <-chan struct{}
	calls           int
}

func (r *replayDeltaDispatcher) ReplayPolicy(Effect) ReplayPolicy { return ReplayPolicySameIdentity }
func (r *replayDeltaDispatcher) Dispatch(_ context.Context, request EffectRequest, emit DeltaEmitter) (Settlement, error) {
	r.calls++
	emit(json.RawMessage(`{"text":"first"}`))
	if r.calls == 1 {
		<-r.listenerEntered
	}
	emit(json.RawMessage(`{"text":"second"}`))
	emit(json.RawMessage(`invalid`))
	if r.calls == 1 {
		return Settlement{}, errors.New("unknown first attempt")
	}
	return NewSettlement(request.ID(), SettlementStatusSucceeded, json.RawMessage(`{"kind":"result","value":"done"}`))
}

func TestReplayDeltasCarryAttemptIdentityAcrossSlowDelivery(t *testing.T) {
	for _, recording := range []bool{false, true} {
		t.Run(fmt.Sprintf("recording_%t", recording), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				entered, release := make(chan struct{}), make(chan struct{})
				unblock := sync.OnceFunc(func() { close(release) })
				defer unblock()
				var received []Delta
				var events []Event
				config := EngineConfig{TreeCommitter: NewMemoryTreeCommitter(), DeltaBufferCapacity: 2,
					DeltaListeners: []DeltaListener{DeltaListenerFunc(func(_ context.Context, delta Delta) {
						if len(received) == 0 {
							close(entered)
							<-release
						}
						received = append(received, delta)
					})},
					EventListeners: []EventListener{EventListenerFunc(func(_ context.Context, event Event) { events = append(events, event) })},
				}
				if recording {
					config.TreeCommitter = &recordingTreeCommitter{}
				}
				engine := controlValue(NewEngine(config))
				defer mustCloseEngine(t, engine)
				dispatcher := &replayDeltaDispatcher{listenerEntered: entered}
				deployment := engineTestDeployment(t, newEngineTestDefinition(t, "engine.effect", "effect"), dispatcher)
				process := controlValue(engine.Start(t.Context(), deployment, controlValue(EncodePayload(engineTestInput{Value: "stream"}))))
				synctest.Wait()
				ids := inspectProcessSnapshot(t, process).UnknownEffectIDs()
				if len(ids) != 1 {
					t.Fatalf("unknown effects=%v", ids)
				}
				if err := process.ReplayUnknownEffect(t.Context(), ids[0]); err != nil {
					t.Fatal(err)
				}
				result := mustAwait(t, process)
				unblock()
				if err := engine.FlushDeltas(t.Context()); err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				if len(received) != 3 || result.Usage().DroppedDeltas != 3 {
					t.Fatalf("delivered=%d dropped=%d", len(received), result.Usage().DroppedDeltas)
				}
				first, second := received[0].AttemptID(), received[2].AttemptID()
				if !first.Valid() || !second.Valid() || first == second || received[1].AttemptID() != first {
					t.Fatal("replay identity is ambiguous")
				}
				for index, delta := range received {
					if delta.EffectID() != ids[0] || delta.EffectSequence() != []uint64{1, 2, 1}[index] {
						t.Fatalf("delta=%+v", delta)
					}
					data, err := jsonv2.Marshal(delta)
					if err != nil {
						t.Fatal(err)
					}
					var decoded Delta
					if err := jsonv2.Unmarshal(data, &decoded); err != nil || decoded.AttemptID() != delta.AttemptID() {
						t.Fatalf("roundtrip: %v", err)
					}
				}
				starts, finishes, drops := map[EffectAttemptID]int{}, map[EffectAttemptID]int{}, map[EffectAttemptID]uint64{}
				for _, event := range events {
					if fact, ok := event.EffectStarted(); ok {
						starts[fact.AttemptID()]++
					}
					if fact, ok := event.EffectFinished(); ok {
						finishes[fact.AttemptID()]++
					}
					if fact, ok := event.DeltaDropped(); ok {
						drops[fact.AttemptID()] += fact.Count()
					}
				}
				if starts[first] != 1 || starts[second] != 1 || finishes[first] != 1 || finishes[second] != 1 || drops[first] != 1 || drops[second] != 2 {
					t.Fatalf("starts=%v finishes=%v drops=%v", starts, finishes, drops)
				}
			})
		})
	}
}
