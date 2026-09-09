package anthropic_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/anthropic"
)

// Anthropic publishes a field-by-field support table for its OpenAI-compatible
// endpoint, and marks these three "Ignored". Sending one and letting Anthropic
// discard it is indistinguishable, from the caller's side, from this adapter
// never having mapped it -- the one outcome Core says an adapter must not
// produce. Each is refused at the call instead.
func TestChatCompletionsRefusesOptionsAnthropicIgnores(t *testing.T) {
	t.Parallel()

	penalty := 0.5
	tests := map[string]corechat.Options{
		"frequency_penalty": {FrequencyPenalty: &penalty},
		"presence_penalty":  {PresencePenalty: &penalty},
		"reasoning_effort":  {ReasoningEffort: "high"},
	}

	for field, options := range tests {
		t.Run(field, func(t *testing.T) {
			t.Parallel()

			model, calls := newCompatModel(t)
			_, err := model.Call(t.Context(), &corechat.Request{
				Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("hi"))},
				Options:  options,
			})
			if err == nil {
				t.Fatalf("Call() = nil error, want %s refused", field)
			}
			if !strings.Contains(err.Error(), field) {
				t.Fatalf("Call() = %v, want an error naming %s", err, field)
			}
			if got := calls(); got != 0 {
				t.Fatalf("HTTP calls = %d, want the option refused before any request", got)
			}
		})
	}
}

// The table caps temperature at 1 and says "values greater than 1 are capped at
// 1". Silently altering a caller's setting is the same class of problem as
// dropping one, so a larger value is refused rather than sent to be capped.
func TestChatCompletionsRefusesATemperatureAnthropicWouldCap(t *testing.T) {
	t.Parallel()

	model, calls := newCompatModel(t)
	temperature := 1.5
	_, err := model.Call(t.Context(), &corechat.Request{
		Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("hi"))},
		Options:  corechat.Options{Temperature: &temperature},
	})
	if err == nil {
		t.Fatal("Call() = nil error, want the temperature refused")
	}
	if !strings.Contains(err.Error(), "temperature") {
		t.Fatalf("Call() = %v, want an error naming temperature", err)
	}
	if got := calls(); got != 0 {
		t.Fatalf("HTTP calls = %d, want the option refused before any request", got)
	}
}

// A temperature the endpoint honors still travels, so the bound refuses only
// what Anthropic would have altered.
func TestChatCompletionsSendsATemperatureAnthropicHonors(t *testing.T) {
	t.Parallel()

	model, calls := newCompatModel(t)
	temperature := 1.0
	if _, err := model.Call(t.Context(), &corechat.Request{
		Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("hi"))},
		Options:  corechat.Options{Temperature: &temperature},
	}); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if got := calls(); got != 1 {
		t.Fatalf("HTTP calls = %d, want 1", got)
	}
}

func newCompatModel(t *testing.T) (*anthropic.ChatCompletions, func() int) {
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

	model, err := anthropic.NewChatCompletions(t.Context(), anthropic.ChatCompletionsConfig{
		APIKey:         "test-key",
		BaseURL:        server.URL,
		DefaultOptions: corechat.Options{Model: "claude-opus-5"},
	})
	if err != nil {
		t.Fatalf("NewChatCompletions: %v", err)
	}
	return model, func() int { return calls }
}
