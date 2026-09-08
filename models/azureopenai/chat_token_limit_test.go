package azureopenai_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/azureopenai"
)

// Azure documents that reasoning models "will only work with the
// max_completion_tokens parameter when using the Chat Completions API", and
// lists max_tokens among the parameters those models do not support. This
// adapter sent max_tokens, so a caller's MaxOutputTokens could not survive on
// a GPT-5 or o-series deployment, and nothing checked.
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
	model, err := azureopenai.NewChat(t.Context(), azureopenai.ChatConfig{
		Config: azureopenai.Config{
			APIKey:  "test-key",
			BaseURL: server.URL + "/openai/v1/",
		},
		DefaultOptions: corechat.Options{Model: "gpt-5"},
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
