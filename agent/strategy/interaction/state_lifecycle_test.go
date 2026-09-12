package interaction

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/chat"
)

func TestCanceledStepPreservesRecoveryState(t *testing.T) {
	definition := fuzzInteractionDefinition(t)
	input, err := agent.EncodeInput(Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("run"))}})
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := definition.Start(input)
	if err != nil {
		t.Fatal(err)
	}
	ready, err := fresh.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	states := append(fuzzInteractionStates(t, definition), ready)
	for _, state := range states {
		var decoded executionState
		if decodeErr := json.Unmarshal(state.Payload(), &decoded); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		t.Run(string(decoded.Phase), func(t *testing.T) {
			restored, restoreErr := definition.Restore(state)
			if restoreErr != nil {
				t.Fatal(restoreErr)
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			transition, stepErr := restored.Step(ctx, nil)
			if !errors.Is(stepErr, context.Canceled) || transition.Valid() {
				t.Fatalf("canceled Step = %+v, %v", transition, stepErr)
			}
			after, snapshotErr := restored.Snapshot()
			if snapshotErr != nil || !bytes.Equal(after.Payload(), state.Payload()) {
				t.Fatalf("canceled Step changed recovery state: %v", snapshotErr)
			}
		})
	}
}

func TestRestoreRejectsIncompleteLifecycleStates(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*executionState)
	}{
		{"active without round", func(state *executionState) { state.ToolRound = nil }},
		{"round without response", func(state *executionState) { state.ToolRound.Response = nil }},
		{"round without children", func(state *executionState) { state.ToolRound.ChildBatch = nil }},
		{"ready with round", func(state *executionState) { state.Phase = phaseReadyModel }},
		{"completed without output", func(state *executionState) { state.complete(Output{}); state.FinalOutput = nil }},
		{"output call count mismatch", func(state *executionState) {
			state.complete(Output{Source: CompletionSourceDirectToolResults, ModelCalls: 2,
				DirectToolResults: []chat.ToolResult{{ID: "call", Name: "direct", Output: chat.NewTextToolOutput("done")}}})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			execution := childBatchTestExecution(t, childCallsTool, phaseAwaitingChildStarts)
			test.change(&execution.state)
			state, stateErr := execution.state.snapshot()
			if stateErr != nil {
				t.Fatal(stateErr)
			}
			if _, restoreErr := execution.definition.Restore(state); !errors.Is(restoreErr, ErrInvalidExecutionState) {
				t.Fatalf("incomplete lifecycle state accepted: %v", restoreErr)
			}
		})
	}
}

func TestChildBatchSettlementIsAtomic(t *testing.T) {
	tools := advertisementTestDefinition(t).tools
	first := chat.ToolResult{ID: "first", Name: "tool", Output: chat.NewTextToolOutput("first result")}
	second := chat.ToolResult{ID: "second", Name: "tool", Output: chat.NewTextToolOutput("second result")}
	round := &toolCallRound{DirectResultEligible: true, ChildBatch: &childCallBatch{
		Kind: childCallsTool, Invocations: []childInvocationState{
			{Result: &toolCallResult{Result: first, Direct: true, AdvertisedToolNames: []string{"first"}}},
			{Result: &toolCallResult{Result: second, AdvertisedToolNames: []string{"duplicate", "duplicate"}}},
		},
	}}
	before, err := json.Marshal(round)
	if err != nil {
		t.Fatal(err)
	}
	if _, settleErr := round.finishChildren(tools, []string{"existing"}); settleErr == nil {
		t.Fatal("invalid advertisement accepted")
	}
	after, err := json.Marshal(round)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("failed settlement partially changed the tool round: %v", err)
	}
	round.ChildBatch.Invocations[1].Result.AdvertisedToolNames = []string{"second"}
	names, err := round.finishChildren(tools, []string{"existing"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(names, []string{"existing", "first", "second"}) || round.ChildBatch != nil ||
		round.DirectResultEligible || len(round.Results) != 2 || round.Results[0].ID != first.ID || round.Results[1].ID != second.ID {
		t.Fatalf("settlement lost order, advertisement, or direct-result eligibility: %+v, %v", round, names)
	}
}
