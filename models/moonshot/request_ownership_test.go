package moonshot_test

import (
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/moonshot"
)

func TestChatRejectsNativeReasoningEffort(t *testing.T) {
	model, err := moonshot.NewChat(t.Context(), moonshot.ChatConfig{APIKey: "test", BaseURL: "http://127.0.0.1:1", DefaultOptions: chat.Options{Model: "test-model"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, effort := range []any{"high", nil} {
		request := &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("hello"))}}
		if setErr := request.Options.Extensions.Set(moonshot.RequestExtensionKey, map[string]any{"reasoning_effort": effort}); setErr != nil {
			t.Fatal(setErr)
		}
		_, err = model.Call(t.Context(), request)
		if err == nil || !strings.Contains(err.Error(), "reasoning_effort") || !strings.Contains(err.Error(), "unknown") {
			t.Fatalf("Call=%v", err)
		}
	}
}
