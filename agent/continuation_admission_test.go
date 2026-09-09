package agent_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/Tangerg/scope/agent"
)

func prepareEpisode(t testing.TB, store *episodeStore) (*agent.Engine, *agent.Process, agent.Deployment, successorRequest) {
	t.Helper()
	deployment, err := episodeDeployment()
	if err != nil {
		t.Fatal(err)
	}
	engine, err := agent.NewEngine(agent.EngineConfig{TreeDurability: store.trees})
	if err != nil {
		t.Fatal(err)
	}
	input, err := agent.EncodeInput(episodeState{Summary: "explicit state"})
	if err != nil {
		t.Fatal(err)
	}
	previous, err := engine.Start(context.Background(), deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.sealEpisode(context.Background(), previous)
	if err != nil {
		t.Fatal(err)
	}
	output, present := result.Output()
	if !present {
		t.Fatal("previous episode has no output")
	}
	transfer, err := agent.ParseInput(output.JSON())
	if err != nil {
		t.Fatal(err)
	}
	return engine, previous, deployment, successorRequest{
		Predecessor: previous.ID(), DeploymentRef: deployment.DeploymentRef(), Input: transfer,
		Limits: agent.Limits{MaxSteps: 8, MaxEffects: 4, MaxSignals: 8, MaxPendingSignals: 8}, TreeLimits: agent.DefaultTreeLimits(),
	}
}

func assertEpisodeResult(t testing.TB, process *agent.Process, request successorRequest, revision uint32) {
	t.Helper()
	result, err := process.Await(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if joinErr := process.Join(context.Background()); joinErr != nil {
		t.Fatal(joinErr)
	}
	output, present := result.Output()
	if !present || result.Status() != agent.StatusCompleted {
		t.Fatalf("successor result=%s", result.Status())
	}
	state, err := output.Decode[episodeState]()
	if err != nil {
		t.Fatal(err)
	}
	if state != (episodeState{Revision: revision, Summary: "explicit state"}) || result.Usage() != (agent.Usage{CommittedSteps: 1}) {
		t.Fatalf("state=%+v usage=%+v", state, result.Usage())
	}
	if process.Budget() != (agent.Budget{Steps: request.Limits.MaxSteps, Effects: request.Limits.MaxEffects, Signals: request.Limits.MaxSignals}) || process.DeploymentRef() != request.DeploymentRef {
		t.Fatal("successor changed its allocation or binding")
	}
}

func TestSuccessorAdmissionSurvivesLostStartAcknowledgment(t *testing.T) {
	store := newEpisodeStore()
	previousEngine, previous, deployment, request := prepareEpisode(t, store)
	store.loseStartAcknowledgment = true
	first := &episodeHost{store: store}
	process, err := first.start(t.Context(), deployment, request)
	if process != nil || !errors.Is(err, errStartAcknowledgmentLost) {
		t.Fatalf("ambiguous start=%v %v", process, err)
	}
	record := store.successors[previous.ID()]
	if !record.successor.Valid() || store.allocations != 1 || store.starts != 1 {
		t.Fatalf("admission after lost response=%+v", record)
	}
	head, present, loadErr := store.trees.LoadTree(t.Context(), record.successor)
	if loadErr != nil || !present || head.ProcessSnapshots()[0].Usage() != (agent.Usage{}) {
		t.Fatalf("unpublished start cut=%t %v", present, loadErr)
	}
	if _, published := first.engines[0].Process(record.successor); published {
		t.Fatal("lost start acknowledgment published a Process")
	}
	restarted := &episodeHost{store: store}
	recovered, err := restarted.start(t.Context(), deployment, request)
	if err != nil {
		t.Fatal(err)
	}
	assertEpisodeResult(t, recovered, request, 2)
	duplicate, err := restarted.start(t.Context(), deployment, request)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.ID() != record.successor || duplicate.ID() != record.successor || store.allocations != 1 || store.starts != 1 {
		t.Fatal("reconciliation created or charged another successor")
	}
	if result, awaitErr := previous.Await(t.Context()); awaitErr != nil || result.Usage() != (agent.Usage{CommittedSteps: 1}) {
		t.Fatalf("predecessor changed: %+v %v", result.Usage(), awaitErr)
	}
	for _, closeErr := range []error{first.close(t.Context()), restarted.close(t.Context()), previousEngine.Close(t.Context())} {
		if closeErr != nil {
			t.Fatal(closeErr)
		}
	}
}

func TestSuccessorAttemptIsFencedBeforeOrAfterInitialization(t *testing.T) {
	for _, afterAdmission := range []bool{false, true} {
		name := "before_admission"
		if afterAdmission {
			name = "after_admission"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				store := newEpisodeStore()
				previousEngine, _, base, request := prepareEpisode(t, store)
				deployment := base
				var barrier *episodeStartBarrier
				if afterAdmission {
					barrier = &episodeStartBarrier{Definition: base.Definition(), entered: make(chan struct{}), release: make(chan struct{})}
					var bindingErr error
					deployment, bindingErr = episodeBinding(barrier)
					if bindingErr != nil {
						t.Fatal(bindingErr)
					}
				}
				request.DeploymentRef = deployment.DeploymentRef()
				oldHost := &episodeHost{store: store}
				oldAttempt, existing, claimErr := store.claim(request)
				if claimErr != nil || existing.Valid() {
					t.Fatalf("first claim=%v %v", existing, claimErr)
				}
				late := make(chan error, 1)
				if afterAdmission {
					go func() {
						_, startErr := oldHost.activate(t.Context(), deployment, request, oldAttempt, existing)
						late <- startErr
					}()
					<-barrier.entered
				}
				replacement := &episodeHost{store: store}
				winner, startErr := replacement.start(t.Context(), deployment, request)
				if startErr != nil {
					t.Fatal(startErr)
				}
				assertEpisodeResult(t, winner, request, 2)
				if afterAdmission {
					close(barrier.release)
				} else {
					_, expiredErr := oldHost.activate(t.Context(), deployment, request, oldAttempt, existing)
					late <- expiredErr
				}
				if expiredErr := <-late; !errors.Is(expiredErr, errSuccessorAttemptExpired) {
					t.Fatalf("late initialization=%v", expiredErr)
				}
				if store.allocations != 1 || store.starts != 1 {
					t.Fatalf("allocations=%d starts=%d", store.allocations, store.starts)
				}
				for _, closeErr := range []error{oldHost.close(t.Context()), replacement.close(t.Context()), previousEngine.Close(t.Context())} {
					if closeErr != nil {
						t.Fatal(closeErr)
					}
				}
			})
		})
	}
}

type episodeStartBarrier struct {
	agent.Definition
	started atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (e *episodeStartBarrier) Start(input agent.Input) (agent.Execution, error) {
	if e.started.CompareAndSwap(false, true) {
		close(e.entered)
		<-e.release
	}
	return e.Definition.Start(input)
}

func TestSuccessorRequestCannotChangeAfterAdmission(t *testing.T) {
	store := newEpisodeStore()
	engine, _, deployment, request := prepareEpisode(t, store)
	host := &episodeHost{store: store}
	process, err := host.start(t.Context(), deployment, request)
	if err != nil {
		t.Fatal(err)
	}
	assertEpisodeResult(t, process, request, 2)
	changed := request
	changed.Input, err = agent.EncodeInput(episodeState{Revision: 2, Summary: "different state"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, claimErr := store.claim(changed); !errors.Is(claimErr, errSuccessorConflict) {
		t.Fatalf("changed state=%v", claimErr)
	}
	changed = request
	changed.Limits.MaxSteps++
	if _, _, claimErr := store.claim(changed); !errors.Is(claimErr, errSuccessorConflict) {
		t.Fatalf("changed budget=%v", claimErr)
	}
	changed = request
	changed.TreeLimits.MaxChildren++
	if _, _, claimErr := store.claim(changed); !errors.Is(claimErr, errSuccessorConflict) {
		t.Fatalf("changed tree bounds=%v", claimErr)
	}
	capability, err := agent.ParseCapability("episode.additional")
	if err != nil {
		t.Fatal(err)
	}
	changed = request
	changed.Capabilities, err = agent.NewCapabilitySet(capability)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, claimErr := store.claim(changed); !errors.Is(claimErr, errSuccessorConflict) {
		t.Fatalf("changed authority=%v", claimErr)
	}
	if store.allocations != 1 || store.starts != 1 {
		t.Fatal("conflicting request charged another allocation")
	}
	for _, closeErr := range []error{host.close(t.Context()), engine.Close(t.Context())} {
		if closeErr != nil {
			t.Fatal(closeErr)
		}
	}
}
