package collaboration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	agent "github.com/Tangerg/scope/agent"
)

func TestUnlimitedTurnAndWaitCountersStopBeforeWrap(t *testing.T) {
	definition, _ := fixture(func(_ context.Context, turn Turn) (Decision, error) { return finish(turn, "done"), nil }, echo())
	execution := require(definition.Start(input("initial"))).(*execution)
	execution.state.Number = ^uint64(0)
	if _, err := execution.startTurn(0); !errors.Is(err, agent.ErrCounterExhausted) || execution.state.Number != ^uint64(0) {
		t.Fatalf("turn identity wrapped: %v", err)
	}
	execution.state.WaitSequence = ^uint64(0)
	if _, err := execution.openWait(0); !errors.Is(err, agent.ErrCounterExhausted) || execution.state.WaitSequence != ^uint64(0) {
		t.Fatalf("wait identity wrapped: %v", err)
	}
}

func TestRestoreIdentifiesInvalidTurnState(t *testing.T) {
	definition, _ := fixture(func(_ context.Context, turn Turn) (Decision, error) { return finish(turn, "done"), nil }, echo())
	for _, test := range []struct {
		name    string
		mutate  func(*executionState)
		context string
		cause   error
	}{
		{
			name: "turn input schema",
			mutate: func(state *executionState) {
				state.Turn.Input.State = require(agent.EncodePayload(42))
			},
			context: "turn input state",
			cause:   agent.ErrInvalidPayload,
		},
		{
			name: "worker binding",
			mutate: func(state *executionState) {
				state.Turn.Input.Workers = nil
			},
			context: "turn workers do not match the definition",
		},
		{
			name: "wait identity",
			mutate: func(state *executionState) {
				id := require(agent.ParseWaitID("unexpected"))
				state.WaitID = &id
			},
			context: "wait identity does not match phase",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			execution := require(definition.Start(input("initial"))).(*execution)
			require(execution.Step(t.Context(), nil))
			if _, err := definition.Restore(t.Context(), require(execution.Snapshot())); err != nil {
				t.Fatal(err)
			}
			test.mutate(&execution.state)
			state := require(agent.NewExecutionState(stateKind, require(json.Marshal(execution.state))))
			_, err := definition.Restore(t.Context(), state)
			if !errors.Is(err, ErrInvalidState) || !strings.Contains(err.Error(), test.context) {
				t.Fatalf("Restore error = %v, want invalid state with %q", err, test.context)
			}
			if test.cause != nil && !errors.Is(err, test.cause) {
				t.Fatalf("Restore error = %v, want cause %v", err, test.cause)
			}
		})
	}
}
