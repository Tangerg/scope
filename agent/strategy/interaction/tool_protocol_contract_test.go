package interaction

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/chat"
)

func TestToolDeploymentSeparatesInvocationAndCompletion(t *testing.T) {
	definition, err := newToolDefinition("interaction.contract.tools", "Separate invocation and completion.")
	if err != nil {
		t.Fatal(err)
	}
	call := toolCall{ModelCallSequence: 1, Call: chat.ToolCall{ID: "call", Name: "ask", Arguments: `{}`}}
	input, err := definition.Descriptor().EncodeInput(call)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = definition.Start(input); err != nil {
		t.Fatal(err)
	}
	for _, extra := range []string{`"checkpoint":null`, `"input_response":null`} {
		raw := bytes.TrimSuffix(input.JSON(), []byte("}"))
		raw = append(raw, []byte(","+extra+"}")...)
		invalid, parseErr := agent.ParseInput(raw)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		if definition.Descriptor().ValidateInput(invalid) == nil {
			t.Fatalf("input schema advertises continuation: %s", raw)
		}
		if _, startErr := definition.Start(invalid); startErr == nil {
			t.Fatalf("Start accepted continuation: %s", raw)
		}
	}
	output, err := agent.EncodeOutput(toolCallResult{Result: chat.ToolResult{ID: "call", Name: "ask", Output: chat.NewTextToolOutput("done")}})
	if err != nil {
		t.Fatal(err)
	}
	if err = definition.Descriptor().ValidateOutput(output); err != nil {
		t.Fatal(err)
	}
	paused, err := agent.ParseOutput(json.RawMessage(`{"checkpoint":{"pause_count":1},"direct":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if definition.Descriptor().ValidateOutput(paused) == nil {
		t.Fatal("completion schema advertises a paused result")
	}
}

func TestToolProtocolRejectsSupersededAndIncompleteShapes(t *testing.T) {
	for _, payload := range []string{
		`{"operation":"tool_call","tool_call":{"model_call_sequence":1,"call":{"id":"call","name":"ask","arguments":"{}"}}}`,
		`{"operation":"tool_call","tool_call":{"invocation":{"model_call_sequence":1,"call":{"id":"call","name":"ask","arguments":"{}"}},"resume":{}}}`,
	} {
		if _, err := decodeEffect(json.RawMessage(payload)); err == nil {
			t.Fatalf("accepted invalid dispatch request: %s", payload)
		}
	}
	if _, err := decodeSignal(json.RawMessage(`{"operation":"tool_call","tool_result":{"result":{"id":"call","name":"ask"},"direct":false}}`)); err == nil {
		t.Fatal("accepted superseded result encoding")
	}
}

func TestToolInputRequestJSONOwnsValidationAndIsolation(t *testing.T) {
	request, err := NewToolInputRequest(json.RawMessage(`{"id":9007199254740993}`), json.RawMessage(`{"type":"boolean"}`), json.RawMessage(`{"step":2}`))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var restored ToolInputRequest
	if err = json.Unmarshal(encoded, &restored); err != nil || !restored.equal(request) {
		t.Fatalf("request round trip: %s, %v", encoded, err)
	}
	clear(encoded)
	clear(restored.Prompt())
	clear(restored.ResponseSchema())
	clear(restored.ContinuationState())
	if !restored.equal(request) {
		t.Fatal("outward JSON mutated an immutable request")
	}
	for _, raw := range []string{
		`null`, `{}`, `{"prompt":null,"response_schema":false}`,
		`{"prompt":null,"response_schema":true,"continuation_state":null,"unknown":true}`,
		`{"prompt":null,"prompt":true,"response_schema":true,"continuation_state":null}`,
	} {
		if err = json.Unmarshal([]byte(raw), &restored); !errors.Is(err, ErrInvalidToolInputRequest) {
			t.Fatalf("invalid request %s: %v", raw, err)
		}
		if !restored.equal(request) {
			t.Fatal("failed decode changed the admitted request")
		}
	}
	if _, err = json.Marshal(ToolInputRequest{}); !errors.Is(err, ErrInvalidToolInputRequest) {
		t.Fatalf("zero request encoded: %v", err)
	}
	var absent *ToolInputRequest
	if err = absent.UnmarshalJSON([]byte(`{}`)); !errors.Is(err, ErrInvalidToolInputRequest) {
		t.Fatalf("nil receiver: %v", err)
	}
}
