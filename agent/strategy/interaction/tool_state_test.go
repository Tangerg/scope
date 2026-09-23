package interaction

import (
	"bytes"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/chat"
)

func FuzzToolExecutionStateRestore(f *testing.F) {
	definition, err := newToolDefinition("interaction.fuzz.tools", "Validate Tool continuation recovery.")
	if err != nil {
		f.Fatal(err)
	}
	call := toolCall{ModelCallSequence: 1, Call: chat.ToolCall{ID: "call", Name: "inspect", Arguments: `{}`}}
	waitID, err := agent.ParseWaitID("wait:tool-input")
	if err != nil {
		f.Fatal(err)
	}
	request, err := newToolInputRequest(json.RawMessage(`"confirm"`), json.RawMessage(`{"type":"boolean"}`), json.RawMessage(`{}`))
	if err != nil {
		f.Fatal(err)
	}
	checkpoint := &toolCheckpoint{PauseCount: 1, InputRequest: request}
	result := &toolCallResult{Result: chat.ToolResult{ID: "call", Name: "inspect", Output: chat.NewTextToolOutput("done")}}
	for _, state := range []toolExecutionState{
		{Phase: toolReady, Call: call},
		{Phase: toolAwaitingResult, Call: call},
		{Phase: toolAwaitingResult, Call: call, Checkpoint: checkpoint},
		{Phase: toolAwaitingWaitOpen, Call: call, Checkpoint: checkpoint},
		{Phase: toolWaitingInput, Call: call, Checkpoint: checkpoint, WaitID: &waitID},
		{Phase: toolCompleted, Call: call, Result: result},
	} {
		captured, captureErr := (&toolExecution{state: state}).Snapshot()
		if captureErr != nil {
			f.Fatal(captureErr)
		}
		f.Add([]byte(captured.Payload()))
	}
	f.Add([]byte(`null`))
	f.Add([]byte(`{"phase":"waiting_input"}`))
	f.Add([]byte(`{"phase":"ready","unknown":true}`))
	f.Fuzz(func(t *testing.T, payload []byte) {
		state, stateErr := agent.ParseExecutionState(toolExecutionStateKind, payload)
		if stateErr != nil {
			return
		}
		execution, restoreErr := definition.Restore(t.Context(), state)
		if restoreErr != nil {
			return
		}
		captured, captureErr := execution.Snapshot()
		if captureErr != nil {
			t.Fatalf("accepted state cannot be captured: %v", captureErr)
		}
		restored, restoreErr := definition.Restore(t.Context(), captured)
		if restoreErr != nil {
			t.Fatalf("captured state cannot be restored: %v", restoreErr)
		}
		again, captureErr := restored.Snapshot()
		if captureErr != nil || !bytes.Equal(captured.Payload(), again.Payload()) {
			t.Fatalf("Tool continuation changed during round trip: %v", captureErr)
		}
	})
}

// A Tool child and its parent Interaction reject the same domain violation, so
// they must persist the same Failure. Divergence here reaches the Host as two
// unrelated codes for one contract.
func TestToolAndInteractionShareRejectionClassification(t *testing.T) {
	var unsolicited agent.Signal
	if err := jsonv2.Unmarshal([]byte(`{"id":"signal:unsolicited","payload":"x"}`), &unsolicited); err != nil {
		t.Fatal(err)
	}
	call := toolCall{ModelCallSequence: 1, Call: chat.ToolCall{ID: "call", Name: "inspect", Arguments: `{}`}}
	for _, sample := range []struct {
		name      string
		execution agent.Execution
	}{
		{"tool", &toolExecution{state: toolExecutionState{Phase: toolReady, Call: call}}},
		{"interaction", &execution{state: executionState{Phase: phaseCompleted}}},
	} {
		t.Run(sample.name, func(t *testing.T) {
			_, stepErr := sample.execution.Step(t.Context(), []agent.Signal{unsolicited})
			classified, ok := errors.AsType[*agent.StepError](stepErr)
			if !ok {
				t.Fatalf("unclassified rejection: %v", stepErr)
			}
			if classified.Failure.Kind() != agent.FailureKindContract ||
				classified.Failure.Code() != "interaction.state.invalid" {
				t.Fatalf("classification = %s/%s", classified.Failure.Kind(), classified.Failure.Code())
			}
		})
	}
}
