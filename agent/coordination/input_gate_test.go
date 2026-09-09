package coordination_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/agenttest"
	"github.com/Tangerg/scope/agent/coordination"
)

func TestInputGatePreservesIdentityAcrossRecoveryAndEarlyAnswer(t *testing.T) {
	for _, durable := range []bool{false, true} {
		name := "ephemeral"
		if durable {
			name = "durable"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				gate := inputGate(t)
				probe := &heldStepDefinition{Definition: gate, entered: make(chan agent.Signal, 1), release: make(chan struct{})}
				release := sync.OnceFunc(func() { close(probe.release) })
				defer release()
				deployment := bind(t, probe, nil)
				config := agent.EngineConfig{}
				store := agenttest.NewMemoryTreeDurability()
				if durable {
					config.TreeDurability = store
				}
				engine, err := agent.NewEngine(config)
				if err != nil {
					t.Fatal(err)
				}
				process, err := engine.Start(t.Context(), deployment, encodedInput(t, "request"))
				if err != nil {
					t.Fatal(err)
				}
				opening := <-probe.entered
				waitID, present := opening.WaitID()
				if !present {
					t.Fatal("opening has no wait identity")
				}
				id, parseErr := agent.ParseSignalID("signal:early-answer")
				if parseErr != nil {
					t.Fatal(parseErr)
				}
				request, requestErr := agent.NewSignalRequest(id, waitID, []byte(`"answer"`))
				if requestErr != nil {
					t.Fatal(requestErr)
				}
				if accepted, deliveryErr := process.DeliverSignals(t.Context(), request); deliveryErr != nil || !accepted {
					t.Fatalf("early answer accepted=%t error=%v", accepted, deliveryErr)
				}
				if accepted, deliveryErr := process.DeliverSignals(t.Context(), request); deliveryErr != nil || accepted {
					t.Fatalf("duplicate answer accepted=%t error=%v", accepted, deliveryErr)
				}
				completed := process
				var restoredEngine *agent.Engine
				if durable {
					checkpoint, found, loadErr := store.LoadTree(t.Context(), process.ID())
					if loadErr != nil || !found {
						t.Fatalf("input checkpoint exists=%t error=%v", found, loadErr)
					}
					restoredEngine, err = agent.NewEngine(agent.EngineConfig{TreeDurability: store})
					if err != nil {
						t.Fatal(err)
					}
					completed, err = restoredEngine.RestoreTree(t.Context(), deployment, checkpoint)
					if err != nil {
						t.Fatal(err)
					}
				}
				release()
				answer := completedOutput[agent.Signal](t, completed)
				if answer.ID() != id || string(answer.Payload()) != `"answer"` {
					t.Fatalf("gate answer lost its delivery identity: %+v", answer)
				}
				if usage := result(t, completed).Usage(); usage != (agent.Usage{CommittedSteps: 3, PreparedEffects: 1, AcceptedSignals: 2}) {
					t.Fatalf("gate usage = %+v", usage)
				}
				if durable {
					if _, staleErr := process.Await(t.Context()); !errors.Is(staleErr, agent.ErrTreeIncarnationConflict) {
						t.Fatalf("retired gate writer = %v", staleErr)
					}
					closeEngine(t, restoredEngine)
				}
				closeEngine(t, engine)
			})
		})
	}
}

func TestInputGateDefinitionConformance(t *testing.T) {
	agenttest.RunDefinitionConformance(t, agenttest.DefinitionConformanceConfig{
		Definition: inputGate(t), Input: encodedInput(t, "request"),
	})
}

func TestInputGateDoesNotCommitAnInvalidOrCanceledAnswer(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		name := "invalid-schema"
		if cancel {
			name = "canceled-candidate"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				gate := inputGate(t)
				var definition agent.Definition = gate
				var probe *heldStepDefinition
				payload := []byte(`123`)
				if cancel {
					payload = []byte(`"answer"`)
					probe = &heldStepDefinition{
						Definition: gate, entered: make(chan agent.Signal, 1), release: make(chan struct{}),
						matches: func(signal agent.Signal) bool { return string(signal.Payload()) == `"answer"` },
					}
					defer close(probe.release)
					definition = probe
				}
				deployment := bind(t, definition, nil)
				engine, err := agent.NewEngine(agent.EngineConfig{})
				if err != nil {
					t.Fatal(err)
				}
				process, err := engine.Start(t.Context(), deployment, encodedInput(t, "request"))
				if err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				waitID, present := inspect(t, engine, process).Snapshot.WaitID()
				if !present {
					t.Fatal("input gate did not open its wait")
				}
				id, idErr := agent.ParseSignalID("signal:uncommitted-answer")
				if idErr != nil {
					t.Fatal(idErr)
				}
				request, requestErr := agent.NewSignalRequest(id, waitID, payload)
				if requestErr != nil {
					t.Fatal(requestErr)
				}
				if accepted, deliveryErr := process.DeliverSignals(t.Context(), request); deliveryErr != nil || !accepted {
					t.Fatalf("answer admission accepted=%t error=%v", accepted, deliveryErr)
				}
				want := agent.StatusFailed
				if cancel {
					<-probe.entered
					if cancelErr := process.RequestCancellation(t.Context(), "cancel before answer adoption"); cancelErr != nil {
						t.Fatal(cancelErr)
					}
					want = agent.StatusCanceled
				}
				if joinErr := process.Join(t.Context()); joinErr != nil {
					t.Fatal(joinErr)
				}
				final := result(t, process)
				if final.Status() != want || final.Usage() != (agent.Usage{CommittedSteps: 2, PreparedEffects: 1, AcceptedSignals: 2}) {
					t.Fatalf("uncommitted answer changed gate progress: status=%s usage=%+v", final.Status(), final.Usage())
				}
				if _, present := final.Output(); present {
					t.Fatal("an uncommitted answer escaped through Output")
				}
				if !cancel {
					failure, failed := final.Termination().Failure()
					if !failed || failure.Code() != "execution.step.failed" {
						t.Fatalf("answer schema failure = %+v", failure)
					}
				}
				closeEngine(t, engine)
			})
		})
	}
}

func TestInputGateRejectsMissingAnswerSchema(t *testing.T) {
	if _, err := coordination.NewInputGate(coordination.InputGateConfig{}); !errors.Is(err, coordination.ErrInvalidConfig) {
		t.Fatalf("missing answer schema = %v", err)
	}
}

// The barrier controls when a pure candidate can return; it does not change its
// decisions or inspect private strategy state.
type heldStepDefinition struct {
	agent.Definition
	once    sync.Once
	entered chan agent.Signal
	release chan struct{}
	matches func(agent.Signal) bool
}

func (h *heldStepDefinition) Start(input agent.Input) (agent.Execution, error) {
	execution, err := h.Definition.Start(input)
	if err != nil {
		return nil, err
	}
	return &heldStepExecution{Execution: execution, owner: h}, nil
}

func (h *heldStepDefinition) Restore(state agent.ExecutionState) (agent.Execution, error) {
	execution, err := h.Definition.Restore(state)
	if err != nil {
		return nil, err
	}
	return &heldStepExecution{Execution: execution, owner: h}, nil
}

type heldStepExecution struct {
	agent.Execution
	owner *heldStepDefinition
}

func (h *heldStepExecution) Step(ctx context.Context, signals []agent.Signal) (agent.Transition, error) {
	if len(signals) > 0 && (h.owner.matches == nil || h.owner.matches(signals[0])) {
		h.owner.once.Do(func() {
			h.owner.entered <- signals[0]
			select {
			case <-h.owner.release:
			case <-ctx.Done():
			}
		})
	}
	return h.Execution.Step(ctx, signals)
}
