package interaction

import (
	"bytes"
	"encoding/json"
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
	checkpoint := &toolCheckpoint{PauseCount: 1, InputRequest: inputRequestWire{
		Prompt: json.RawMessage(`"confirm"`), ResponseSchema: json.RawMessage(`{"type":"boolean"}`), ContinuationState: json.RawMessage(`{}`),
	}}
	result := &toolCallResult{Result: &chat.ToolResult{ID: "call", Name: "inspect", Output: chat.NewTextToolOutput("done")}}
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
		state, stateErr := agent.NewExecutionState(toolExecutionStateKind, payload)
		if stateErr != nil {
			return
		}
		execution, restoreErr := definition.Restore(state)
		if restoreErr != nil {
			return
		}
		captured, captureErr := execution.Snapshot()
		if captureErr != nil {
			t.Fatalf("accepted state cannot be captured: %v", captureErr)
		}
		restored, restoreErr := definition.Restore(captured)
		if restoreErr != nil {
			t.Fatalf("captured state cannot be restored: %v", restoreErr)
		}
		again, captureErr := restored.Snapshot()
		if captureErr != nil || !bytes.Equal(captured.Payload(), again.Payload()) {
			t.Fatalf("Tool continuation changed during round trip: %v", captureErr)
		}
	})
}
