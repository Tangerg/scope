package google_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/chat"
)

func TestNativeChatRetainsExactToolAndSchemaNumbers(t *testing.T) {
	for _, provider := range []string{"google", "vertexai"} {
		t.Run(provider, func(t *testing.T) {
			var body []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var readErr error
				body, readErr = io.ReadAll(r.Body)
				if readErr != nil {
					t.Error(readErr)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: "+`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"next","name":"lookup","args":{"id":9007199254740993,"ratio":12345678901234567890.0123456789}}}]} ,"finishReason":"STOP"}]}`+"\n\n")
			}))
			defer server.Close()
			model := newNativeChat(t, provider, server)
			request, err := chat.NewRequest(chat.NewUserMessage(chat.NewTextPart("lookup")), chat.NewAssistantMessage(chat.NewToolCallPart(chat.ToolCall{ID: "call", Name: "lookup", Arguments: `{"id":9007199254740993}`})), chat.NewToolMessage(chat.ToolResult{ID: "call", Name: "lookup", Output: chat.ToolOutput{Details: json.RawMessage(`{"id":9007199254740993}`)}}))
			if err != nil {
				t.Fatal(err)
			}
			const schema = `{"type":"object","properties":{"id":{"const":9007199254740993}},"required":["id"]}`
			request.Tools = []chat.ToolDefinition{{Name: "lookup", InputSchema: json.RawMessage(schema)}}
			format, err := chat.NewJSONSchemaOutputFormat(chat.JSONSchemaConfig{Name: "answer", Schema: json.RawMessage(schema)})
			if err != nil {
				t.Fatal(err)
			}
			request.Options.OutputFormat = &format
			response, err := model.Call(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			wire := string(body)
			if strings.Count(wire, `"const":9007199254740993`) != 2 || strings.Count(wire, `"id":9007199254740993`) != 2 {
				t.Fatalf("lossy request: %s", wire)
			}
			var arguments string
			for _, part := range response.Output.Message.Parts {
				if part.ToolCall != nil {
					arguments = part.ToolCall.Arguments
				}
			}
			if !strings.Contains(arguments, `"id":9007199254740993`) || !strings.Contains(arguments, `"ratio":12345678901234567890.0123456789`) {
				t.Fatalf("lossy model arguments: %s", arguments)
			}
		})
	}
}
