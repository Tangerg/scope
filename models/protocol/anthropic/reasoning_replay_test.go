package anthropic_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/protocol/anthropic"
)

func TestChat_OmitsUnsignedReasoningFromPortableHistory(t *testing.T) {
	var wireRequest struct {
		Messages []struct {
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
			} `json:"content"`
		} `json:"messages"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&wireRequest); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(writer, "invalid request", http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"id":"msg-1","type":"message","role":"assistant","model":"claude-test","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	t.Cleanup(server.Close)

	model, err := anthropic.NewMessages(t.Context(), anthropic.MessagesConfig{
		APIKey:         "test-key",
		DefaultOptions: corechat.Options{Model: "claude-test"},
		BaseURL:        server.URL,
	})
	if err != nil {
		t.Fatalf("NewMessages: %v", err)
	}
	_, err = model.Call(t.Context(), &corechat.Request{Messages: []corechat.Message{
		corechat.NewUserMessage(corechat.NewTextPart("first")),
		corechat.NewAssistantMessage(
			corechat.NewReasoningPart("provider-private reasoning", nil),
			corechat.NewTextPart("first answer"),
		),
		corechat.NewUserMessage(corechat.NewTextPart("second")),
	}})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if len(wireRequest.Messages) != 3 {
		t.Fatalf("wire messages = %d; want 3", len(wireRequest.Messages))
	}
	for _, block := range wireRequest.Messages[1].Content {
		if block.Type == "thinking" {
			t.Fatalf("unsigned reasoning was replayed: %#v", wireRequest.Messages[1].Content)
		}
	}
}
