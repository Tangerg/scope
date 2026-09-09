package xiaomi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/xiaomi"
)

// MiMo documents both of these as settings it throws away. "when tool_choice
// passes non-auto values, backend defaults to removing the field, model
// response behavior remains equal to auto mode", and in thinking mode the
// models "do not support custom temperature and top_p parameters. Even if
// passed, actual values forced to defaults 1.0 and 0.95."
//
// Thinking defaults to enabled, so the override is what happens when a caller
// sets nothing -- the case the adapter used to skip by returning early on an
// absent extension.
func TestChatRefusesSettingsMiMoDiscards(t *testing.T) {
	t.Parallel()

	temperature := 0.5
	topP := 0.5
	tests := map[string]func(*corechat.Request){
		"tool_choice": func(request *corechat.Request) {
			request.ToolChoice = &corechat.ToolChoice{Mode: corechat.ToolChoiceRequired}
		},
		"temperature": func(request *corechat.Request) { request.Options.Temperature = &temperature },
		"top_p":       func(request *corechat.Request) { request.Options.TopP = &topP },
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			model, calls := newDiscardTestModel(t)
			request := &corechat.Request{
				Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("hi"))},
			}
			mutate(request)
			_, err := model.Call(t.Context(), request)
			if err == nil {
				t.Fatalf("Call() = nil error, want %s refused", name)
			}
			if !strings.Contains(err.Error(), name) {
				t.Fatalf("Call() = %v, want an error naming %s", err, name)
			}
			if got := calls(); got != 0 {
				t.Fatalf("HTTP calls = %d, want the setting refused before any request", got)
			}
		})
	}
}

// Outside thinking mode MiMo honors both, so the refusal is scoped to the mode
// that discards them rather than applied to every request.
func TestChatSendsSamplingWhenThinkingIsDisabled(t *testing.T) {
	t.Parallel()

	model, calls := newDiscardTestModel(t)
	options := corechat.Options{}
	if err := options.Extensions.Set(xiaomi.RequestExtensionKey,
		xiaomi.ChatRequestOptions{Thinking: xiaomi.ThinkingDisabled}); err != nil {
		t.Fatal(err)
	}
	temperature := 0.5
	options.Temperature = &temperature

	if _, err := model.Call(t.Context(), &corechat.Request{
		Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("hi"))},
		Options:  options,
	}); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if got := calls(); got != 1 {
		t.Fatalf("HTTP calls = %d, want 1", got)
	}
}

func newDiscardTestModel(t *testing.T) (*xiaomi.Chat, func() int) {
	t.Helper()

	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls++
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"id":"c","model":"m","choices":[{"index":0,"finish_reason":"stop",` +
			`"message":{"role":"assistant","content":"hi"}}],"usage":{}}`))
	}))
	t.Cleanup(server.Close)

	model, err := xiaomi.NewChat(t.Context(), xiaomi.ChatConfig{
		APIKey:         "test-key",
		BaseURL:        server.URL,
		DefaultOptions: corechat.Options{Model: "mimo-v2.5-pro"},
	})
	if err != nil {
		t.Fatalf("NewChat: %v", err)
	}
	return model, func() int { return calls }
}
