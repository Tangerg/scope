package coordination_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/coordination"
)

func TestCoordinationProtocolRejectsPrematureAndMissingSignals(t *testing.T) {
	timer := deadlineBinding(t, coordination.DeadlineDispatcher{})
	deadline := encodedInput(t, time.Date(2026, time.September, 9, 12, 0, 0, 0, time.UTC))
	first := competition(t, func(context.Context, agent.ChildKey, agent.ChildOutcome) (bool, error) { return true, nil }, 1)
	for _, sample := range []struct {
		name       string
		definition agent.Definition
		input      agent.Payload
	}{
		{"deadline", timer.Definition(), deadline},
		{"gate", inputGate(t), encodedInput(t, "request")},
		{"competition", first, encodedInput(t, []agent.ChildSpec{candidate(t, "one", timer, deadline)})},
	} {
		t.Run(sample.name, func(t *testing.T) {
			execution, err := sample.definition.Start(sample.input)
			if err != nil {
				t.Fatal(err)
			}
			initial, err := execution.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if transition, stepErr := execution.Step(ctx, nil); transition.Valid() || !errors.Is(stepErr, context.Canceled) {
				t.Fatalf("canceled step = %v, %v", transition, stepErr)
			}
			if transition, stepErr := execution.Step(t.Context(), []agent.Signal{externalSignal(t, "", "premature answer")}); transition.Valid() || !errors.Is(stepErr, coordination.ErrInvalidProtocol) {
				t.Fatalf("premature signal = %v, %v", transition, stepErr)
			}
			after, err := execution.Snapshot()
			if err != nil || !bytes.Equal(initial.Payload(), after.Payload()) {
				t.Fatalf("rejected step changed initial state: %s, %v", after.Payload(), err)
			}
			if _, stepErr := execution.Step(t.Context(), nil); stepErr != nil {
				t.Fatal(stepErr)
			}
			if transition, stepErr := execution.Step(t.Context(), nil); transition.Valid() || !errors.Is(stepErr, coordination.ErrInvalidProtocol) {
				t.Fatalf("missing acknowledgement = %v, %v", transition, stepErr)
			}
			completed := mutatedState(t, initial, "phase", "completed")
			if sample.name == "competition" {
				completed = mutatedState(t, initial, "completed", true)
			}
			restored, err := sample.definition.Restore(t.Context(), completed)
			if err != nil {
				t.Fatal(err)
			}
			if transition, err := restored.Step(t.Context(), nil); transition.Valid() || !errors.Is(err, coordination.ErrInvalidProtocol) {
				t.Fatalf("completed execution advanced = %v, %v", transition, err)
			}
		})
	}
}

func TestInputGateRequiresTheAddressedWaitOnRestore(t *testing.T) {
	definition := inputGate(t)
	execution, err := definition.Start(encodedInput(t, "request"))
	if err != nil {
		t.Fatal(err)
	}
	initial, err := execution.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	waiting := mutatedState(t, mutatedState(t, initial, "phase", "awaiting_open"), "wait_id", "wait:answer")
	for _, signals := range [][]agent.Signal{
		nil,
		{externalSignal(t, "", "unaddressed")},
		{externalSignal(t, "wait:other", "wrong wait")},
	} {
		restored, err := definition.Restore(t.Context(), waiting)
		if err != nil {
			t.Fatal(err)
		}
		if transition, stepErr := restored.Step(t.Context(), signals); transition.Valid() || !errors.Is(stepErr, coordination.ErrInvalidProtocol) {
			t.Fatalf("unaddressed answer = %v, %v", transition, stepErr)
		}
		after, err := restored.Snapshot()
		if err != nil || !bytes.Equal(waiting.Payload(), after.Payload()) {
			t.Fatalf("invalid answer changed pending wait: %s, %v", after.Payload(), err)
		}
	}
	invalid := mutatedState(t, initial, "request", 1)
	if _, err := definition.Restore(t.Context(), invalid); !errors.Is(err, coordination.ErrInvalidExecutionState) {
		t.Fatalf("restored request violated its frozen schema: %v", err)
	}
}
