package coordination_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/agenttest"
	"github.com/Tangerg/scope/agent/coordination"
)

func TestTimerRejectsNilContextBeforeProtocolValidation(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("nil context became a protocol result")
		}
	}()
	var nilContext context.Context
	_, _ = (coordination.Timer{}).Dispatch(nilContext, agent.EffectRequest{}, nil)
}

func TestDeadlineRestoresTheSameAbsoluteTimerAndEffectIdentity(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := time.Now()
		deadline := started.Add(10 * time.Second)
		dispatcher := &recordingTimer{}
		deployment := deadlineBinding(t, dispatcher)
		store := agenttest.NewMemoryTreeDurability()
		engine, err := agent.NewEngine(agent.EngineConfig{TreeDurability: store})
		if err != nil {
			t.Fatal(err)
		}
		process, err := engine.Start(t.Context(), deployment, encodedInput(t, deadline))
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		pending, found, loadErr := store.LoadTree(t.Context(), process.ID())
		if loadErr != nil || !found || len(dispatcher.identities()) != 1 {
			t.Fatalf("pending timer exists=%t calls=%d error=%v", found, len(dispatcher.identities()), loadErr)
		}
		time.Sleep(4 * time.Second)
		restoredEngine, err := agent.NewEngine(agent.EngineConfig{TreeDurability: store})
		if err != nil {
			t.Fatal(err)
		}
		restored, err := restoredEngine.RestoreTree(t.Context(), deployment, pending)
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		identities := dispatcher.identities()
		if len(identities) != 2 || identities[0] != identities[1] {
			t.Fatalf("timer replay changed identity: %v", identities)
		}
		if killErr := process.Kill(t.Context(), "retire previous timer writer"); killErr != nil {
			t.Fatal(killErr)
		}
		if _, staleErr := process.Await(t.Context()); !errors.Is(staleErr, agent.ErrTreeIncarnationConflict) {
			t.Fatalf("retired timer writer = %v", staleErr)
		}
		if got := completedOutput[time.Time](t, restored); !got.Equal(deadline) || !time.Now().Equal(deadline) {
			t.Fatalf("restored timer reached %v at %v, want original deadline %v", got, time.Now(), deadline)
		}
		if usage := result(t, restored).Usage(); usage != (agent.Usage{CommittedSteps: 2, PreparedEffects: 1, AcceptedSignals: 1}) {
			t.Fatalf("timer replay charged work again: %+v", usage)
		}
		if joinErr := restored.Join(t.Context()); joinErr != nil {
			t.Fatal(joinErr)
		}
		closeEngine(t, restoredEngine)
		closeEngine(t, engine)
	})
}

func TestDeadlineCancellationStopsOwnedTimerWithoutAdvancingTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := time.Now()
		dispatcher := &recordingTimer{}
		deployment := deadlineBinding(t, dispatcher)
		engine, err := agent.NewEngine(agent.EngineConfig{})
		if err != nil {
			t.Fatal(err)
		}
		process, err := engine.Start(t.Context(), deployment, encodedInput(t, started.Add(time.Hour)))
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if len(dispatcher.identities()) != 1 {
			t.Fatal("timer has not entered its owned call")
		}
		if cancelErr := process.RequestCancellation(t.Context(), "replace deadline"); cancelErr != nil {
			t.Fatal(cancelErr)
		}
		if joinErr := process.Join(t.Context()); joinErr != nil {
			t.Fatal(joinErr)
		}
		termination := result(t, process).Termination()
		if termination.Status() != agent.StatusCanceled || len(termination.UnresolvedEffectIDs()) != 0 || !time.Now().Equal(started) {
			t.Fatalf("timer cancellation = %+v at %v", termination, time.Now())
		}
		closeEngine(t, engine)
	})
}

func TestDeadlineAcceptsPastInstantAndRejectsZero(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		deployment := deadlineBinding(t, coordination.Timer{})
		if _, err := deployment.Definition().Start(encodedInput(t, time.Time{})); !errors.Is(err, agent.ErrInvalidInput) {
			t.Fatalf("zero deadline = %v", err)
		}
		engine, err := agent.NewEngine(agent.EngineConfig{})
		if err != nil {
			t.Fatal(err)
		}
		past := time.Now().Add(-time.Hour)
		process, err := engine.Start(t.Context(), deployment, encodedInput(t, past))
		if err != nil {
			t.Fatal(err)
		}
		if got := completedOutput[time.Time](t, process); !got.Equal(past) {
			t.Fatalf("expired deadline changed to %v", got)
		}
		closeEngine(t, engine)
	})
}

func TestDeadlineDefinitionConformance(t *testing.T) {
	deployment := deadlineBinding(t, coordination.Timer{})
	agenttest.RunDefinitionConformance(t, agenttest.DefinitionConformanceConfig{
		Definition: deployment.Definition(), Input: encodedInput(t, time.Date(2026, time.September, 9, 12, 0, 0, 0, time.UTC)),
	})
}

type recordingTimer struct {
	mu       sync.Mutex
	requests []agent.EffectID
}

func (r *recordingTimer) ReplayPolicy(effect agent.Effect) agent.ReplayPolicy {
	return (coordination.Timer{}).ReplayPolicy(effect)
}

func (r *recordingTimer) Dispatch(ctx context.Context, request agent.EffectRequest, emit agent.DeltaEmitter) (agent.Settlement, error) {
	r.mu.Lock()
	r.requests = append(r.requests, request.ID())
	r.mu.Unlock()
	return (coordination.Timer{}).Dispatch(ctx, request, emit)
}

func (r *recordingTimer) identities() []agent.EffectID {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]agent.EffectID{}, r.requests...)
}
