package interaction

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/tool"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/chat"
)

func TestToolDeploymentSeparatesInvocationAndCompletion(t *testing.T) {
	definition, err := newToolDefinition("interaction.contract.tools", "Separate invocation and completion.")
	if err != nil {
		t.Fatal(err)
	}
	call := toolCall{Call: chat.ToolCall{ID: "call", Name: "ask", Arguments: `{}`}}
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
		invalid, parseErr := agent.ParsePayload(raw)
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
	output, err := agent.EncodePayload(toolCallResult{Disposition: ResultSucceeded, Output: chat.NewTextToolOutput("done")})
	if err != nil {
		t.Fatal(err)
	}
	if err = definition.Descriptor().ValidateOutput(output); err != nil {
		t.Fatal(err)
	}
	paused, err := agent.ParsePayload(json.RawMessage(`{"input_request":{"prompt":"x"},"direct":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if definition.Descriptor().ValidateOutput(paused) == nil {
		t.Fatal("completion schema advertises a paused result")
	}
}

func TestToolProtocolRejectsSupersededAndIncompleteShapes(t *testing.T) {
	for _, payload := range []string{
		`{"tool_call":{"model_call_sequence":1,"call":{"id":"call","name":"ask","arguments":"{}"}}}`,
		`{"tool_call":{"invocation":{"model_call_sequence":1,"call":{"id":"call","name":"ask","arguments":"{}"}},"resume":{}}}`,
	} {
		if _, err := decodeEffect(json.RawMessage(payload)); err == nil {
			t.Fatalf("accepted invalid dispatch request: %s", payload)
		}
	}
	if _, err := decodeSignal(json.RawMessage(`{"tool_result":{"result":{"id":"call","name":"ask"},"direct":false}}`)); err == nil {
		t.Fatal("accepted superseded result encoding")
	}
}

func TestToolInputRequestJSONOwnsValidationAndIsolation(t *testing.T) {
	request, err := newInputRequest(json.RawMessage(`{"id":9007199254740993}`), json.RawMessage(`{"type":"boolean"}`), json.RawMessage(`{"step":2}`))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := jsonv2.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"prompt":{"id":9007199254740993},"response_schema":{"type":"boolean"},"continuation_state":{"step":2}}`
	if string(encoded) != want {
		t.Fatalf("checkpoint JSON = %s, want %s", encoded, want)
	}
	var restored tool.InputRequest
	if err = jsonv2.Unmarshal(encoded, &restored); err != nil || !inputRequestsEqual(restored, request) {
		t.Fatalf("request round trip: %s, %v", encoded, err)
	}
	clear(encoded)
	if !inputRequestsEqual(restored, request) {
		t.Fatal("outward JSON mutated an immutable request")
	}
	for _, test := range []struct {
		raw  string
		want error
	}{
		{`null`, tool.ErrInvalidInputRequest},
		{`{}`, tool.ErrInvalidInputRequest},
		{`{"prompt":null,"response_schema":false}`, tool.ErrInvalidInputRequest},
		{`{"prompt":null,"response_schema":true,"continuation_state":null,"unknown":true}`, tool.ErrInvalidInputRequest},
		{`{"prompt":null,"prompt":true,"response_schema":true,"continuation_state":null}`, jsontext.ErrDuplicateName},
	} {
		if err = jsonv2.Unmarshal([]byte(test.raw), &restored); !errors.Is(err, test.want) {
			t.Fatalf("invalid request %s: %v, want %v", test.raw, err, test.want)
		}
		if !inputRequestsEqual(restored, request) {
			t.Fatal("failed decode changed the admitted request")
		}
	}
	if _, err = jsonv2.Marshal(tool.InputRequest{}); !errors.Is(err, tool.ErrInvalidInputRequest) {
		t.Fatalf("zero request encoded: %v", err)
	}
	var absent *tool.InputRequest
	if err = absent.UnmarshalJSON([]byte(`{}`)); !errors.Is(err, tool.ErrInvalidInputRequest) {
		t.Fatalf("nil receiver: %v", err)
	}
}

func TestToolInputRequestPreservesJSONNumbers(t *testing.T) {
	for _, value := range []string{"9007199254740993", "18446744073709551615", "1.234567890123456789", "1e400"} {
		t.Run(value, func(t *testing.T) {
			schema := `{"const":` + value + `}`
			request, err := newInputRequest(json.RawMessage(value), json.RawMessage(schema), json.RawMessage(value))
			if err != nil {
				t.Fatal(err)
			}
			if string(request.Prompt()) != value || string(request.ResponseSchema().JSON()) != schema || string(request.State()) != value {
				t.Fatalf("request changed JSON numbers: prompt=%s schema=%s continuation=%s", request.Prompt(), request.ResponseSchema().JSON(), request.State())
			}
		})
	}
}

func TestProtocolRejectionsHaveStableClassification(t *testing.T) {
	for _, payload := range []json.RawMessage{
		[]byte(`{"operation":"unsupported"}`),
		[]byte(`{"tool_call":{}}`),
		[]byte(`{"extra":true}`),
	} {
		if _, err := decodeEffect(payload); !errors.Is(err, ErrInvalidProtocol) {
			t.Fatalf("effect rejection has no protocol classification: %v", err)
		}
		if _, err := decodeSignal(payload); !errors.Is(err, ErrInvalidProtocol) {
			t.Fatalf("signal rejection has no protocol classification: %v", err)
		}
	}
}
