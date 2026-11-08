package anthropic_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/invocation"
	toolcontract "github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/models/anthropic"
)

func TestMessagesRejectsInvalidToolSchemasBeforeIO(t *testing.T) {
	for _, test := range []struct {
		name   string
		input  json.RawMessage
		output json.RawMessage
	}{
		{name: "invalid input property type", input: json.RawMessage(`{"type":"object","properties":{"x":{"type":"nonsense"}}}`)},
		{name: "invalid output type", input: json.RawMessage(`{"type":"object"}`), output: json.RawMessage(`{"type":"nonsense"}`)},
		{name: "present empty output", input: json.RawMessage(`{"type":"object"}`), output: json.RawMessage{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := new(endpointTransport)
			model, err := anthropic.NewMessages(t.Context(), anthropic.MessagesConfig{
				APIKey: "test", HTTPClient: &http.Client{Transport: transport},
				DefaultOptions: corechat.Options{Model: "claude-test"},
			})
			if err != nil {
				t.Fatal(err)
			}
			request := &corechat.Request{
				Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("hello"))},
				Tools:    []toolcontract.Definition{{Name: "weather", InputSchema: test.input, OutputSchema: test.output}},
			}
			response, err := model.Call(t.Context(), request)
			if response != nil || !errors.Is(err, corechat.ErrInvalidRequest) || invocation.ClassifyFailure(err) != invocation.FailureNotExecuted || transport.calls != 0 {
				t.Fatalf("response=%v error=%v transport calls=%d", response, err, transport.calls)
			}
		})
	}
}
