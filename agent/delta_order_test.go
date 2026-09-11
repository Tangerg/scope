package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
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
			emit(payload)
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
		payload, err := json.Marshal(struct {
			Index int    `json:"index"`
			Text  string `json:"text"`
		}{Index: index, Text: strings.Repeat("x", (index%4)*32*1024)})
		if err != nil {
			t.Fatal(err)
		}
		payloads[index] = payload
	}
	var received []Delta
	engine, err := NewEngine(EngineConfig{
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
	input, err := EncodeInput(engineTestInput{Value: "stream"})
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
		if err := json.Unmarshal(delta.Payload(), &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Index < 0 || payload.Index >= count || seen[payload.Index] ||
			payload.Text != strings.Repeat("x", (payload.Index%4)*32*1024) {
			t.Fatalf("invalid or duplicate payload %d", payload.Index)
		}
		seen[payload.Index] = true
	}
}
