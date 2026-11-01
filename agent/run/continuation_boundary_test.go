package run_test

import (
	startrand "crypto/rand"

	"context"
	"errors"
	"testing"
	"testing/synctest"

	"github.com/Tangerg/scope/agent/coordination"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/jsonschema"
)

func TestEpisodeBoundaryRejectsUnresolvedDescendantAfterRootSuccess(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newEpisodeStore()
		worker, err := newEchoDefinition()
		if err != nil {
			t.Fatal(err)
		}
		workerBinding, err := run.Bind[run.Payload, run.Payload](run.BindingConfig{
			Definition: worker, Dispatcher: episodeUncertainDispatcher{},
			ImplementationDigest: run.ComputeDigest([]byte("uncertain-episode-worker")), ConfigurationDigest: run.ComputeDigest([]byte("unknown-outcome")),
		})
		if err != nil {
			t.Fatal(err)
		}
		schema, err := jsonschema.For[string]()
		if err != nil {
			t.Fatal(err)
		}
		gate, err := coordination.NewInputGate(coordination.InputGateConfig{Name: "example.episode.winner", Description: "Receive the accepted result.", RequestSchema: schema, AnswerSchema: schema})
		if err != nil {
			t.Fatal(err)
		}
		gateBinding, err := episodeBinding(gate)
		if err != nil {
			t.Fatal(err)
		}
		competition, err := coordination.NewFirstSuccess(coordination.FirstSuccessConfig{
			Name: "example.episode.competition", Description: "Complete while retaining the losing worker's uncertainty.", MaxCandidates: 2,
			Accept: func(context.Context, run.ChildKey, run.ChildOutcome) (bool, error) { return true, nil },
		})
		if err != nil {
			t.Fatal(err)
		}
		rootBinding, err := episodeBinding(competition)
		if err != nil {
			t.Fatal(err)
		}
		engine, err := run.New(t.Context(), run.Config{Store: store.trees, ProgramResolver: episodeResolver{
			workerBinding.ProgramRef(): workerBinding, gateBinding.ProgramRef(): gateBinding,
		}})
		if err != nil {
			t.Fatal(err)
		}
		workerInput, err := run.EncodePayload(echoInput{Value: "uncertain work"})
		if err != nil {
			t.Fatal(err)
		}
		gateInput, err := run.EncodePayload("accepted input")
		if err != nil {
			t.Fatal(err)
		}
		workerKey, err := run.ParseChildKey("uncertain")
		if err != nil {
			t.Fatal(err)
		}
		gateKey, err := run.ParseChildKey("winner")
		if err != nil {
			t.Fatal(err)
		}
		budget := run.Budget{Steps: run.NewQuota(16), Effects: run.NewQuota(8), Signals: run.NewQuota(16)}
		input, err := run.EncodePayload([]run.ChildSpec{
			{Key: workerKey, ProgramRef: workerBinding.ProgramRef(), Input: workerInput, Budget: budget},
			{Key: gateKey, ProgramRef: gateBinding.ProgramRef(), Input: gateInput, Budget: budget},
		})
		if err != nil {
			t.Fatal(err)
		}
		root, err := engine.Start(t.Context(), rootBinding, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "test", Key: startrand.Text()}, Input: input})
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		tree, err := engine.InspectTree(t.Context(), root.Relation().ProcessID())
		if err != nil {
			t.Fatal(err)
		}
		var winner *run.Handle[run.Payload]
		var waitID run.WaitID
		uncertain := false
		for _, fact := range tree.Processes {
			if fact.Snapshot.ProgramRef() == workerBinding.ProgramRef() {
				uncertain = len(fact.Snapshot.UnknownEffectIDs()) == 1
			}
			if fact.Snapshot.ProgramRef() == gateBinding.ProgramRef() {
				winner, _ = engine.Process(fact.Snapshot.Relation().ProcessID())
				waitID, _ = fact.Snapshot.WaitID()
			}
		}
		if winner == nil || !waitID.Valid() || !uncertain {
			t.Fatal("competition did not preserve independent input and unknown work")
		}
		if _, sealErr := store.sealEpisode(t.Context(), winner); !errors.Is(sealErr, errUnsafeEpisodeBoundary) {
			t.Fatalf("non-root continuation=%v", sealErr)
		}
		id, err := run.ParseSignalID("signal:episode-winner")
		if err != nil {
			t.Fatal(err)
		}
		answer, err := run.NewSignalRequest(id, waitID, []byte(`"winner"`))
		if err != nil {
			t.Fatal(err)
		}
		if accepted, deliveryErr := winner.DeliverSignals(t.Context(), answer); deliveryErr != nil || !accepted {
			t.Fatalf("winner=%t %v", accepted, deliveryErr)
		}
		result, err := root.Await(t.Context())
		if err != nil || result.Termination().Status() != run.StatusCompleted {
			t.Fatalf("root=%s %v", result.Termination().Status(), err)
		}
		if joinErr := root.Join(t.Context()); joinErr != nil {
			t.Fatal(joinErr)
		}
		if _, sealErr := store.sealEpisode(t.Context(), root); !errors.Is(sealErr, errUnsafeEpisodeBoundary) {
			t.Fatalf("unresolved descendant crossed episode boundary: %v", sealErr)
		}
		if store.sealed[root.Relation().ProcessID()].Valid() || store.allocations != 0 || store.starts != 0 {
			t.Fatal("unsafe boundary gained successor admission authority")
		}
		if closeErr := engine.Close(t.Context()); closeErr != nil {
			t.Fatal(closeErr)
		}
	})
}

type episodeUncertainDispatcher struct{}

func (episodeUncertainDispatcher) Policy(run.Effect) run.EffectPolicy {
	return run.EffectPolicy{Replay: run.ReplayPolicyNever}
}

func (episodeUncertainDispatcher) Dispatch(context.Context, run.EffectRequest, run.DeltaEmitter) (run.Settlement, error) {
	return run.Settlement{}, errors.New("remote result remains unknown")
}

type episodeResolver map[run.ProgramRef]run.Program[run.Payload, run.Payload]

func (e episodeResolver) Resolve(reference run.ProgramRef) (run.Program[run.Payload, run.Payload], error) {
	deployment, present := e[reference]
	if !present {
		return run.Program[run.Payload, run.Payload]{}, run.ErrInvalidProgram
	}
	return deployment, nil
}
