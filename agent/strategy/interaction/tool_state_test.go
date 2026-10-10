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

// The model call that requested a Tool belongs to the child's ChildKey; an
// input, retained state, or dispatch request that restates it is rejected.
func TestToolCallCarriesNoRequestPosition(t *testing.T) {
	definition, err := newToolDefinition("interaction.position.tools", "Keep Tool call attribution in its ChildKey.")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		member string
		valid  bool
	}{
		{name: "call only", valid: true},
		{name: "model call sequence", member: `"model_call_sequence":1,`},
		{name: "tool call index", member: `"tool_call_index":0,`},
	} {
		t.Run(test.name, func(t *testing.T) {
			call := `{` + test.member + `"call":{"id":"call","name":"inspect","arguments":"{}"}}`
			input, err := agent.ParsePayload([]byte(call))
			if err != nil {
				t.Fatal(err)
			}
			_, startErr := definition.Start(input)
			state, err := agent.ParseExecutionState(toolExecutionStateKind, []byte(`{"phase":"awaiting_result","call":`+call+`}`))
			if err != nil {
				t.Fatal(err)
			}
			_, restoreErr := definition.Restore(t.Context(), state)
			_, dispatchErr := decodeEffect(json.RawMessage(`{"tool_call":{"invocation":` + call + `}}`))
			if !test.valid {
				if !errors.Is(startErr, ErrInvalidInput) || !errors.Is(restoreErr, ErrInvalidExecutionState) || !errors.Is(dispatchErr, ErrInvalidProtocol) {
					t.Fatalf("restated position accepted: start=%v restore=%v dispatch=%v", startErr, restoreErr, dispatchErr)
				}
				return
			}
			if startErr != nil || restoreErr != nil || dispatchErr != nil {
				t.Fatalf("plain call rejected: start=%v restore=%v dispatch=%v", startErr, restoreErr, dispatchErr)
			}
		})
	}
}

func FuzzToolExecutionStateRestore(f *testing.F) {
	definition, err := newToolDefinition("interaction.fuzz.tools", "Validate Tool continuation recovery.")
	if err != nil {
		f.Fatal(err)
	}
	call := toolCall{Call: chat.ToolCall{ID: "call", Name: "inspect", Arguments: `{}`}}
	waitID, err := agent.ParseWaitID("wait:tool-input")
	if err != nil {
		f.Fatal(err)
	}
	request, err := newToolInputRequest(json.RawMessage(`"confirm"`), json.RawMessage(`{"type":"boolean"}`), json.RawMessage(`{}`))
	if err != nil {
		f.Fatal(err)
	}
	for _, state := range []toolExecutionState{
		{Phase: toolReady, Call: call},
		{Phase: toolAwaitingResult, Call: call},
		{Phase: toolAwaitingWaitOpen, Call: call, InputRequest: &request},
		{Phase: toolAwaitingWaitOpen, Call: call, InputRequest: &request, WaitID: &waitID},
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
	call := toolCall{Call: chat.ToolCall{ID: "call", Name: "inspect", Arguments: `{}`}}
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
	call := toolCall{Call: chat.ToolCall{ID: "call", Name: "inspect", Arguments: `{}`}}
	request, err := newToolInputRequest([]byte(`"confirm"`), []byte(`{"type":"boolean"}`), []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	waitID, err := agent.ParseWaitID("wait:tool-input")
	if err != nil {
		t.Fatal(err)
	}
	definition, err := newToolDefinition("interaction.waiting.tools", "Resume one waiting Tool.")
	if err != nil {
		t.Fatal(err)
	}
	state := toolExecutionState{Phase: toolAwaitingWaitOpen, Call: call, InputRequest: &request, WaitID: &waitID}
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

// toolInputRequestsEqual compares the private fields of two requests for tests.
func toolInputRequestsEqual(a, b toolInputRequest) bool {
	return bytes.Equal(a.prompt, b.prompt) &&
		a.responseSchema.Equal(b.responseSchema) &&
		bytes.Equal(a.continuationState, b.continuationState)
}

func TestToolInputWaitKeepsOnlyTheCurrentRequest(t *testing.T) {
	call := toolCall{Call: chat.ToolCall{ID: "call", Name: "inspect", Arguments: `{}`}}
	request, err := newToolInputRequest([]byte(`"confirm"`), []byte(`{"type":"boolean"}`), []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	// A Tool child may pause for input more than once. Each pause keeps only its
	// current request, and dispatching the resume clears it, so no per-pause
	// identity accumulates.
	execution := &toolExecution{state: toolExecutionState{Phase: toolAwaitingResult, Call: call}}
	for range 2 {
		if _, err := execution.openInputWait(request); err != nil {
			t.Fatal(err)
		}
		if execution.state.Phase != toolAwaitingWaitOpen || execution.state.InputRequest == nil || !toolInputRequestsEqual(*execution.state.InputRequest, request) {
			t.Fatalf("state after openInputWait = %#v", execution.state)
		}
		if _, err := execution.request(0, toolDispatchRequest{Invocation: call}); err != nil {
			t.Fatal(err)
		}
		if execution.state.InputRequest != nil {
			t.Fatal("resume dispatch retained the old input request")
		}
	}
}
