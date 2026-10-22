package interaction

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/core/chat"
)

func TestModelRequestCannotDeclareToolAuthority(t *testing.T) {
	definition, err := NewDefinition(DefinitionConfig{Name: "model.authority", Description: "Keep tool authority in the Deployment."})
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := NewDispatcher(definition, DispatcherConfig{Model: chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		t.Error("model called with a request that declares tool authority")
		return &chat.Response{Output: &chat.Output{FinishReason: chat.FinishReasonStop}}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{
		`"tools":[]`,
		`"tools":[{"name":"injected","description":"An unbound tool.","input_schema":{"type":"object"}}]`,
		`"tool_choice":null`,
	} {
		t.Run(field, func(t *testing.T) {
			request := `{"messages":[{"role":"user","parts":[{"kind":"text","text":"run"}]}],` + field + `}`
			payload := json.RawMessage(`{"model_call":{"model_call_sequence":1,"request":` + request + `}}`)
			if _, err := decodeEffect(payload); !errors.Is(err, ErrInvalidProtocol) || !errors.Is(err, jsonv2.ErrUnknownName) {
				t.Errorf("decodeEffect = %v, want an unknown-member protocol error", err)
			}
			effect, err := agent.NewDispatcherEffect(payload)
			if err != nil {
				t.Fatal(err)
			}
			state, err := agent.ParseExecutionState(executionStateKind, json.RawMessage(`{"working_context":`+request+`,"model_call_count":0}`))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := definition.Restore(t.Context(), state); !errors.Is(err, ErrInvalidExecutionState) || !errors.Is(err, jsonv2.ErrUnknownName) {
				t.Errorf("Restore = %v, want an unknown-member state error", err)
			}
			conformancetest.CheckDispatcherRejection(t, dispatcher, effect)
		})
	}
}
