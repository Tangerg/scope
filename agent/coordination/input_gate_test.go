package coordination_test

import (
	startrand "crypto/rand"

	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/Tangerg/scope/agent/coordination"
	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/agent/run/runtest"
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
				probe := &heldStepDefinition{Definition: gate, entered: make(chan run.Signal, 1), release: make(chan struct{})}
				release := sync.OnceFunc(func() { close(probe.release) })
				defer release()
				deployment := bind(t, probe, nil)
				config := run.Config{Store: run.NewMemoryStore()}
				store := run.NewMemoryStore()
				if restore {
					config.Store = store
				}
				engine, err := run.New(t.Context(), config)
				if err != nil {
					t.Fatal(err)
				}
				process, err := engine.Start(t.Context(), deployment, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "test", Key: startrand.Text()}, Input: encodedInput(t, "request")})
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
					Phase   string     `json:"phase"`
					Request string     `json:"request"`
					WaitID  run.WaitID `json:"wait_id"`
					Answer  run.Signal `json:"answer"`
				}{"completed", "request", waitID, opening})
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				forged, stateErr := run.ParseExecutionState(state.Kind(), forgedBytes)
				if stateErr != nil {
					t.Fatal(stateErr)
				}
				if _, restoreErr := gate.Restore(t.Context(), forged); restoreErr == nil {
					t.Fatal("gate accepted opening evidence as an answer")
				}
				id, parseErr := run.ParseSignalID("signal:early-answer")
				if parseErr != nil {
					t.Fatal(parseErr)
				}
				request, requestErr := run.NewSignalRequest(id, waitID, []byte(`"answer"`))
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
				var restoredEngine *run.Runtime
				if restore {
					checkpoint, found, loadErr := store.LoadTree(t.Context(), process.Relation().ProcessID())
					if loadErr != nil || !found {
						t.Fatalf("input checkpoint exists=%t error=%v", found, loadErr)
					}
					restoredEngine, err = run.New(t.Context(), run.Config{Store: store})
					if err != nil {
						t.Fatal(err)
					}
					completed, err = restoredEngine.Recover(t.Context(), deployment, checkpoint)
					if err != nil {
						t.Fatal(err)
					}
				}
				release()
				answer := completedOutput[run.Signal](t, completed)
				if answer.ID() != id || string(answer.Payload()) != `"answer"` {
					t.Fatalf("gate answer lost its delivery identity: %+v", answer)
				}
				if usage := result(t, completed).Usage(); usage != (run.Usage{CommittedSteps: 3, PreparedEffects: 1, AcceptedSignals: 2}) {
					t.Fatalf("gate usage = %+v", usage)
				}
				if restore {
					if _, staleErr := process.Await(t.Context()); !errors.Is(staleErr, run.ErrTreeIncarnationConflict) {
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
	config := runtest.DefinitionConformanceConfig{
		Definition: inputGate(t), Input: encodedInput(t, "request"),
	}
	runtest.RunDefinitionConformance(t, config)
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

func TestInputGateRejectsInvalidAnswerAtAdmissionAndKeepsWaitOpen(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gate := inputGate(t)
		deployment := bind(t, gate, nil)
		engine, err := run.New(t.Context(), run.Config{Store: run.NewMemoryStore()})
		if err != nil {
			t.Fatal(err)
		}
		process, err := engine.Start(t.Context(), deployment, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "test", Key: startrand.Text()}, Input: encodedInput(t, "request")})
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		waitID, present := inspect(t, engine, process).Snapshot.WaitID()
		if !present {
			t.Fatal("input gate did not open its wait")
		}

		// A schema-invalid answer is rejected at admission, so it never advances the
		// wait or fails the Process: the mailbox owns the wait's acceptance contract.
		badID, badErr := run.ParseSignalID("signal:invalid-answer")
		if badErr != nil {
			t.Fatal(badErr)
		}
		bad, badReqErr := run.NewSignalRequest(badID, waitID, []byte(`123`))
		if badReqErr != nil {
			t.Fatal(badReqErr)
		}
		if accepted, deliveryErr := process.DeliverSignals(t.Context(), bad); accepted || !errors.Is(deliveryErr, run.ErrSignalRejected) {
			t.Fatalf("invalid answer admission accepted=%t error=%v", accepted, deliveryErr)
		}
		synctest.Wait()
		if _, stillOpen := inspect(t, engine, process).Snapshot.WaitID(); !stillOpen {
			t.Fatal("rejected answer closed the wait")
		}

		goodID, goodErr := run.ParseSignalID("signal:valid-answer")
		if goodErr != nil {
			t.Fatal(goodErr)
		}
		good, goodReqErr := run.NewSignalRequest(goodID, waitID, []byte(`"answer"`))
		if goodReqErr != nil {
			t.Fatal(goodReqErr)
		}
		if accepted, deliveryErr := process.DeliverSignals(t.Context(), good); deliveryErr != nil || !accepted {
			t.Fatalf("valid answer admission accepted=%t error=%v", accepted, deliveryErr)
		}
		if joinErr := process.Join(t.Context()); joinErr != nil {
			t.Fatal(joinErr)
		}
		final := result(t, process)
		if final.Termination().Status() != run.StatusCompleted {
			t.Fatalf("valid answer did not complete the gate: status=%s", final.Termination().Status())
		}
		if _, present := final.Termination().Output(); !present {
			t.Fatal("completed gate returned no output")
		}
		closeEngine(t, engine)
	})
}

func TestInputGateDoesNotCommitACanceledAnswer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gate := inputGate(t)
		probe := &heldStepDefinition{
			Definition: gate, entered: make(chan run.Signal, 1), release: make(chan struct{}),
			matches: func(signal run.Signal) bool { return string(signal.Payload()) == `"answer"` },
		}
		defer close(probe.release)
		deployment := bind(t, probe, nil)
		engine, err := run.New(t.Context(), run.Config{Store: run.NewMemoryStore()})
		if err != nil {
			t.Fatal(err)
		}
		process, err := engine.Start(t.Context(), deployment, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "test", Key: startrand.Text()}, Input: encodedInput(t, "request")})
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		waitID, present := inspect(t, engine, process).Snapshot.WaitID()
		if !present {
			t.Fatal("input gate did not open its wait")
		}
		id, idErr := run.ParseSignalID("signal:uncommitted-answer")
		if idErr != nil {
			t.Fatal(idErr)
		}
		request, requestErr := run.NewSignalRequest(id, waitID, []byte(`"answer"`))
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		if accepted, deliveryErr := process.DeliverSignals(t.Context(), request); deliveryErr != nil || !accepted {
			t.Fatalf("answer admission accepted=%t error=%v", accepted, deliveryErr)
		}
		<-probe.entered
		if cancelErr := process.Cancel(t.Context(), "cancel before answer adoption"); cancelErr != nil {
			t.Fatal(cancelErr)
		}
		if joinErr := process.Join(t.Context()); joinErr != nil {
			t.Fatal(joinErr)
		}
		final := result(t, process)
		if final.Termination().Status() != run.StatusCanceled || final.Usage() != (run.Usage{CommittedSteps: 2, PreparedEffects: 1, AcceptedSignals: 2}) {
			t.Fatalf("uncommitted answer changed gate progress: status=%s usage=%+v", final.Termination().Status(), final.Usage())
		}
		if _, present := final.Termination().Output(); present {
			t.Fatal("an uncommitted answer escaped through Output")
		}
		closeEngine(t, engine)
	})
}

func TestInputGateRejectsMissingAnswerSchema(t *testing.T) {
	if _, err := coordination.NewInputGate(coordination.InputGateConfig{}); !errors.Is(err, coordination.ErrInvalidConfig) {
		t.Fatalf("missing answer schema = %v", err)
	}
}

// The barrier controls when a pure candidate can return; it does not change its
// decisions or inspect private strategy state.
type heldStepDefinition struct {
	run.Definition
	once    sync.Once
	entered chan run.Signal
	release chan struct{}
	matches func(run.Signal) bool
}

func (h *heldStepDefinition) Start(input run.Payload) (run.Execution, error) {
	execution, err := h.Definition.Start(input)
	if err != nil {
		return nil, err
	}
	return &heldStepExecution{Execution: execution, owner: h}, nil
}

func (h *heldStepDefinition) Restore(ctx context.Context, state run.ExecutionState) (run.Execution, error) {
	execution, err := h.Definition.Restore(ctx, state)
	if err != nil {
		return nil, err
	}
	return &heldStepExecution{Execution: execution, owner: h}, nil
}

type heldStepExecution struct {
	run.Execution
	owner *heldStepDefinition
}

func (h *heldStepExecution) Step(ctx context.Context, signals []run.Signal) (run.Transition, error) {
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

func TestInputGateRestoreDerivesWaitingFromWaitIdentity(t *testing.T) {
	gate := inputGate(t)
	for _, sample := range []struct {
		phase, wait string
		valid       bool
	}{
		{"awaiting_open", "", true},
		{"awaiting_open", `,"wait_id":"wait:gate"`, true},
		{"waiting", `,"wait_id":"wait:gate"`, false},
		{"ready", `,"wait_id":"wait:gate"`, false},
	} {
		state, err := run.ParseExecutionState("coordination.input_gate", []byte(`{"phase":"`+sample.phase+`","request":"request"`+sample.wait+`}`))
		if err != nil {
			t.Fatal(err)
		}
		restored, err := gate.Restore(t.Context(), state)
		if !sample.valid {
			if !errors.Is(err, coordination.ErrInvalidExecutionState) {
				t.Fatalf("Restore %s: %v", state.Payload(), err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if sample.wait != "" {
			var answer run.Signal
			if err := jsonv2.Unmarshal([]byte(`{"id":"signal:answer","wait_id":"wait:gate","payload":"answer"}`), &answer); err != nil {
				t.Fatal(err)
			}
			transition, err := restored.Step(t.Context(), []run.Signal{answer})
			if err != nil || transition.Kind() != run.TransitionKindComplete {
				t.Fatalf("restored gate did not consume answer: %v", err)
			}
		}
	}
}

func TestCompletedInputGateLeavesItsAnswerToTheEngine(t *testing.T) {
	gate := inputGate(t)
	for payload, valid := range map[string]bool{
		`{"phase":"completed","request":"request"}`:                         true,
		`{"phase":"completed","request":"request","wait_id":"wait:answer"}`: false,
	} {
		state, err := run.ParseExecutionState("coordination.input_gate", json.RawMessage(payload))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := gate.Restore(t.Context(), state); (err == nil) != valid || !valid && !errors.Is(err, coordination.ErrInvalidExecutionState) {
			t.Fatalf("Restore(%s) = %v, want valid=%t", payload, err, valid)
		}
	}
}
