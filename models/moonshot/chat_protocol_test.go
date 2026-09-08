package moonshot_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/moonshot"
	"github.com/Tangerg/scope/models/protocol/openai"
)

func TestAnthropicChatConstructorValidatesCredential(t *testing.T) {
	if _, err := moonshot.NewMessages(t.Context(), moonshot.MessagesConfig{}); err == nil {
		t.Fatal("NewMessages(t.Context(), ) accepted an absent credential")
	}
	model, err := moonshot.NewMessages(t.Context(), moonshot.MessagesConfig{APIKey: "test-key"})
	if err != nil {
		t.Fatal(err)
	}
	if model == nil {
		t.Fatal("NewMessages(t.Context(), ) = nil")
	}
}

func TestChatUsesCurrentKimiWireContract(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(writer, "invalid request", http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"id":"chat-1","model":"kimi-k3","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"answer","reasoning_content":"thinking"}}]}`))
	}))
	t.Cleanup(server.Close)

	maxTokens := int64(4096)
	model, err := moonshot.NewChat(t.Context(), moonshot.ChatConfig{
		APIKey:  "test-key",
		BaseURL: server.URL,
		DefaultOptions: corechat.Options{
			Model:           moonshot.ModelK3,
			MaxOutputTokens: &maxTokens,
		},
	})
	if err != nil {
		t.Fatalf("NewChat: %v", err)
	}
	previousThinking, err := openai.NewTextReasoningPart("moonshot", openai.TextReasoningContent, "previous thinking")
	if err != nil {
		t.Fatalf("NewTextReasoningPart: %v", err)
	}
	request := &corechat.Request{Messages: []corechat.Message{
		corechat.NewUserMessage(corechat.NewTextPart("continue")),
		corechat.NewAssistantMessage(previousThinking, corechat.NewTextPart("previous answer")),
		corechat.NewUserMessage(corechat.NewTextPart("again")),
	}}
	if setExtensionErr := request.Options.Extensions.Set(moonshot.RequestExtensionKey, moonshot.ChatRequestOptions{ReasoningEffort: moonshot.ReasoningEffortHigh}); setExtensionErr != nil {
		t.Fatalf("SetExtension: %v", setExtensionErr)
	}
	response, err := model.Call(t.Context(), request)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if body["max_completion_tokens"] != float64(maxTokens) {
		t.Fatalf("max_completion_tokens = %#v", body["max_completion_tokens"])
	}
	if _, found := body["max_tokens"]; found {
		t.Fatalf("deprecated max_tokens sent: %#v", body)
	}
	if body["reasoning_effort"] != string(moonshot.ReasoningEffortHigh) {
		t.Fatalf("reasoning_effort = %#v", body["reasoning_effort"])
	}
	messages := body["messages"].([]any)
	if messages[1].(map[string]any)["reasoning_content"] != "previous thinking" {
		t.Fatalf("assistant history = %#v", messages[1])
	}
	parts := response.Output.Message.Parts
	if len(parts) != 2 || parts[0].Kind != corechat.PartReasoning || parts[0].Text != "thinking" || parts[1].Text != "answer" {
		t.Fatalf("response parts = %#v", parts)
	}
}

func TestChatRejectsK2ThinkingOptionsForK3(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("request must fail before transport")
	}))
	t.Cleanup(server.Close)
	model, err := moonshot.NewChat(t.Context(), moonshot.ChatConfig{
		APIKey:         "test-key",
		BaseURL:        server.URL,
		DefaultOptions: corechat.Options{Model: moonshot.ModelK3},
	})
	if err != nil {
		t.Fatalf("NewChat: %v", err)
	}
	request := &corechat.Request{Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("hello"))}}
	if err := request.Options.Extensions.Set(moonshot.RequestExtensionKey, moonshot.ChatRequestOptions{
		Thinking: &moonshot.Thinking{Type: moonshot.ThinkingEnabled, Keep: moonshot.ThinkingKeepAll},
	}); err != nil {
		t.Fatalf("SetExtension: %v", err)
	}
	if _, err := model.Call(t.Context(), request); err == nil {
		t.Fatal("Call succeeded; want model-specific validation error")
	}
}
