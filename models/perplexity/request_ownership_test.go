package perplexity_test

import (
	jsonv2 "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/perplexity"
)

func TestChatRejectsNativeReasoningEffort(t *testing.T) {
	model, err := perplexity.NewChatCompletions(t.Context(), perplexity.ChatCompletionsConfig{APIKey: "test", BaseURL: "http://127.0.0.1:1", DefaultOptions: chat.Options{Model: "test-model"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, effort := range []any{"high", nil} {
		request := &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("hello"))}}
		if setErr := request.Options.Extensions.Set(perplexity.RequestExtensionKey, map[string]any{"reasoning_effort": effort}); setErr != nil {
			t.Fatal(setErr)
		}
		_, err = model.Call(t.Context(), request)
		if err == nil || !strings.Contains(err.Error(), "reasoning_effort") || !strings.Contains(err.Error(), "unknown") {
			t.Fatalf("Call=%v", err)
		}
	}
}

func TestChatMapsCoreReasoningEffort(t *testing.T) {
	for _, effort := range []chat.ReasoningEffort{"minimal", "low", "medium", "high"} {
		t.Run(string(effort), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				var body map[string]any
				if err := jsonv2.UnmarshalRead(request.Body, &body); err != nil {
					t.Error(err)
				}
				if body["reasoning_effort"] != string(effort) {
					t.Errorf("reasoning_effort=%v", body["reasoning_effort"])
				}
				writer.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(writer, "data: {\"id\":\"c\",\"model\":\"sonar\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"answer\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			}))
			defer server.Close()
			model, err := perplexity.NewChatCompletions(t.Context(), perplexity.ChatCompletionsConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: chat.Options{Model: "sonar", ReasoningEffort: effort}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := model.Call(t.Context(), &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("hello"))}}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
