package groq_test

import (
	jsonv2 "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/invocation"
	"github.com/Tangerg/scope/models/groq"
)

// Groq's Qwen models accept "default", a word OpenAI's vocabulary lacks, so
// the shared protocol must take Groq's vocabulary rather than OpenAI's.
func TestChatAdmitsGroqReasoningEffortVocabulary(t *testing.T) {
	efforts := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body struct {
			ReasoningEffort string `json:"reasoning_effort"`
		}
		if err := jsonv2.UnmarshalRead(request.Body, &body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		efforts <- body.ReasoningEffort
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(writer, "data: %s\n\ndata: [DONE]\n\n", `{"id":"c","model":"m","choices":[{"index":0,"finish_reason":"stop","delta":{"role":"assistant","content":"hi"}}],"object":"chat.completion.chunk"}`)
	}))
	t.Cleanup(server.Close)
	model, err := groq.NewChatCompletions(t.Context(), groq.ChatCompletionsConfig{
		APIKey: "test-key", BaseURL: server.URL, DefaultOptions: corechat.Options{Model: "qwen/qwen3-32b"},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := func(effort corechat.ReasoningEffort) *corechat.Request {
		return &corechat.Request{
			Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("hi"))},
			Options:  corechat.Options{ReasoningEffort: effort},
		}
	}
	if _, err := model.Call(t.Context(), request("default")); err != nil {
		t.Fatalf("Call with default effort: %v", err)
	}
	if sent := <-efforts; sent != "default" {
		t.Fatalf("reasoning_effort = %q, want default", sent)
	}
	if _, err := model.Call(t.Context(), request("minimal")); invocation.ClassifyFailure(err) != invocation.FailureNotExecuted {
		t.Fatalf("Call with an effort outside Groq's vocabulary = %v, want a local refusal", err)
	}
}
