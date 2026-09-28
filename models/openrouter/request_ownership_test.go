package openrouter_test

import (
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/openrouter"
)

func TestChatRejectsNativeReasoningEffort(t *testing.T) {
	model, err := openrouter.NewChat(t.Context(), openrouter.ChatConfig{APIKey: "test", BaseURL: "http://127.0.0.1:1", DefaultOptions: chat.Options{Model: "test-model"}})
	if err != nil {
		t.Fatal(err)
	}
	request := &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("hello"))}}
	if err := request.Options.Extensions.Set(openrouter.OpenAIRequestExtensionKey, map[string]any{"reasoning": map[string]any{"effort": "high"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := model.Call(t.Context(), request); err == nil || !strings.Contains(err.Error(), "reasoning.effort is owned") {
		t.Fatalf("Call=%v", err)
	}
}
