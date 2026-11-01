package run_test

import (
	startrand "crypto/rand"

	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/Tangerg/scope/agent/coordination"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/jsonschema"
)

func TestEpisodeCutoverLeavesRejectedInputWithIngress(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newEpisodeStore()
		schema, err := jsonschema.For[episodeState]()
		if err != nil {
			t.Fatal(err)
		}
		gate, err := coordination.NewInputGate(coordination.InputGateConfig{
			Name: "example.episode.input", Description: "Receive the completed domain revision.", RequestSchema: schema, AnswerSchema: schema,
		})
		if err != nil {
			t.Fatal(err)
		}
		answerID, err := run.ParseSignalID("signal:episode-answer")
		if err != nil {
			t.Fatal(err)
		}
		barrier := &episodeAnswerBarrier{Definition: gate, answerID: answerID, entered: make(chan struct{}), release: make(chan struct{})}
		binding, err := episodeBinding(barrier)
		if err != nil {
			t.Fatal(err)
		}
		engine, err := run.New(t.Context(), run.Config{Store: store.trees})
		if err != nil {
			t.Fatal(err)
		}
		initial, err := run.EncodePayload(episodeState{Summary: "initial state"})
		if err != nil {
			t.Fatal(err)
		}
		previous, err := engine.Start(t.Context(), binding, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "test", Key: startrand.Text()}, Input: initial})
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		tree, err := engine.InspectTree(t.Context(), previous.Relation().ProcessID())
		if err != nil {
			t.Fatal(err)
		}
		fact, present := tree.Process(previous.Relation().ProcessID())
		if !present {
			t.Fatal("previous episode is missing")
		}
		waitID, waiting := fact.Snapshot.WaitID()
		if !waiting {
			t.Fatal("previous episode did not wait")
		}
		payload, err := run.EncodePayload(episodeState{Revision: 1, Summary: "explicit state"})
		if err != nil {
			t.Fatal(err)
		}
		answer, err := run.NewSignalRequest(answerID, waitID, payload.JSON())
		if err != nil {
			t.Fatal(err)
		}
		if bindErr := store.bindInput(previous, answer); bindErr != nil {
			t.Fatal(bindErr)
		}
		if accepted, deliveryErr := previous.DeliverSignals(t.Context(), answer); deliveryErr != nil || !accepted {
			t.Fatalf("answer admission=%t %v", accepted, deliveryErr)
		}
		if ackErr := store.acknowledgeInput(previous.Relation().ProcessID(), answer.ID()); ackErr != nil {
			t.Fatal(ackErr)
		}
		<-barrier.entered
		lateID, err := run.ParseSignalID("signal:late-episode-input")
		if err != nil {
			t.Fatal(err)
		}
		late, err := run.NewSignalRequest(lateID, run.WaitID{}, []byte(`{"revision":99,"summary":"keep with original recipient"}`))
		if err != nil {
			t.Fatal(err)
		}
		if bindErr := store.bindInput(previous, late); bindErr != nil {
			t.Fatal(bindErr)
		}
		if accepted, deliveryErr := previous.DeliverSignals(t.Context(), late); accepted || !errors.Is(deliveryErr, run.ErrSignalRejected) {
			t.Fatalf("late admission=%t %v", accepted, deliveryErr)
		}
		outsideID, err := run.ParseSignalID("signal:unsubmitted-episode-input")
		if err != nil {
			t.Fatal(err)
		}
		outside, err := run.NewSignalRequest(outsideID, run.WaitID{}, []byte(`"still owned by ingress"`))
		if err != nil {
			t.Fatal(err)
		}
		if bindErr := store.bindInput(previous, outside); bindErr != nil {
			t.Fatal(bindErr)
		}
		// Rejected input remains owned by ingress across the final Step.
		close(barrier.release)
		result, err := store.sealEpisode(t.Context(), previous)
		if err != nil {
			t.Fatal(err)
		}
		if store.inputs[answerID].disposition != episodeInputConsumed || store.inputs[lateID].disposition != episodeInputNotAdmitted || store.inputs[lateID].acknowledged || store.inputs[outsideID].disposition != episodeInputNotAdmitted {
			t.Fatal("input cutover lost an input owner or consumption boundary")
		}
		output, present := result.Termination().Output()
		if !present {
			t.Fatal("old episode has no output")
		}
		consumed, err := output.Decode[run.Signal]()
		if err != nil {
			t.Fatal(err)
		}
		transfer, err := run.ParsePayload(consumed.Payload())
		if err != nil {
			t.Fatal(err)
		}
		deployment, err := episodeDeployment()
		if err != nil {
			t.Fatal(err)
		}
		treeLimits := run.DefaultTreeLimits()
		treeLimits.MaxPendingSignals = 8
		request := successorRequest{
			Predecessor: previous.Relation().ProcessID(), ProgramRef: deployment.ProgramRef(), Input: transfer,
			Budget: run.Budget{Steps: run.NewQuota(8), Effects: run.NewQuota(4), Signals: run.NewQuota(8)}, TreeLimits: treeLimits,
		}
		host := &episodeHost{store: store}
		next, err := host.start(t.Context(), deployment, request)
		if err != nil {
			t.Fatal(err)
		}
		assertEpisodeResult(t, next, request, 2)
		if record := store.inputs[lateID]; record.recipient != previous.Relation().ProcessID() || record.disposition != episodeInputNotAdmitted || record.acknowledged {
			t.Fatal("rejected input changed its original recipient or disposition")
		}
		if bindErr := store.bindInput(next, late); !errors.Is(bindErr, run.ErrSignalConflict) {
			t.Fatalf("same identity retarget=%v", bindErr)
		}
		if ackErr := store.acknowledgeInput(next.Relation().ProcessID(), lateID); !errors.Is(ackErr, run.ErrSignalConflict) {
			t.Fatalf("foreign late acknowledgment=%v", ackErr)
		}
		freshID, err := run.ParseSignalID("signal:after-ingress-seal")
		if err != nil {
			t.Fatal(err)
		}
		fresh, err := run.NewSignalRequest(freshID, run.WaitID{}, []byte(`"next request"`))
		if err != nil {
			t.Fatal(err)
		}
		if bindErr := store.bindInput(previous, fresh); !errors.Is(bindErr, errUnsafeEpisodeBoundary) {
			t.Fatalf("old ingress reopened=%v", bindErr)
		}
		if result.Usage() != (run.Usage{CommittedSteps: 3, PreparedEffects: 1, AcceptedSignals: 2}) {
			t.Fatalf("old budget changed=%+v", result.Usage())
		}
		head, present, loadErr := store.trees.LoadTree(t.Context(), previous.Relation().ProcessID())
		if loadErr != nil || !present {
			t.Fatalf("old retained tree=%t %v", present, loadErr)
		}
		restoredEngine, err := run.New(t.Context(), run.Config{Store: store.trees})
		if err != nil {
			t.Fatal(err)
		}
		restored, err := restoredEngine.Recover(t.Context(), binding, head)
		if err != nil {
			t.Fatal(err)
		}
		if _, sealErr := store.sealEpisode(t.Context(), restored); sealErr != nil {
			t.Fatal(sealErr)
		}
		retained := store.sealed[previous.Relation().ProcessID()].ProcessSnapshots()[0].SignalReceipts()
		if len(retained) != 2 || !retained[1].Matches(answer) || !retained[1].Consumed() {
			t.Fatal("retention erased the old admitted input")
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
	run.Definition
	answerID run.SignalID
	entered  chan struct{}
	release  chan struct{}
	held     atomic.Bool
}

func (e *episodeAnswerBarrier) Start(input run.Payload) (run.Execution, error) {
	execution, err := e.Definition.Start(input)
	if err != nil {
		return nil, err
	}
	return &episodeAnswerExecution{Execution: execution, owner: e}, nil
}

func (e *episodeAnswerBarrier) Restore(ctx context.Context, state run.ExecutionState) (run.Execution, error) {
	execution, err := e.Definition.Restore(ctx, state)
	if err != nil {
		return nil, err
	}
	return &episodeAnswerExecution{Execution: execution, owner: e}, nil
}

type episodeAnswerExecution struct {
	run.Execution
	owner *episodeAnswerBarrier
}

func (e *episodeAnswerExecution) Step(ctx context.Context, signals []run.Signal) (run.Transition, error) {
	if len(signals) > 0 && signals[0].ID() == e.owner.answerID && e.owner.held.CompareAndSwap(false, true) {
		close(e.owner.entered)
		select {
		case <-e.owner.release:
		case <-ctx.Done():
			return run.Transition{}, ctx.Err()
		}
	}
	return e.Execution.Step(ctx, signals)
}
