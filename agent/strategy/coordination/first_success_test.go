package coordination_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/agenttest"
	"github.com/Tangerg/scope/agent/strategy/coordination"
)

func TestFirstSuccessRestoresRejectedResultAndAcceptsInputWithoutWaitingForDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := time.Now()
		gate := bind(t, inputGate(t), nil)
		timer := deadlineBinding(t, coordination.Timer{})
		definition := competition(t, func(_ context.Context, outcome agent.ChildOutcome) (bool, error) {
			return outcome.Key().String() == "input", nil
		}, 3)
		deployment := bind(t, definition, nil)
		candidates := []agent.ChildSpec{
			candidate(t, "rejected", timer, encodedInput(t, started.Add(time.Second))),
			candidate(t, "input", gate, encodedInput(t, "continue or replace")),
			candidate(t, "deadline", timer, encodedInput(t, started.Add(time.Hour))),
		}
		bindings := resolver{gate.DeploymentRef(): gate, timer.DeploymentRef(): timer}
		store := agenttest.NewMemoryTreeDurability()
		config := agent.EngineConfig{TreeDurability: store, DeploymentResolver: bindings}
		engine, err := agent.NewEngine(config)
		if err != nil {
			t.Fatal(err)
		}
		root, err := engine.Start(t.Context(), deployment, encodedInput(t, candidates))
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		input := child(t, engine, root, "input")
		waitID, present := inspect(t, engine, input).Snapshot.WaitID()
		if !present {
			t.Fatal("input gate is not waiting")
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if inspect(t, engine, root).Snapshot.Status() != agent.StatusWaiting || result(t, child(t, engine, root, "rejected")).Status() != agent.StatusCompleted {
			t.Fatal("a rejected business result did not leave the competition waiting")
		}
		checkpoint, found, loadErr := store.LoadTree(t.Context(), root.ID())
		if loadErr != nil || !found {
			t.Fatalf("competition checkpoint exists=%t error=%v", found, loadErr)
		}
		restoredEngine, err := agent.NewEngine(config)
		if err != nil {
			t.Fatal(err)
		}
		restored, err := restoredEngine.RestoreTree(t.Context(), deployment, checkpoint)
		if err != nil {
			t.Fatal(err)
		}
		if killErr := root.Kill(t.Context(), "retire old coordination writer"); killErr != nil {
			t.Fatal(killErr)
		}
		if joinErr := root.Join(t.Context()); !errors.Is(joinErr, agent.ErrTreeIncarnationConflict) {
			t.Fatalf("retired competition writer = %v", joinErr)
		}
		synctest.Wait()
		restoredInput := child(t, restoredEngine, restored, "input")
		if restoredWait, present := inspect(t, restoredEngine, restoredInput).Snapshot.WaitID(); !present || restoredWait != waitID {
			t.Fatal("restoration changed the addressed input gate")
		}
		signalID, parseErr := agent.ParseSignalID("signal:reactive-input")
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		request, requestErr := agent.NewSignalRequest(signalID, waitID, []byte(`"replace"`))
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		if accepted, deliveryErr := restoredInput.DeliverSignals(t.Context(), request); deliveryErr != nil || !accepted {
			t.Fatalf("restored input accepted=%t error=%v", accepted, deliveryErr)
		}
		report := completedOutput[coordination.FirstSuccessResult](t, restored)
		if !report.Valid() || report.Winner == nil || report.Winner.String() != "input" || len(report.Starts) != 3 ||
			!slices.Equal(outcomeKeys(report), []string{"rejected", "input"}) {
			t.Fatalf("competition report = %+v", report)
		}
		inputOutput, _ := report.Outcomes[1].Result().Output()
		original, decodeErr := inputOutput.Decode[agent.Signal]()
		if decodeErr != nil || original.ID() != signalID || string(original.Payload()) != `"replace"` {
			t.Fatalf("selected input lost its identity: %+v error=%v", original, decodeErr)
		}
		if joinErr := restored.Join(t.Context()); joinErr != nil {
			t.Fatal(joinErr)
		}
		loser := result(t, child(t, restoredEngine, restored, "deadline"))
		if loser.Status() != agent.StatusCanceled || loser.Termination().Cause() != agent.TerminationCauseParentCancellation ||
			len(loser.Termination().UnresolvedEffectIDs()) != 0 || !time.Now().Equal(started.Add(time.Second)) {
			t.Fatalf("competition did not cancel and join its deadline: %+v at %v", loser, time.Now())
		}
		if usage := result(t, restored).Usage(); usage != (agent.Usage{CommittedSteps: 6, PreparedEffects: 5, AcceptedSignals: 7}) {
			t.Fatalf("competition consumption changed across recovery: %+v", usage)
		}
		closeEngine(t, restoredEngine)
		closeEngine(t, engine)
	})
}

func TestFirstSuccessUsesRequestOrderWhenSeveralResultsAreAlreadyVisible(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		timer := deadlineBinding(t, coordination.Timer{})
		definition := competition(t, func(_ context.Context, _ agent.ChildOutcome) (bool, error) { return true, nil }, 2)
		probe := &heldStepDefinition{Definition: definition, entered: make(chan agent.Signal, 1), release: make(chan struct{})}
		release := sync.OnceFunc(func() { close(probe.release) })
		defer release()
		deployment := bind(t, probe, nil)
		engine, err := agent.NewEngine(agent.EngineConfig{DeploymentResolver: resolver{timer.DeploymentRef(): timer}})
		if err != nil {
			t.Fatal(err)
		}
		candidates := []agent.ChildSpec{
			candidate(t, "declared-first", timer, encodedInput(t, time.Now().Add(2*time.Second))),
			candidate(t, "finished-first", timer, encodedInput(t, time.Now().Add(time.Second))),
		}
		root, err := engine.Start(t.Context(), deployment, encodedInput(t, candidates))
		if err != nil {
			t.Fatal(err)
		}
		<-probe.entered
		time.Sleep(2 * time.Second)
		synctest.Wait()
		for _, key := range []string{"declared-first", "finished-first"} {
			if result(t, child(t, engine, root, key)).Status() != agent.StatusCompleted {
				t.Fatal("candidate did not finish before wait registration")
			}
		}
		release()
		report := completedOutput[coordination.FirstSuccessResult](t, root)
		if !report.Valid() || report.Winner == nil || report.Winner.String() != "declared-first" ||
			!slices.Equal(outcomeKeys(report), []string{"declared-first", "finished-first"}) {
			t.Fatalf("simultaneous visible outcomes lost request order: %+v", report)
		}
		closeEngine(t, engine)
	})
}

func TestFirstSuccessWaitsForAllAdmissionsBeforeAcceptingCompletedChild(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		timer := deadlineBinding(t, coordination.Timer{})
		accepted := make(chan agent.ChildOutcome, 2)
		definition := competition(t, func(_ context.Context, outcome agent.ChildOutcome) (bool, error) {
			accepted <- outcome
			return true, nil
		}, 2)
		entered, released := make(chan struct{}), make(chan struct{})
		release := sync.OnceFunc(func() { close(released) })
		defer release()
		engine, err := agent.NewEngine(agent.EngineConfig{
			DeploymentResolver: resolver{timer.DeploymentRef(): timer},
			ProcessAdmitter: agent.ProcessAdmitterFunc(func(ctx context.Context, admission agent.ProcessAdmission) error {
				key, child := admission.Relation().ChildKey()
				if !child || key.String() != "slow-admission" {
					return nil
				}
				close(entered)
				select {
				case <-released:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}),
		})
		if err != nil {
			t.Fatal(err)
		}
		candidates := []agent.ChildSpec{
			candidate(t, "completed-first", timer, encodedInput(t, time.Now().Add(-time.Second))),
			candidate(t, "slow-admission", timer, encodedInput(t, time.Now().Add(time.Hour))),
		}
		root, err := engine.Start(t.Context(), bind(t, definition, nil), encodedInput(t, candidates))
		if err != nil {
			t.Fatal(err)
		}
		<-entered
		if result(t, child(t, engine, root, "completed-first")).Status() != agent.StatusCompleted {
			t.Fatal("first child did not complete")
		}
		synctest.Wait()
		select {
		case <-accepted:
			t.Fatal("competition accepted a result before all admissions settled")
		default:
		}
		release()
		report := completedOutput[coordination.FirstSuccessResult](t, root)
		if report.Winner == nil || report.Winner.String() != "completed-first" {
			t.Fatalf("winner=%v", report.Winner)
		}
		if err := root.Join(t.Context()); err != nil {
			t.Fatal(err)
		}
		closeEngine(t, engine)
	})
}

func TestFirstSuccessRetainsFailedAdmissionAndAllRejectedResults(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		timer := deadlineBinding(t, coordination.Timer{})
		definition := competition(t, func(_ context.Context, _ agent.ChildOutcome) (bool, error) { return false, nil }, 2)
		deployment := bind(t, definition, nil)
		engine, err := agent.NewEngine(agent.EngineConfig{DeploymentResolver: resolver{timer.DeploymentRef(): timer}})
		if err != nil {
			t.Fatal(err)
		}
		candidates := []agent.ChildSpec{
			candidate(t, "invalid-input", timer, encodedInput(t, "not-an-instant")),
			candidate(t, "rejected-result", timer, encodedInput(t, time.Now().Add(-time.Second))),
		}
		root, err := engine.Start(t.Context(), deployment, encodedInput(t, candidates))
		if err != nil {
			t.Fatal(err)
		}
		report := completedOutput[coordination.FirstSuccessResult](t, root)
		if !report.Valid() || report.Winner != nil || len(report.Starts) != 2 ||
			!slices.Equal(outcomeKeys(report), []string{"rejected-result"}) {
			t.Fatalf("all-rejected competition = %+v", report)
		}
		if failure, failed := report.Starts[0].Failure(); !failed || failure.Code() != "engine.child.input.invalid" {
			t.Fatalf("failed admission fact = %+v, failed=%t", failure, failed)
		}
		if _, present := report.Starts[0].ProcessID(); present {
			t.Fatal("failed admission published a candidate Process")
		}
		closeEngine(t, engine)
	})
}

func TestFirstSuccessBoundsAndDefinitionConformance(t *testing.T) {
	timer := deadlineBinding(t, coordination.Timer{})
	definition := competition(t, func(_ context.Context, _ agent.ChildOutcome) (bool, error) { return true, nil }, 1)
	spec := candidate(t, "candidate", timer, encodedInput(t, time.Date(2026, time.September, 9, 12, 0, 0, 0, time.UTC)))
	for _, candidates := range [][]agent.ChildSpec{nil, {}, {spec, spec}} {
		if _, err := definition.Start(encodedInput(t, candidates)); !errors.Is(err, agent.ErrInvalidInput) {
			t.Fatalf("invalid candidate set = %v", err)
		}
	}
	agenttest.RunDefinitionConformance(t, agenttest.DefinitionConformanceConfig{
		Definition: definition, Input: encodedInput(t, []agent.ChildSpec{spec}),
	})
}

func child(t testing.TB, engine *agent.Engine, root *agent.Process, key string) *agent.Process {
	t.Helper()
	tree, err := engine.InspectTree(context.Background(), root.ID())
	if err != nil {
		t.Fatal(err)
	}
	for _, process := range tree.Processes {
		childKey, present := process.Snapshot.Relation().ChildKey()
		if !present || childKey.String() != key {
			continue
		}
		found, present := engine.Process(process.Snapshot.ProcessID())
		if !present {
			t.Fatal("published child has no handle")
		}
		return found
	}
	t.Fatalf("child %q is absent", key)
	return nil
}

func outcomeKeys(report coordination.FirstSuccessResult) []string {
	var keys []string
	for _, outcome := range report.Outcomes {
		keys = append(keys, outcome.Key().String())
	}
	return keys
}
