package interaction

import (
	"bytes"
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/chat"
)

func TestRestoreCannotResetMissingModelCallCount(t *testing.T) {
	definition := fuzzInteractionDefinition(t)
	input, err := agent.EncodePayload(Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("run"))}})
	if err != nil {
		t.Fatal(err)
	}
	execution, err := definition.Start(input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = execution.Step(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	state, err := execution.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"missing", "null"} {
		t.Run(mode, func(t *testing.T) {
			var fields map[string]json.RawMessage
			if err := jsonv2.Unmarshal(state.Payload(), &fields); err != nil {
				t.Fatal(err)
			}
			delete(fields, "model_call_count")
			if mode == "null" {
				fields["model_call_count"] = json.RawMessage(`null`)
			}
			corrupted, err := agent.EncodeExecutionState(state.Kind(), fields)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := definition.Restore(t.Context(), corrupted); !errors.Is(err, ErrInvalidExecutionState) {
				t.Fatalf("lost model progress was restored: %v", err)
			}
		})
	}
}

func TestCanceledStepPreservesRecoveryState(t *testing.T) {
	definition := fuzzInteractionDefinition(t)
	input, err := agent.EncodePayload(Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("run"))}})
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
		if decodeErr := jsonv2.Unmarshal(state.Payload(), &decoded); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		t.Run(fmt.Sprintf("phase_%d", decoded.phase()), func(t *testing.T) {
			restored, restoreErr := definition.Restore(t.Context(), state)
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
		{"round without response", func(state *executionState) { state.ToolRound.Response = nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			execution := childBatchTestExecution(t, childCallsTool, phaseAwaitingChildStarts)
			test.change(&execution.state)
			state, stateErr := execution.state.snapshot()
			if stateErr != nil {
				t.Fatal(stateErr)
			}
			if _, restoreErr := execution.definition.Restore(t.Context(), state); !errors.Is(restoreErr, ErrInvalidExecutionState) {
				t.Fatalf("incomplete lifecycle state accepted: %v", restoreErr)
			}
		})
	}
}

func TestRestoreValidatesCompleteRoundAdmission(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*executionState)
	}{
		{"complete", func(*executionState) {}},
		{"foreign result", func(state *executionState) { state.ToolRound.Results[0].Result.ID = "other" }},
		{"rejected success", func(state *executionState) { state.ToolRound.Results[0].Rejected = true }},
		{"direct failure", func(state *executionState) {
			state.ToolRound.Results[0].Direct = true
			state.ToolRound.Results[0].Result.IsError = true
		}},
		{"truncated execution", func(state *executionState) { state.ToolRound.Response.Output.FinishReason = chat.FinishReasonLength }},
		{"unfinished child", func(state *executionState) { state.ToolRound.ChildBatch = &childCallBatch{} }},
		{"completed with pending work", func(state *executionState) { state.Completed = true }},
	} {
		t.Run(test.name, func(t *testing.T) {
			execution := childBatchTestExecution(t, childCallsTool, phaseAwaitingChildStarts)
			execution.state.ToolRound.ChildBatch = nil
			execution.state.ToolRound.Results = []toolCallResult{{Result: chat.ToolResult{ID: "call_batch", Name: "delegate_fuzz", Output: chat.NewTextToolOutput("done")}}}
			test.change(&execution.state)
			state, err := execution.state.snapshot()
			if err != nil {
				t.Fatal(err)
			}
			_, restoreErr := execution.definition.Restore(t.Context(), state)
			if test.name == "complete" && restoreErr != nil || test.name != "complete" && !errors.Is(restoreErr, ErrInvalidExecutionState) {
				t.Fatalf("restore complete round: %v", restoreErr)
			}
		})
	}
}

func TestChildBatchSettlementIsAtomic(t *testing.T) {
	tools := advertisementTestDefinition(t).tools
	first := chat.ToolResult{ID: "first", Name: "tool", Output: chat.NewTextToolOutput("first result")}
	second := chat.ToolResult{ID: "second", Name: "tool", Output: chat.NewTextToolOutput("second result")}
	round := &toolCallRound{ChildBatch: &childCallBatch{
		Kind: childCallsTool, Invocations: []childInvocationState{
			{Result: &toolCallResult{Result: first, Direct: true, AdvertisedToolNames: []string{"first"}}},
			{Result: &toolCallResult{Result: second, AdvertisedToolNames: []string{"duplicate", "duplicate"}}},
		},
	}}
	before, err := jsonv2.Marshal(round)
	if err != nil {
		t.Fatal(err)
	}
	if _, settleErr := round.finishChildren(tools, []string{"existing"}); settleErr == nil {
		t.Fatal("invalid advertisement accepted")
	}
	after, err := jsonv2.Marshal(round)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("failed settlement partially changed the tool round: %v", err)
	}
	round.ChildBatch.Invocations[1].Result.AdvertisedToolNames = []string{"second"}
	names, err := round.finishChildren(tools, []string{"existing"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(names, []string{"existing", "first", "second"}) || round.ChildBatch != nil ||
		len(round.Results) != 2 || round.Results[0].Result.ID != first.ID || round.Results[1].Result.ID != second.ID {
		t.Fatalf("settlement lost order, advertisement, or direct-result eligibility: %+v, %v", round, names)
	}
}
