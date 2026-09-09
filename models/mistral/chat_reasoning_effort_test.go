package mistral_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/mistral"
)

// Core states the rule this checks: an adapter "must not accept the effort and
// send a request that never carried it". This one used to do exactly that --
// TopK was refused explicitly while ReasoningEffort was neither mapped nor
// rejected, so a caller's reasoning setting vanished between Call and the wire.
//
// Mistral's chat endpoint documents reasoning_effort as
// "none"|"minimal"|"low"|"medium"|"high"|"xhigh", which is Core's vocabulary
// without max, so every one of them travels.
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

// An empty effort means "the model's default", so it must not overwrite a
// reasoning_effort a caller set through the native extension.
func TestChatLeavesTheNativeReasoningEffortAlone(t *testing.T) {
	t.Parallel()

	bodies := make(chan map[string]any, 1)
	server := newRecordingServer(t, bodies)

	options := corechat.Options{}
	if err := options.Extensions.Set(mistral.RequestExtensionKey,
		mistral.ChatRequestOptions{ReasoningEffort: mistral.ReasoningEffortMedium}); err != nil {
		t.Fatal(err)
	}

	model := newChatModel(t, server.URL)
	if _, err := model.Call(t.Context(), &corechat.Request{
		Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("hi"))},
		Options:  options,
	}); err != nil {
		t.Fatalf("Call: %v", err)
	}

	body := <-bodies
	if got := body["reasoning_effort"]; got != "medium" {
		t.Fatalf("reasoning_effort = %v, want medium", got)
	}
}

func newRecordingServer(t *testing.T, bodies chan<- map[string]any) *httptest.Server {
	t.Helper()

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
