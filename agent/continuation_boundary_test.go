package agent_test

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/coordination"
)

func TestEpisodeBoundaryRejectsUnresolvedDescendantAfterRootSuccess(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newEpisodeStore()
		worker, err := newEchoDefinition()
		if err != nil {
			t.Fatal(err)
		}
		workerBinding, err := agent.NewDeployment(agent.DeploymentConfig{
			Definition: worker, Dispatcher: episodeUncertainDispatcher{},
			ImplementationDigest: agent.ComputeDigest([]byte("uncertain-episode-worker")), ConfigurationDigest: agent.ComputeDigest([]byte("unknown-outcome")),
		})
		if err != nil {
			t.Fatal(err)
		}
		schema, err := agent.SchemaFor[string]()
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
			Accept: func(context.Context, agent.ChildOutcome) (bool, error) { return true, nil },
		})
		if err != nil {
			t.Fatal(err)
		}
		rootBinding, err := episodeBinding(competition)
		if err != nil {
			t.Fatal(err)
		}
		engine, err := agent.NewEngine(agent.EngineConfig{TreeDurability: store.trees, DeploymentResolver: episodeResolver{
			workerBinding.DeploymentRef(): workerBinding, gateBinding.DeploymentRef(): gateBinding,
		}})
		if err != nil {
			t.Fatal(err)
		}
		workerInput, err := agent.EncodeInput(echoInput{Value: "uncertain work"})
		if err != nil {
			t.Fatal(err)
		}
		gateInput, err := agent.EncodeInput("accepted input")
		if err != nil {
			t.Fatal(err)
		}
		workerKey, err := agent.ParseChildKey("uncertain")
		if err != nil {
			t.Fatal(err)
		}
		gateKey, err := agent.ParseChildKey("winner")
		if err != nil {
			t.Fatal(err)
		}
		budget := agent.Budget{Steps: 16, Effects: 8, Signals: 16}
		input, err := agent.EncodeInput([]agent.ChildSpec{
			{Key: workerKey, DeploymentRef: workerBinding.DeploymentRef(), Input: workerInput, Budget: budget},
			{Key: gateKey, DeploymentRef: gateBinding.DeploymentRef(), Input: gateInput, Budget: budget},
		})
		if err != nil {
			t.Fatal(err)
		}
		root, err := engine.Start(t.Context(), rootBinding, input)
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		tree, err := engine.InspectTree(t.Context(), root.ID())
		if err != nil {
			t.Fatal(err)
		}
		var winner *agent.Process
		var waitID agent.WaitID
		uncertain := false
		for _, fact := range tree.Processes {
			if fact.Snapshot.DeploymentRef() == workerBinding.DeploymentRef() {
				uncertain = len(fact.Snapshot.UnknownEffectIDs()) == 1
			}
			if fact.Snapshot.DeploymentRef() == gateBinding.DeploymentRef() {
				winner, _ = engine.Process(fact.Snapshot.ProcessID())
				waitID, _ = fact.Snapshot.WaitID()
			}
		}
		if winner == nil || !waitID.Valid() || !uncertain {
			t.Fatal("competition did not preserve independent input and unknown work")
		}
		if _, sealErr := store.sealEpisode(t.Context(), winner); !errors.Is(sealErr, errUnsafeEpisodeBoundary) {
			t.Fatalf("non-root continuation=%v", sealErr)
		}
		id, err := agent.ParseSignalID("signal:episode-winner")
		if err != nil {
			t.Fatal(err)
		}
		answer, err := agent.NewSignalRequest(id, waitID, []byte(`"winner"`))
		if err != nil {
			t.Fatal(err)
		}
		if accepted, deliveryErr := winner.DeliverSignals(t.Context(), answer); deliveryErr != nil || !accepted {
			t.Fatalf("winner=%t %v", accepted, deliveryErr)
		}
		result, err := root.Await(t.Context())
		if err != nil || result.Status() != agent.StatusCompleted {
			t.Fatalf("root=%s %v", result.Status(), err)
		}
		if joinErr := root.Join(t.Context()); joinErr != nil {
			t.Fatal(joinErr)
		}
		if _, sealErr := store.sealEpisode(t.Context(), root); !errors.Is(sealErr, errUnsafeEpisodeBoundary) {
			t.Fatalf("unresolved descendant crossed episode boundary: %v", sealErr)
		}
		if store.sealed[root.ID()].Valid() || store.allocations != 0 || store.starts != 0 {
			t.Fatal("unsafe boundary gained successor admission authority")
		}
		if closeErr := engine.Close(t.Context()); closeErr != nil {
			t.Fatal(closeErr)
		}
	})
}

type episodeUncertainDispatcher struct{}

func (episodeUncertainDispatcher) ReplayPolicy(agent.Effect) agent.ReplayPolicy {
	return agent.ReplayPolicyNever
}

func (episodeUncertainDispatcher) Dispatch(context.Context, agent.EffectRequest, agent.DeltaEmitter) (agent.Settlement, error) {
	return agent.Settlement{}, errors.New("remote result remains unknown")
}

type episodeResolver map[agent.DeploymentRef]agent.Deployment

func (e episodeResolver) Resolve(reference agent.DeploymentRef) (agent.Deployment, error) {
	deployment, present := e[reference]
	if !present {
		return agent.Deployment{}, agent.ErrInvalidDeployment
	}
	return deployment, nil
}
