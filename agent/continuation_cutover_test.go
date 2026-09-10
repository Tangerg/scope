package agent_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/coordination"
)

func TestEpisodeCutoverRetainsLateInputAndRecipientAfterSuccessorStart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newEpisodeStore()
		schema, err := agent.SchemaFor[episodeState]()
		if err != nil {
			t.Fatal(err)
		}
		gate, err := coordination.NewInputGate(coordination.InputGateConfig{
			Name: "example.episode.input", Description: "Receive the completed domain revision.", RequestSchema: schema, AnswerSchema: schema,
		})
		if err != nil {
			t.Fatal(err)
		}
		answerID, err := agent.ParseSignalID("signal:episode-answer")
		if err != nil {
			t.Fatal(err)
		}
		barrier := &episodeAnswerBarrier{Definition: gate, answerID: answerID, entered: make(chan struct{}), release: make(chan struct{})}
		binding, err := episodeBinding(barrier)
		if err != nil {
			t.Fatal(err)
		}
		engine, err := agent.NewEngine(agent.EngineConfig{TreeDurability: store.trees})
		if err != nil {
			t.Fatal(err)
		}
		initial, err := agent.EncodeInput(episodeState{Summary: "initial state"})
		if err != nil {
			t.Fatal(err)
		}
		previous, err := engine.Start(t.Context(), binding, initial)
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		tree, err := engine.InspectTree(t.Context(), previous.ID())
		if err != nil {
			t.Fatal(err)
		}
		fact, present := tree.Process(previous.ID())
		if !present {
			t.Fatal("previous episode is missing")
		}
		waitID, waiting := fact.Snapshot.WaitID()
		if !waiting {
			t.Fatal("previous episode did not wait")
		}
		payload, err := agent.EncodeInput(episodeState{Revision: 1, Summary: "explicit state"})
		if err != nil {
			t.Fatal(err)
		}
		answer, err := agent.NewSignalRequest(answerID, waitID, payload.JSON())
		if err != nil {
			t.Fatal(err)
		}
		if bindErr := store.bindInput(previous, answer); bindErr != nil {
			t.Fatal(bindErr)
		}
		if accepted, deliveryErr := previous.DeliverSignals(t.Context(), answer); deliveryErr != nil || !accepted {
			t.Fatalf("answer admission=%t %v", accepted, deliveryErr)
		}
		if ackErr := store.acknowledgeInput(previous.ID(), answer.ID()); ackErr != nil {
			t.Fatal(ackErr)
		}
		<-barrier.entered
		lateID, err := agent.ParseSignalID("signal:late-episode-input")
		if err != nil {
			t.Fatal(err)
		}
		late, err := agent.NewSignalRequest(lateID, agent.WaitID{}, []byte(`{"revision":99,"summary":"keep with original recipient"}`))
		if err != nil {
			t.Fatal(err)
		}
		if bindErr := store.bindInput(previous, late); bindErr != nil {
			t.Fatal(bindErr)
		}
		if accepted, deliveryErr := previous.DeliverSignals(t.Context(), late); deliveryErr != nil || !accepted {
			t.Fatalf("late admission=%t %v", accepted, deliveryErr)
		}
		outsideID, err := agent.ParseSignalID("signal:unsubmitted-episode-input")
		if err != nil {
			t.Fatal(err)
		}
		outside, err := agent.NewSignalRequest(outsideID, agent.WaitID{}, []byte(`"still owned by ingress"`))
		if err != nil {
			t.Fatal(err)
		}
		if bindErr := store.bindInput(previous, outside); bindErr != nil {
			t.Fatal(bindErr)
		}
		// The receiver has acknowledged the late input, but its response has not
		// reached the ingress record. The final Step cannot have consumed it.
		close(barrier.release)
		result, err := store.sealEpisode(t.Context(), previous)
		if err != nil {
			t.Fatal(err)
		}
		if store.inputs[answerID].disposition != episodeInputConsumed || store.inputs[lateID].disposition != episodeInputRetained || store.inputs[lateID].acknowledged || store.inputs[outsideID].disposition != episodeInputNotAdmitted {
			t.Fatal("input cutover lost an input owner or consumption boundary")
		}
		output, present := result.Output()
		if !present {
			t.Fatal("old episode has no output")
		}
		consumed, err := output.Decode[agent.Signal]()
		if err != nil {
			t.Fatal(err)
		}
		transfer, err := agent.ParseInput(consumed.Payload())
		if err != nil {
			t.Fatal(err)
		}
		deployment, err := episodeDeployment()
		if err != nil {
			t.Fatal(err)
		}
		request := successorRequest{
			Predecessor: previous.ID(), DeploymentRef: deployment.DeploymentRef(), Input: transfer,
			Limits: agent.Limits{MaxSteps: 8, MaxEffects: 4, MaxSignals: 8, MaxPendingSignals: 8}, TreeLimits: agent.DefaultTreeLimits(),
		}
		host := &episodeHost{store: store}
		next, err := host.start(t.Context(), deployment, request)
		if err != nil {
			t.Fatal(err)
		}
		assertEpisodeResult(t, next, request, 2)
		if ackErr := store.acknowledgeInput(previous.ID(), lateID); ackErr != nil {
			t.Fatal(ackErr)
		}
		if record := store.inputs[lateID]; record.recipient != previous.ID() || record.disposition != episodeInputRetained || !record.acknowledged {
			t.Fatal("late acknowledgment changed its original recipient or disposition")
		}
		if bindErr := store.bindInput(next, late); !errors.Is(bindErr, agent.ErrSignalConflict) {
			t.Fatalf("same identity retarget=%v", bindErr)
		}
		if ackErr := store.acknowledgeInput(next.ID(), lateID); !errors.Is(ackErr, agent.ErrSignalConflict) {
			t.Fatalf("foreign late acknowledgment=%v", ackErr)
		}
		freshID, err := agent.ParseSignalID("signal:after-ingress-seal")
		if err != nil {
			t.Fatal(err)
		}
		fresh, err := agent.NewSignalRequest(freshID, agent.WaitID{}, []byte(`"next request"`))
		if err != nil {
			t.Fatal(err)
		}
		if bindErr := store.bindInput(previous, fresh); !errors.Is(bindErr, errUnsafeEpisodeBoundary) {
			t.Fatalf("old ingress reopened=%v", bindErr)
		}
		if result.Usage() != (agent.Usage{CommittedSteps: 3, PreparedEffects: 1, AcceptedSignals: 3}) {
			t.Fatalf("old budget changed=%+v", result.Usage())
		}
		head, present, loadErr := store.trees.LoadTree(t.Context(), previous.ID())
		if loadErr != nil || !present {
			t.Fatalf("old retained tree=%t %v", present, loadErr)
		}
		restoredEngine, err := agent.NewEngine(agent.EngineConfig{TreeDurability: store.trees})
		if err != nil {
			t.Fatal(err)
		}
		restored, err := restoredEngine.RestoreTree(t.Context(), binding, head)
		if err != nil {
			t.Fatal(err)
		}
		if _, sealErr := store.sealEpisode(t.Context(), restored); sealErr != nil {
			t.Fatal(sealErr)
		}
		retained := store.sealed[previous.ID()].ProcessSnapshots()[0].SignalReceipts()
		if len(retained) != 3 || !retained[2].Matches(late) || retained[2].Consumed() {
			t.Fatal("retention erased the old admitted input")
		}
		if pending, ok := retained[2].PendingSignal(); !ok || string(pending.Payload()) != string(late.Payload()) {
			t.Fatal("retained input bytes changed")
		}
		if store.allocations != 1 || store.starts != 1 {
			t.Fatal("late confirmation admitted another successor")
		}
		for _, closeErr := range []error{host.close(t.Context()), engine.Close(t.Context()), restoredEngine.Close(t.Context())} {
			if closeErr != nil {
				t.Fatal(closeErr)
			}
		}
	})
}

type episodeAnswerBarrier struct {
	agent.Definition
	answerID agent.SignalID
	entered  chan struct{}
	release  chan struct{}
	held     atomic.Bool
}

func (e *episodeAnswerBarrier) Start(input agent.Input) (agent.Execution, error) {
	execution, err := e.Definition.Start(input)
	if err != nil {
		return nil, err
	}
	return &episodeAnswerExecution{Execution: execution, owner: e}, nil
}

func (e *episodeAnswerBarrier) Restore(state agent.ExecutionState) (agent.Execution, error) {
	execution, err := e.Definition.Restore(state)
	if err != nil {
		return nil, err
	}
	return &episodeAnswerExecution{Execution: execution, owner: e}, nil
}

type episodeAnswerExecution struct {
	agent.Execution
	owner *episodeAnswerBarrier
}

func (e *episodeAnswerExecution) Step(ctx context.Context, signals []agent.Signal) (agent.Transition, error) {
	if len(signals) > 0 && signals[0].ID() == e.owner.answerID && e.owner.held.CompareAndSwap(false, true) {
		close(e.owner.entered)
		select {
		case <-e.owner.release:
		case <-ctx.Done():
			return agent.Transition{}, ctx.Err()
		}
	}
	return e.Execution.Step(ctx, signals)
}
