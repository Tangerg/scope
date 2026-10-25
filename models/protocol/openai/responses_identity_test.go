package openai_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/modeltest"
)

func TestResponsesRejectsContradictoryTerminalFacts(t *testing.T) {
	for _, test := range []struct {
		name, event, status, identity string
	}{
		{"changed ID", "completed", "completed", `"id":"other","model":"model","created_at":1`},
		{"changed model", "completed", "completed", `"id":"response-1","model":"other","created_at":1`},
		{"changed creation time", "completed", "completed", `"id":"response-1","model":"model","created_at":2`},
		{"failed event with completed status", "failed", "completed", `"id":"response-1","model":"model","created_at":1`},
		{"completed event with failed status", "completed", "failed", `"id":"response-1","model":"model","created_at":1`},
		{"incomplete event with completed status", "incomplete", "completed", `"id":"response-1","model":"model","created_at":1`},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := modeltest.AnthropicSSEServer([]modeltest.AnthropicEvent{
				{Event: "response.created", Data: `{"type":"response.created","response":{"id":"response-1","model":"model","created_at":1,"status":"in_progress"}}`},
				{Event: "response.output_text.delta", Data: `{"type":"response.output_text.delta","item_id":"message-1","output_index":0,"content_index":0,"delta":"answer"}`},
				{Event: "response." + test.event, Data: fmt.Sprintf(`{"type":"response.%s","response":{%s,"status":%q,"output":[]}}`, test.event, test.identity, test.status)},
			})
			defer server.Close()
			request := &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("question"))}}
			model := newResponsesModel(t, server.URL, "model")
			response, err := model.Call(t.Context(), request)
			if response != nil || !errors.Is(err, chat.ErrInvalidResponse) || chat.ClassifyFailure(err) != chat.CallFailureUnknown {
				t.Fatalf("response=%v error=%v", response, err)
			}
		})
	}
}
