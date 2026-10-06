package interaction

import (
	"bytes"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"math"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/chat"
)

func TestToolCallRequiresExplicitPosition(t *testing.T) {
	definition, err := newToolDefinition("interaction.position.tools", "Preserve Tool call attribution.")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name     string
		member   string
		position uint32
		valid    bool
	}{
		{name: "missing"},
		{name: "null", member: `"tool_call_index":null,`},
		{name: "zero", member: `"tool_call_index":0,`, valid: true},
		{name: "later", member: `"tool_call_index":7,`, position: 7, valid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			call := `{"model_call_sequence":1,` + test.member + `"call":{"id":"call","name":"inspect","arguments":"{}"}}`
			input, err := agent.ParsePayload([]byte(call))
			if err != nil {
				t.Fatal(err)
			}
			_, startErr := definition.Start(input)
			state, err := agent.ParseExecutionState(toolExecutionStateKind, []byte(`{"phase":"awaiting_result","call":`+call+`}`))
			if err != nil {
				t.Fatal(err)
			}
			restored, restoreErr := definition.Restore(t.Context(), state)
			_, dispatchErr := decodeEffect(json.RawMessage(`{"tool_call":{"invocation":` + call + `}}`))
			if !test.valid {
				if !errors.Is(startErr, ErrInvalidInput) || !errors.Is(restoreErr, ErrInvalidExecutionState) || !errors.Is(dispatchErr, ErrInvalidProtocol) {
					t.Fatalf("lost position accepted: start=%v restore=%v dispatch=%v", startErr, restoreErr, dispatchErr)
				}
				return
			}
			if startErr != nil || restoreErr != nil || dispatchErr != nil {
				t.Fatalf("explicit position rejected: start=%v restore=%v dispatch=%v", startErr, restoreErr, dispatchErr)
			}
			captured, err := restored.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := captured.Decode[toolExecutionState](toolExecutionStateKind)
			if err != nil || decoded.Call.ToolCallIndex != test.position {
				t.Fatalf("restored position = %d, error = %v", decoded.Call.ToolCallIndex, err)
			}
		})
	}
}

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
	for _, state := range []toolExecutionState{
		{Phase: toolReady, Call: call},
		{Phase: toolAwaitingResult, Call: call},
		{Phase: toolAwaitingResult, Call: call, Checkpoint: checkpoint},
		{Phase: toolAwaitingWaitOpen, Call: call, Checkpoint: checkpoint},
		{Phase: toolAwaitingWaitOpen, Call: call, Checkpoint: checkpoint, WaitID: &waitID},
		{Phase: toolCompleted, Call: call},
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
		{"interaction", &execution{state: executionState{Completed: true}}},
	} {
		t.Run(sample.name, func(t *testing.T) {
			_, stepErr := sample.execution.Step(t.Context(), []agent.Signal{unsolicited})
			failure, ok := agent.StepFailure(stepErr)
			if !ok {
				t.Fatalf("unclassified rejection: %v", stepErr)
			}
			if failure.Kind() != agent.FailureKindContract || failure.Code() != "interaction.state.invalid" {
				t.Fatalf("classification = %s/%s", failure.Kind(), failure.Code())
			}
		})
	}
}

func TestToolWaitingInputHasOnlyItsWaitIdentity(t *testing.T) {
	call := toolCall{ModelCallSequence: 1, Call: chat.ToolCall{ID: "call", Name: "inspect", Arguments: `{}`}}
	request, err := newToolInputRequest([]byte(`"confirm"`), []byte(`{"type":"boolean"}`), []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	waitID, err := agent.ParseWaitID("wait:tool-input")
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := &toolCheckpoint{PauseCount: 1, InputRequest: request}
	definition, err := newToolDefinition("interaction.waiting.tools", "Resume one waiting Tool.")
	if err != nil {
		t.Fatal(err)
	}
	state := toolExecutionState{Phase: toolAwaitingWaitOpen, Call: call, Checkpoint: checkpoint, WaitID: &waitID}
	captured, err := (&toolExecution{state: state}).Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := definition.Restore(t.Context(), captured)
	if err != nil {
		t.Fatal(err)
	}
	if restored.(*toolExecution).state.phase() != toolWaitingInput {
		t.Fatal("restored wait tried to accept a second opening")
	}
	state.Phase = toolWaitingInput
	legacy, err := (&toolExecution{state: state}).Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := definition.Restore(t.Context(), legacy); !errors.Is(err, ErrInvalidExecutionState) {
		t.Fatalf("accepted duplicate waiting phase: %v", err)
	}
}

func TestToolExecutionOwnsItsInputPauseCount(t *testing.T) {
	call := toolCall{ModelCallSequence: 1, Call: chat.ToolCall{ID: "call", Name: "inspect", Arguments: `{}`}}
	request, err := newToolInputRequest([]byte(`"confirm"`), []byte(`{"type":"boolean"}`), []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	execution := &toolExecution{state: toolExecutionState{Phase: toolAwaitingResult, Call: call}}
	for want := uint64(1); want <= 2; want++ {
		if _, err := execution.openInputWait(request); err != nil {
			t.Fatal(err)
		}
		if execution.state.Checkpoint.PauseCount != want {
			t.Fatalf("pause count = %d, want %d", execution.state.Checkpoint.PauseCount, want)
		}
	}
	execution.state.Checkpoint.PauseCount = math.MaxUint64
	if _, err := execution.openInputWait(request); !errors.Is(err, ErrInvalidExecutionState) {
		t.Fatalf("exhausted pause count = %v, want ErrInvalidExecutionState", err)
	}
}
