package coordination_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/coordination"
)

func TestFirstSuccessRejectsExternalChildStartWithoutChangingProgress(t *testing.T) {
	timer := deadlineBinding(t, coordination.Timer{})
	definition := competition(t, func(_ context.Context, _ agent.ChildOutcome) (bool, error) { return true, nil }, 1)
	spec := candidate(t, "one", timer, encodedInput(t, time.Date(2026, time.September, 9, 12, 0, 0, 0, time.UTC)))
	execution, err := definition.Start(encodedInput(t, []agent.ChildSpec{spec}))
	if err != nil {
		t.Fatal(err)
	}
	if _, stepErr := execution.Step(t.Context(), nil); stepErr != nil {
		t.Fatal(stepErr)
	}
	before, err := execution.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(struct {
		Operation     string              `json:"operation"`
		Key           agent.ChildKey      `json:"key"`
		ProcessID     string              `json:"process_id"`
		DeploymentRef agent.DeploymentRef `json:"deployment_ref"`
	}{Operation: "start_child", Key: spec.Key, ProcessID: "process:forged", DeploymentRef: spec.DeploymentRef})
	if err != nil {
		t.Fatal(err)
	}
	wire, err := json.Marshal(struct {
		ID      string          `json:"id"`
		Payload json.RawMessage `json:"payload"`
	}{ID: "signal:external", Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	var signal agent.Signal
	if decodeErr := json.Unmarshal(wire, &signal); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if _, stepErr := execution.Step(t.Context(), []agent.Signal{signal}); !errors.Is(stepErr, agent.ErrInvalidSignal) {
		t.Fatalf("external child start was accepted: %v", stepErr)
	}
	after, err := execution.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before.Payload(), after.Payload()) {
		t.Fatal("external child start changed competition progress")
	}
	if _, restoreErr := definition.Restore(t.Context(), after); restoreErr != nil {
		t.Fatalf("rejected external start damaged restoration: %v", restoreErr)
	}
}

func TestCoordinationRejectsExternalTimerAndWaitOpening(t *testing.T) {
	deadline := time.Date(2026, time.September, 9, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name       string
		definition agent.Definition
		input      agent.Input
		waitID     string
		payload    any
	}{
		{name: "timer", definition: deadlineBinding(t, coordination.Timer{}).Definition(), input: encodedInput(t, deadline), payload: struct {
			Deadline time.Time `json:"deadline"`
			Reached  bool      `json:"reached"`
		}{Deadline: deadline, Reached: true}},
		{name: "wait opening", definition: inputGate(t), input: encodedInput(t, "request"), waitID: "wait:external", payload: "request"},
	} {
		t.Run(test.name, func(t *testing.T) {
			execution, err := test.definition.Start(test.input)
			if err != nil {
				t.Fatal(err)
			}
			if _, stepErr := execution.Step(t.Context(), nil); stepErr != nil {
				t.Fatal(stepErr)
			}
			before, err := execution.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			wire, err := json.Marshal(struct {
				ID      string `json:"id"`
				WaitID  string `json:"wait_id,omitempty"`
				Payload any    `json:"payload"`
			}{ID: "signal:external", WaitID: test.waitID, Payload: test.payload})
			if err != nil {
				t.Fatal(err)
			}
			var signal agent.Signal
			if decodeErr := json.Unmarshal(wire, &signal); decodeErr != nil {
				t.Fatal(decodeErr)
			}
			if _, stepErr := execution.Step(t.Context(), []agent.Signal{signal}); !errors.Is(stepErr, coordination.ErrInvalidProtocol) {
				t.Fatalf("external authority was accepted: %v", stepErr)
			}
			after, err := execution.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before.Payload(), after.Payload()) {
				t.Fatal("external authority changed progress")
			}
			if _, restoreErr := test.definition.Restore(t.Context(), after); restoreErr != nil {
				t.Fatalf("rejection damaged restoration: %v", restoreErr)
			}
		})
	}
}

func TestCoordinationRejectsMalformedRestoration(t *testing.T) {
	deadline := deadlineBinding(t, coordination.Timer{})
	first := competition(t, func(_ context.Context, _ agent.ChildOutcome) (bool, error) { return true, nil }, 2)
	spec := candidate(t, "one", deadline, encodedInput(t, time.Date(2026, time.September, 9, 12, 0, 0, 0, time.UTC)))
	for _, sample := range []struct {
		name       string
		definition agent.Definition
		input      agent.Input
	}{
		{name: "gate", definition: inputGate(t), input: encodedInput(t, "request")},
		{name: "deadline", definition: deadline.Definition(), input: spec.Input},
		{name: "first-success", definition: first, input: encodedInput(t, []agent.ChildSpec{spec})},
	} {
		t.Run(sample.name, func(t *testing.T) {
			execution, err := sample.definition.Start(sample.input)
			if err != nil {
				t.Fatal(err)
			}
			state, err := execution.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			foreign, err := agent.NewExecutionState("foreign", state.Payload())
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if _, restoreErr := sample.definition.Restore(ctx, state); !errors.Is(restoreErr, context.Canceled) {
				t.Fatalf("canceled restoration = %v", restoreErr)
			}
			if _, restoreErr := sample.definition.Restore(t.Context(), state); restoreErr != nil {
				t.Fatalf("cancellation damaged the immutable state: %v", restoreErr)
			}
			for _, invalid := range []agent.ExecutionState{
				foreign,
				mutatedState(t, state, "unexpected", true),
				mutatedState(t, state, "phase", "waiting"),
				mutatedState(t, state, "phase", "future-phase"),
			} {
				if _, restoreErr := sample.definition.Restore(t.Context(), invalid); !errors.Is(restoreErr, coordination.ErrInvalidState) {
					t.Fatalf("malformed state was not rejected: %v", restoreErr)
				}
			}
		})
	}
	if _, err := first.Start(encodedInput(t, []agent.ChildSpec{spec, spec})); !errors.Is(err, agent.ErrInvalidInput) {
		t.Fatalf("duplicate candidate identity = %v", err)
	}
}

func TestFirstSuccessSurfacesPolicyFailureAndFailedStarts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		timer := deadlineBinding(t, coordination.Timer{})
		policyFailure := errors.New("business predicate could not evaluate the result")
		for _, failedStarts := range []bool{false, true} {
			definition := competition(t, func(_ context.Context, _ agent.ChildOutcome) (bool, error) {
				return false, policyFailure
			}, 1)
			deployment := bind(t, definition, nil)
			engine, err := agent.NewEngine(agent.EngineConfig{DeploymentResolver: resolver{timer.DeploymentRef(): timer}})
			if err != nil {
				t.Fatal(err)
			}
			input := encodedInput(t, time.Now().Add(-time.Second))
			if failedStarts {
				input = encodedInput(t, "invalid deadline")
			}
			root, err := engine.Start(t.Context(), deployment, encodedInput(t, []agent.ChildSpec{candidate(t, "one", timer, input)}))
			if err != nil {
				t.Fatal(err)
			}
			final := result(t, root)
			if failedStarts {
				report := completedOutput[coordination.FirstSuccessResult](t, root)
				if !report.Valid() || report.Winner != nil || len(report.Outcomes) != 0 || len(report.Starts) != 1 {
					t.Fatalf("failed start did not produce a complete report: %+v", report)
				}
			} else {
				failure, failed := final.Termination().Failure()
				if final.Status() != agent.StatusFailed || !failed || failure.Code() != "execution.step.failed" {
					t.Fatalf("policy error became a business result: %+v", final)
				}
			}
			closeEngine(t, engine)
		}
	})
}

func TestDeadlineReportsDefiniteInterruptionWithoutProcessCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		deployment := deadlineBinding(t, interruptedTimer{})
		engine, err := agent.NewEngine(agent.EngineConfig{})
		if err != nil {
			t.Fatal(err)
		}
		process, err := engine.Start(t.Context(), deployment, encodedInput(t, time.Now().Add(time.Hour)))
		if err != nil {
			t.Fatal(err)
		}
		final := result(t, process)
		failure, failed := final.Termination().Failure()
		if final.Status() != agent.StatusFailed || !failed || failure.Code() != "coordination.deadline.interrupted" ||
			len(final.Termination().UnresolvedEffectIDs()) != 0 {
			t.Fatalf("definite timer interruption = %+v", final)
		}
		closeEngine(t, engine)
	})
}

func TestTimerRejectsAnUnrelatedOperation(t *testing.T) {
	for _, payload := range []json.RawMessage{[]byte(`{}`), []byte(`{"deadline":"0001-01-01T00:00:00Z"}`), []byte(`{"deadline":123}`)} {
		effect, err := agent.NewDispatcherEffect(payload)
		if err != nil {
			t.Fatal(err)
		}
		if policy := (coordination.Timer{}).ReplayPolicy(effect); policy != agent.ReplayPolicyNever {
			t.Fatalf("unsupported operation received replay permission: %s", policy)
		}
	}
	if _, err := (coordination.Timer{}).Dispatch(t.Context(), agent.EffectRequest{}, nil); !errors.Is(err, coordination.ErrInvalidProtocol) {
		t.Fatalf("unowned timer request = %v", err)
	}
}

func mutatedState(t testing.TB, state agent.ExecutionState, field string, value any) agent.ExecutionState {
	t.Helper()
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(state.Payload(), &wire); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	wire[field] = encoded
	payload, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := agent.NewExecutionState(state.Kind(), payload)
	if err != nil {
		t.Fatal(err)
	}
	return changed
}

type interruptedTimer struct{}

func (interruptedTimer) ReplayPolicy(effect agent.Effect) agent.ReplayPolicy {
	return (coordination.Timer{}).ReplayPolicy(effect)
}

func (interruptedTimer) Dispatch(ctx context.Context, request agent.EffectRequest, emit agent.DeltaEmitter) (agent.Settlement, error) {
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	return (coordination.Timer{}).Dispatch(canceled, request, emit)
}
