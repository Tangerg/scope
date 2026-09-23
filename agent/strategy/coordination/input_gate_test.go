package coordination_test

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"sync"
	"testing"
	"testing/synctest"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/agenttest"
	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/strategy/coordination"
)

func TestInputGatePreservesIdentityAcrossRecoveryAndEarlyAnswer(t *testing.T) {
	for _, restore := range []bool{false, true} {
		name := "same_writer"
		if restore {
			name = "restored"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				gate := inputGate(t)
				probe := &heldStepDefinition{Definition: gate, entered: make(chan agent.Signal, 1), release: make(chan struct{})}
				release := sync.OnceFunc(func() { close(probe.release) })
				defer release()
				deployment := bind(t, probe, nil)
				config := agent.EngineConfig{TreeCommitter: agent.NewMemoryTreeCommitter()}
				store := agent.NewMemoryTreeCommitter()
				if restore {
					config.TreeCommitter = store
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
				initial, startErr := gate.Start(encodedInput(t, "request"))
				if startErr != nil {
					t.Fatal(startErr)
				}
				state, snapshotErr := initial.Snapshot()
				if snapshotErr != nil {
					t.Fatal(snapshotErr)
				}
				forgedBytes, marshalErr := jsonv2.Marshal(struct {
					Phase   string       `json:"phase"`
					Request string       `json:"request"`
					WaitID  agent.WaitID `json:"wait_id"`
					Answer  agent.Signal `json:"answer"`
				}{"completed", "request", waitID, opening})
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				forged, stateErr := agent.ParseExecutionState(state.Kind(), forgedBytes)
				if stateErr != nil {
					t.Fatal(stateErr)
				}
				if _, restoreErr := gate.Restore(t.Context(), forged); restoreErr == nil {
					t.Fatal("gate accepted opening evidence as an answer")
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
				if restore {
					checkpoint, found, loadErr := store.LoadTree(t.Context(), process.ID())
					if loadErr != nil || !found {
						t.Fatalf("input checkpoint exists=%t error=%v", found, loadErr)
					}
					restoredEngine, err = agent.NewEngine(agent.EngineConfig{TreeCommitter: store})
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
				if restore {
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
	config := agenttest.DefinitionConformanceConfig{
		Definition: inputGate(t), Input: encodedInput(t, "request"),
	}
	agenttest.RunDefinitionConformance(t, config)
	execution, err := config.Definition.Start(config.Input)
	if err != nil {
		t.Fatal(err)
	}
	state, err := execution.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	conformancetest.CheckRestoreCancellation(t, config.Definition, state)
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
				engine, err := agent.NewEngine(agent.EngineConfig{TreeCommitter: agent.NewMemoryTreeCommitter()})
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
					if !failed || failure.Kind() != agent.FailureKindContract || failure.Code() != "coordination.protocol.invalid" || final.Termination().Cause() != agent.TerminationCauseContractFailure {
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

func (h *heldStepDefinition) Start(input agent.Payload) (agent.Execution, error) {
	execution, err := h.Definition.Start(input)
	if err != nil {
		return nil, err
	}
	return &heldStepExecution{Execution: execution, owner: h}, nil
}

func (h *heldStepDefinition) Restore(ctx context.Context, state agent.ExecutionState) (agent.Execution, error) {
	execution, err := h.Definition.Restore(ctx, state)
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
