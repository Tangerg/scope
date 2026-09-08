package xiaomi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/protocol/openai"
	"github.com/Tangerg/scope/models/xiaomi"
)

func TestAnthropicChatConstructorValidatesCredential(t *testing.T) {
	if _, err := xiaomi.NewMessages(t.Context(), xiaomi.MessagesConfig{}); err == nil {
		t.Fatal("NewMessages(t.Context(), ) accepted an absent credential")
	}
	model, err := xiaomi.NewMessages(t.Context(), xiaomi.MessagesConfig{APIKey: "test-key"})
	if err != nil {
		t.Fatal(err)
	}
	if model == nil {
		t.Fatal("NewMessages(t.Context(), ) = nil")
	}
}

func TestChatUsesMiMoThinkingAndToolReasoningContract(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(writer, "invalid request", http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"id":"chat-1","model":"mimo-v2.5-pro","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"answer","reasoning_content":"thinking"}}]}`))
	}))
	t.Cleanup(server.Close)

	maxTokens := int64(4096)
	model, err := xiaomi.NewChat(t.Context(), xiaomi.ChatConfig{
		APIKey:  "test-key",
		BaseURL: server.URL,
		DefaultOptions: corechat.Options{
			Model:           xiaomi.ModelV25Pro,
			MaxOutputTokens: &maxTokens,
		},
	})
	if err != nil {
		t.Fatalf("NewChat: %v", err)
	}
	toolReasoning, err := openai.NewTextReasoningPart("xiaomi", openai.TextReasoningContent, "tool thinking")
	if err != nil {
		t.Fatalf("NewTextReasoningPart: %v", err)
	}
	request := &corechat.Request{Messages: []corechat.Message{
		corechat.NewUserMessage(corechat.NewTextPart("look up")),
		corechat.NewAssistantMessage(
			toolReasoning,
			corechat.NewToolCallPart(corechat.ToolCall{ID: "call-1", Name: "lookup", Arguments: `{}`}),
		),
		corechat.NewToolMessage(corechat.ToolResult{
			ID: "call-1", Name: "lookup", Output: corechat.NewTextToolOutput("found"),
		}),
	}}
	if setExtensionErr := request.Options.Extensions.Set(xiaomi.RequestExtensionKey, xiaomi.ChatRequestOptions{Thinking: xiaomi.ThinkingEnabled}); setExtensionErr != nil {
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
	thinking := body["thinking"].(map[string]any)
	if thinking["type"] != string(xiaomi.ThinkingEnabled) {
		t.Fatalf("thinking = %#v", thinking)
	}
	messages := body["messages"].([]any)
	if messages[1].(map[string]any)["reasoning_content"] != "tool thinking" {
		t.Fatalf("assistant history = %#v", messages[1])
	}
	parts := response.Output.Message.Parts
	if len(parts) != 2 || parts[0].Kind != corechat.PartReasoning || parts[0].Text != "thinking" {
		t.Fatalf("response parts = %#v", parts)
	}
}

func TestChatRejectsTemperatureAboveOfficialMaximum(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("request must fail before transport")
	}))
	t.Cleanup(server.Close)
	model, err := xiaomi.NewChat(t.Context(), xiaomi.ChatConfig{
		APIKey:         "test-key",
		BaseURL:        server.URL,
		DefaultOptions: corechat.Options{Model: xiaomi.ModelV25Pro},
	})
	if err != nil {
		t.Fatalf("NewChat: %v", err)
	}
	temperature := 1.6
	_, err = model.Call(t.Context(), &corechat.Request{
		Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("hello"))},
		Options:  corechat.Options{Temperature: &temperature},
	})
	if err == nil {
		t.Fatal("Call succeeded; want temperature validation error")
	}
}
