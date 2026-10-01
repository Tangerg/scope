package xai_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/xai"
)

func TestChatPreservesReasoningContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		for _, chunk := range []string{
			`{"id":"chat-1","model":"grok","choices":[{"index":0,"delta":{"reasoning_content":"visible "}}]}`,
			`{"id":"chat-1","model":"grok","choices":[{"index":0,"delta":{"reasoning_content":"reasoning"}}]}`,
			`{"id":"chat-1","model":"grok","choices":[{"index":0,"delta":{"content":"answer"},"finish_reason":"stop"}]}`,
		} {
			fmt.Fprintf(writer, "data: %s\n\n", chunk)
		}
		fmt.Fprint(writer, "data: [DONE]\n\n")
	}))
	defer server.Close()
	model, err := xai.NewChatCompletions(t.Context(), xai.ChatCompletionsConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: chat.Options{Model: "grok"}})
	if err != nil {
		t.Fatal(err)
	}
	response, err := model.Call(t.Context(), &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("hello"))}})
	if err != nil {
		t.Fatal(err)
	}
	parts := response.Output.Message.Parts
	if len(parts) != 2 || parts[0].Kind != chat.PartReasoning || parts[0].Text != "visible reasoning" || parts[1].Text != "answer" {
		t.Fatalf("parts=%#v", parts)
	}
}
