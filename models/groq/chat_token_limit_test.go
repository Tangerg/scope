package groq_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/groq"
)

// Groq's API reference marks max_tokens "Deprecated in favor of
// max_completion_tokens", and its own text-generation guide passes the
// replacement. The shared reasoning dialect defaults to the legacy field, so
// this adapter overrides it — and nothing checked which field left the process.
//
// The check reads the request body rather than the dialect value, because the
// wire field is what the provider acts on and the dialect is only how this
// adapter spells it.
func TestChatSendsTheDocumentedTokenLimitField(t *testing.T) {
	t.Parallel()

	bodies := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(writer, "bad request", http.StatusBadRequest)
			return
		}
		bodies <- body
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"id":"c","model":"m","choices":[{"index":0,"finish_reason":"stop",` +
			`"message":{"role":"assistant","content":"hi"}}],"usage":{}}`))
	}))
	t.Cleanup(server.Close)

	limit := int64(64)
	model, err := groq.NewChat(t.Context(), groq.ChatConfig{
		APIKey:         "test-key",
		BaseURL:        server.URL,
		DefaultOptions: corechat.Options{Model: "llama-3.3-70b-versatile"},
	})
	if err != nil {
		t.Fatalf("NewChat: %v", err)
	}
	if _, err := model.Call(t.Context(), &corechat.Request{
		Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("hi"))},
		Options:  corechat.Options{MaxOutputTokens: &limit},
	}); err != nil {
		t.Fatalf("Call: %v", err)
	}

	body := <-bodies
	if _, present := body["max_tokens"]; present {
		t.Errorf("request carries max_tokens, which this provider documents against")
	}
	value, present := body["max_completion_tokens"]
	if !present {
		t.Fatalf("request carries no max_completion_tokens; body = %v", body)
	}
	if number, ok := value.(float64); !ok || int64(number) != limit {
		t.Fatalf("max_completion_tokens = %v, want %d", value, limit)
	}
}
