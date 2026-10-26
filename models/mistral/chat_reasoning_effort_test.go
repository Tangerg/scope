package mistral_test

import (
	jsonv2 "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/mistral"
)

// Every supported effort must reach the wire; accepting one without projecting
// it silently changes the caller's requested generation policy.
func TestChatSendsThePortableReasoningEffort(t *testing.T) {
	t.Parallel()

	for _, effort := range []string{"none", "minimal", "low", "medium", "high", "xhigh"} {
		t.Run(effort, func(t *testing.T) {
			t.Parallel()

			bodies := make(chan map[string]any, 1)
			server := newRecordingServer(t, bodies)

			model := newChatModel(t, server.URL)
			if _, err := model.Call(t.Context(), &corechat.Request{
				Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("hi"))},
				Options:  corechat.Options{ReasoningEffort: corechat.ReasoningEffort(effort)},
			}); err != nil {
				t.Fatalf("Call: %v", err)
			}

			body := <-bodies
			if got := body["reasoning_effort"]; got != effort {
				t.Fatalf("reasoning_effort = %v, want %q", got, effort)
			}
		})
	}
}

// The enum is closed on Mistral's side, so a level it does not document is
// refused rather than sent and ignored. max is Core's one value with no
// Mistral equivalent.
func TestChatRefusesAnUndocumentedReasoningEffort(t *testing.T) {
	t.Parallel()

	bodies := make(chan map[string]any, 1)
	server := newRecordingServer(t, bodies)

	model := newChatModel(t, server.URL)
	_, err := model.Call(t.Context(), &corechat.Request{
		Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("hi"))},
		Options:  corechat.Options{ReasoningEffort: "max"},
	})
	if err == nil {
		t.Fatal("Call() = nil error, want the undocumented effort refused")
	}
}

func TestChatRejectsNativeReasoningEffort(t *testing.T) {
	options := corechat.Options{}
	if err := options.Extensions.Set(mistral.RequestExtensionKey, map[string]any{"reasoning_effort": "medium"}); err != nil {
		t.Fatal(err)
	}
	model := newChatModel(t, "http://127.0.0.1:1")
	_, err := model.Call(t.Context(), &corechat.Request{Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("hi"))}, Options: options})
	if err == nil || !strings.Contains(err.Error(), "reasoning_effort") || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("Call = %v", err)
	}
}

func newRecordingServer(t *testing.T, bodies chan<- map[string]any) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := jsonv2.UnmarshalRead(request.Body, &body); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(writer, "bad request", http.StatusBadRequest)
			return
		}
		bodies <- body
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(writer, "data: %s\n\ndata: [DONE]\n\n", `{"id":"c","model":"m","choices":[{"index":0,"finish_reason":"stop",`+
			`"delta":{"role":"assistant","content":"hi"}}],"usage":{}}`)
	}))
	t.Cleanup(server.Close)
	return server
}

func newChatModel(t *testing.T, baseURL string) *mistral.Chat {
	t.Helper()

	model, err := mistral.NewChat(t.Context(), mistral.ChatConfig{
		APIKey:         "test-key",
		BaseURL:        baseURL,
		DefaultOptions: corechat.Options{Model: "mistral-large-latest"},
	})
	if err != nil {
		t.Fatalf("NewChat: %v", err)
	}
	return model
}
