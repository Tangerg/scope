package interaction

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/chat"
)

// A current round's successful Delegate result is its Artifact until the round
// ends, so Restore must admit it against the same OutputSchema the archived
// records use. Generic Tool-result validation cannot see that contract, so
// without the shared rule a structurally invalid output would be accepted the
// first time and only fail on a later Step or re-Restore.
func TestRoundDelegateResultRestoreValidatesOutputContract(t *testing.T) {
	definition := fuzzInteractionDefinition(t)
	request := &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("validate"))}}
	call := chat.ToolCall{ID: "call_fuzz", Name: "delegate_fuzz", Arguments: `{"task":"check"}`}
	message := chat.NewAssistantMessage(chat.NewToolCallPart(call))
	response := &chat.Response{Output: &chat.Output{Message: &message, FinishReason: chat.FinishReasonToolCalls}}

	roundState := func(output chat.ToolOutput) executionState {
		return executionState{
			WorkingContext: request.Clone(), ModelCallCount: 1,
			ToolRound: &toolCallRound{Response: response.Clone(), Results: []toolCallResult{{
				Disposition: ResultSucceeded, Output: output,
			}}},
		}
	}
	restore := func(t *testing.T, state executionState) (agent.Execution, error) {
		t.Helper()
		payload, err := jsonv2.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		envelope, err := agent.ParseExecutionState(executionStateKind, payload)
		if err != nil {
			t.Fatal(err)
		}
		return definition.Restore(t.Context(), envelope)
	}

	wrongSchema, err := chat.NewJSONToolOutput([]byte(`{"other":"invalid"}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name       string
		output     chat.ToolOutput
		wantSchema bool
	}{
		{"violates output schema", wrongSchema, true},
		{"missing structured details", chat.NewTextToolOutput("plain answer"), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, restoreErr := restore(t, roundState(test.output))
			if !errors.Is(restoreErr, ErrInvalidExecutionState) {
				t.Fatalf("Restore accepted an invalid Delegate output: %v", restoreErr)
			}
			if test.wantSchema && !errors.Is(restoreErr, agent.ErrSchemaValidation) {
				t.Fatalf("schema cause lost: %v", restoreErr)
			}
		})
	}

	valid, err := chat.NewJSONToolOutput([]byte(`{"result":"ok"}`))
	if err != nil {
		t.Fatal(err)
	}
	execution, err := restore(t, roundState(valid))
	if err != nil {
		t.Fatalf("Restore rejected a conforming Delegate output: %v", err)
	}
	if _, err := execution.Snapshot(); err != nil {
		t.Fatalf("conforming round state cannot be captured: %v", err)
	}
}
