package minimax_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/minimax"
)

func TestAnthropicChatConstructorValidatesCredential(t *testing.T) {
	if _, err := minimax.NewMessages(t.Context(), minimax.MessagesConfig{}); err == nil {
		t.Fatal("NewMessages(t.Context(), ) accepted an absent credential")
	}
	model, err := minimax.NewMessages(t.Context(), minimax.MessagesConfig{APIKey: "test-key"})
	if err != nil {
		t.Fatal(err)
	}
	if model == nil {
		t.Fatal("NewMessages(t.Context(), ) = nil")
	}
}

func TestChatUsesSplitReasoningByDefault(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(writer, "invalid request", http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"id":"chat-1","model":"MiniMax-M3","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"answer","reasoning_content":"thinking","reasoning_details":[{"type":"reasoning.text","text":"thinking","format":"MiniMax-response-v1","index":0}]}}]}`))
	}))
	t.Cleanup(server.Close)

	model, err := minimax.NewChat(t.Context(), minimax.ChatConfig{
		APIKey:         "test-key",
		BaseURL:        server.URL,
		DefaultOptions: corechat.Options{Model: "MiniMax-M3"},
	})
	if err != nil {
		t.Fatalf("NewChat: %v", err)
	}
	response, err := model.Call(t.Context(), &corechat.Request{Messages: []corechat.Message{
		corechat.NewUserMessage(corechat.NewTextPart("solve")),
	}})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if body["reasoning_split"] != true {
		t.Fatalf("reasoning_split = %#v; want true", body["reasoning_split"])
	}
	parts := response.Output.Message.Parts
	if len(parts) != 2 || parts[0].Kind != corechat.PartReasoning || parts[0].Text != "thinking" || parts[1].Text != "answer" {
		t.Fatalf("response parts = %#v", parts)
	}
}

func TestChatRespectsExplicitReasoningSplit(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(writer, "invalid request", http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"id":"chat-1","model":"MiniMax-M3","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"<think>thinking</think>answer"}}]}`))
	}))
	t.Cleanup(server.Close)

	model, err := minimax.NewChat(t.Context(), minimax.ChatConfig{
		APIKey:         "test-key",
		BaseURL:        server.URL,
		DefaultOptions: corechat.Options{Model: "MiniMax-M3"},
	})
	if err != nil {
		t.Fatalf("NewChat: %v", err)
	}
	request := &corechat.Request{Messages: []corechat.Message{
		corechat.NewUserMessage(corechat.NewTextPart("solve")),
	}}
	reasoningSplit := false
	if err := request.Options.Extensions.Set(minimax.RequestExtensionKey, minimax.ChatRequestOptions{ReasoningSplit: &reasoningSplit}); err != nil {
		t.Fatalf("SetExtension: %v", err)
	}
	if _, err := model.Call(t.Context(), request); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if body["reasoning_split"] != false {
		t.Fatalf("reasoning_split = %#v; want false", body["reasoning_split"])
	}
}

func TestChatReplaysStructuredReasoningDetails(t *testing.T) {
	requests := make([]map[string]any, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(writer, "invalid request", http.StatusBadRequest)
			return
		}
		requests = append(requests, body)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"id":"chat-1","model":"MiniMax-M3","choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","content":"","tool_calls":[{"id":"call-1","type":"function","function":{"name":"lookup","arguments":"{}"}}],"reasoning_content":"thinking","reasoning_details":[{"type":"reasoning.text","id":"reasoning-text-1","format":"MiniMax-response-v1","index":0,"text":"thinking"}]}}]}`))
	}))
	t.Cleanup(server.Close)

	model, err := minimax.NewChat(t.Context(), minimax.ChatConfig{
		APIKey:         "test-key",
		BaseURL:        server.URL,
		DefaultOptions: corechat.Options{Model: minimax.ModelM3},
	})
	if err != nil {
		t.Fatalf("NewChat: %v", err)
	}
	userMessage := corechat.NewUserMessage(corechat.NewTextPart("look up"))
	response, err := model.Call(t.Context(), &corechat.Request{Messages: []corechat.Message{userMessage}})
	if err != nil {
		t.Fatalf("first Call: %v", err)
	}
	assistantMessage := response.Output.Message
	if assistantMessage == nil || len(assistantMessage.Parts) != 2 || len(assistantMessage.Parts[0].ReasoningState) == 0 {
		t.Fatalf("assistant message = %#v", assistantMessage)
	}
	if _, err := model.Call(t.Context(), &corechat.Request{Messages: []corechat.Message{
		userMessage,
		assistantMessage.Clone(),
		corechat.NewToolMessage(corechat.ToolResult{
			ID: "call-1", Name: "lookup", Output: corechat.NewTextToolOutput("found"),
		}),
	}}); err != nil {
		t.Fatalf("second Call: %v", err)
	}
	messages := requests[1]["messages"].([]any)
	assistant := messages[1].(map[string]any)
	if _, found := assistant["reasoning_content"]; found {
		t.Fatalf("structured reasoning downgraded to reasoning_content: %#v", assistant)
	}
	details, ok := assistant["reasoning_details"].([]any)
	if !ok || len(details) != 1 {
		t.Fatalf("reasoning_details = %#v", assistant["reasoning_details"])
	}
	detail := details[0].(map[string]any)
	if detail["type"] != "reasoning.text" || detail["text"] != "thinking" || detail["format"] != "MiniMax-response-v1" {
		t.Fatalf("reasoning detail = %#v", detail)
	}
}
