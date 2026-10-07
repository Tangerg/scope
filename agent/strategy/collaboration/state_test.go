package collaboration

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	agent "github.com/Tangerg/scope/agent"
)

func TestTurnAndDecisionOwnProgressThroughRecovery(t *testing.T) {
	definition := fixture(func(_ context.Context, turn Turn) (Decision, error) {
		if turn.Number == 1 {
			return Decision{Mode: ModeWait, State: input("working"), Tasks: []TaskRequest{request("work", "test.echo", "value")}}, nil
		}
		if turn.Number != 2 || require(turn.State.Decode[string]()) != "working" || turn.Tasks[0].Outcome == nil {
			return Decision{}, errors.New("turn lost its predecessor decision or task outcome")
		}
		return Decision{State: input("finished"), Output: input("done")}, nil
	}, echo())
	instance := require(definition.Start(input("initial")))
	initial := require(instance.Snapshot())
	var fields map[string]json.RawMessage
	if err := jsonv2.Unmarshal(initial.Payload(), &fields); err != nil {
		t.Fatal(err)
	}
	if got := slices.Sorted(maps.Keys(fields)); !slices.Equal(got, []string{"initial_state"}) {
		t.Fatalf("initial state fields = %v", got)
	}
	restored := require(definition.Restore(t.Context(), initial))
	require(restored.Step(t.Context(), nil))
	started := require(restored.Snapshot())
	fields = nil
	if err := jsonv2.Unmarshal(started.Payload(), &fields); err != nil {
		t.Fatal(err)
	}
	if got := slices.Sorted(maps.Keys(fields)); !slices.Equal(got, []string{"turn"}) {
		t.Fatalf("started state fields = %v", got)
	}
	engine, process := run(t, definition, agent.NewMemoryTreeCommitter())
	if got := completed(t, process); got != "done" {
		t.Fatal(got)
	}
	tree := require(engine.InspectTree(t.Context(), process.ID()))
	root, _ := tree.Process(process.ID())
	state := root.Snapshot.CommittedExecutionState()
	fields = nil
	if err := jsonv2.Unmarshal(state.Payload(), &fields); err != nil {
		t.Fatal(err)
	}
	// The Engine owns the final Decision's Output, so only the marker remains.
	if got := slices.Sorted(maps.Keys(fields)); !slices.Equal(got, []string{"completed"}) {
		t.Fatalf("completed state fields = %v", got)
	}
	recovered := require(definition.Restore(t.Context(), state)).(*execution)
	if _, err := recovered.Step(t.Context(), nil); !errors.Is(err, ErrInvalidProtocol) {
		t.Fatalf("completed collaboration stepped again: %v", err)
	}
}

func TestUnlimitedTurnCounterStopsBeforeWrap(t *testing.T) {
	definition := fixture(func(_ context.Context, turn Turn) (Decision, error) { return finish(turn, "done"), nil }, echo())
	execution := require(definition.Start(input("initial"))).(*execution)
	require(execution.Step(t.Context(), nil))
	execution.state.Turn.Number = ^uint64(0)
	if _, err := execution.startTurn(0); !errors.Is(err, agent.ErrCounterExhausted) || execution.state.number() != ^uint64(0) {
		t.Fatalf("turn identity wrapped: %v", err)
	}
}

func TestRestoreIdentifiesInvalidTurnState(t *testing.T) {
	definition := fixture(func(_ context.Context, turn Turn) (Decision, error) { return finish(turn, "done"), nil }, echo())
	for _, test := range []struct {
		name    string
		mutate  func(*executionState)
		context string
		cause   error
	}{
		{
			name: "turn input schema",
			mutate: func(state *executionState) {
				state.Turn.State = require(agent.EncodePayload(42))
			},
			context: "turn input state",
			cause:   agent.ErrInvalidPayload,
		},
		{
			name: "unseen outcome",
			mutate: func(state *executionState) {
				state.Turn.OutcomeArrived = true
			},
			context: "an arrived task outcome is not recorded",
		},
		{
			name: "wait identity",
			mutate: func(state *executionState) {
				id := require(agent.ParseWaitID("unexpected"))
				state.WaitID = &id
			},
			context: "wait identity without an open wait",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			execution := require(definition.Start(input("initial"))).(*execution)
			require(execution.Step(t.Context(), nil))
			if _, err := definition.Restore(t.Context(), require(execution.Snapshot())); err != nil {
				t.Fatal(err)
			}
			test.mutate(&execution.state)
			state := require(agent.ParseExecutionState(stateKind, require(jsonv2.Marshal(execution.state))))
			_, err := definition.Restore(t.Context(), state)
			if !errors.Is(err, ErrInvalidExecutionState) || !strings.Contains(err.Error(), test.context) {
				t.Fatalf("Restore error = %v, want invalid state with %q", err, test.context)
			}
			if test.cause != nil && !errors.Is(err, test.cause) {
				t.Fatalf("Restore error = %v, want cause %v", err, test.cause)
			}
		})
	}
}
