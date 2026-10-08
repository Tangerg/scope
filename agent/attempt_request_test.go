package agent

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
)

func TestDispatchRequestCorrelatesPhysicalAttemptsWithoutPersistingThem(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		listener := &recordingEventListener{}
		engine := controlValue(NewEngine(EngineConfig{
			TreeCommitter: NewMemoryTreeCommitter(), EventListeners: []EventListener{listener},
		}))
		defer mustCloseEngine(t, engine)
		var mu sync.Mutex
		var requests []EffectRequest
		dispatcher := replayTestDispatcher{
			policy: ReplayPolicySameIdentity,
			dispatch: func(_ context.Context, request EffectRequest) (Settlement, error) {
				mu.Lock()
				requests = append(requests, request)
				count := len(requests)
				mu.Unlock()
				if count == 1 {
					return Settlement{}, errors.New("execution result unavailable")
				}
				return NewSettlement(SettlementStatusSucceeded, []byte(`{"kind":"result","value":"confirmed"}`))
			},
		}
		deployment := engineTestDeployment(t, newEngineTestDefinition(t, "engine.effect", "effect"), dispatcher)
		process := controlValue(engine.Start(t.Context(), deployment, controlValue(EncodePayload(engineTestInput{Value: "input"}))))
		synctest.Wait()
		mu.Lock()
		if len(requests) != 1 {
			count := len(requests)
			mu.Unlock()
			t.Fatalf("initial dispatch count = %d", count)
		}
		first := requests[0]
		mu.Unlock()
		firstAttempt, present := first.AttemptID()
		if !present || !firstAttempt.Valid() {
			t.Fatal("actual dispatch has no physical identity")
		}
		clonedAttempt, present := first.clone().AttemptID()
		if !present || clonedAttempt != firstAttempt {
			t.Fatal("request cloning changed physical identity")
		}
		snapshot := controlValue(engine.CaptureTree(t.Context(), process.Relation().ProcessID()))
		retained, present := snapshot.EffectRequest(process.Relation().ProcessID(), first.ID())
		if !present || !retained.Valid() {
			t.Fatal("unknown logical request was not retained")
		}
		if attempt, active := retained.AttemptID(); active || attempt.Valid() {
			t.Fatal("snapshot manufactured an active invocation")
		}
		if bytes.Contains(snapshot.JSON(), []byte(firstAttempt.String())) {
			t.Fatal("physical attempt leaked into recovery state")
		}
		if err := process.ReplayUnknownEffect(t.Context(), first.ID()); err != nil {
			t.Fatal(err)
		}
		result := controlValue(process.Await(t.Context()))
		if err := process.Join(t.Context()); err != nil {
			t.Fatal(err)
		}
		if result.Termination().Status() != StatusCompleted {
			t.Fatalf("status = %s", result.Termination().Status())
		}
		mu.Lock()
		captured := append([]EffectRequest(nil), requests...)
		mu.Unlock()
		if len(captured) != 2 || captured[1].ID() != first.ID() {
			t.Fatalf("replay requests = %+v", captured)
		}
		secondAttempt, present := captured[1].AttemptID()
		if !present || secondAttempt == firstAttempt {
			t.Fatal("replay reused a physical invocation identity")
		}
		var started, finished []EffectAttemptID
		for _, event := range listener.snapshot() {
			if id, ok := event.EffectID(); !ok || id != first.ID() {
				continue
			}
			if fact, ok := event.EffectStarted(); ok {
				started = append(started, fact.AttemptID())
			}
			if fact, ok := event.EffectFinished(); ok {
				finished = append(finished, fact.AttemptID())
			}
		}
		if len(started) != 2 || len(finished) != 2 || started[0] != firstAttempt ||
			finished[0] != firstAttempt || started[1] != secondAttempt || finished[1] != secondAttempt {
			t.Fatalf("request/event attribution: started=%v finished=%v", started, finished)
		}
	})
}
