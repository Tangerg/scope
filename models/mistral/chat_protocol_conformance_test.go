package mistral_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/models/mistral"
)

// This adapter owns its protocol rather than reaching a shared one, so nothing
// else pins the Model and Streamer contract for it: that a call leaves the
// request untouched, that every delta is valid on its own, and that the deltas
// aggregate through [corechat.ResponseAccumulator] into a valid response. The
// shared suite is the only place those hold for every provider at once.
func TestChat_CoreConformance(t *testing.T) {
	modeltest.ChatSuite{
		New: func(t *testing.T) (corechat.Model, corechat.Streamer) {
			t.Helper()
			adapter := newMistralConformanceChat(t, newMistralChatServer(t).URL)
			return adapter, adapter
		},
		Request: newMistralConformanceRequest,
	}.Run(t)
}

func newMistralConformanceRequest(t *testing.T) *corechat.Request {
	t.Helper()
	return &corechat.Request{
		Messages: []corechat.Message{
			corechat.NewSystemMessage("be brief"),
			corechat.NewUserMessage(corechat.NewTextPart("hello")),
		},
	}
}

func newMistralConformanceChat(t *testing.T, baseURL string) *mistral.Chat {
	t.Helper()
	adapter, err := mistral.NewChat(mistral.ChatConfig{
		APIKey:         "test-key",
		BaseURL:        baseURL,
		DefaultOptions: corechat.Options{Model: "mistral-small-latest"},
	})
	if err != nil {
		t.Fatalf("NewChat: %v", err)
	}
	return adapter
}

// newMistralChatServer answers both paths the suite exercises, choosing by the
// stream flag the adapter sends rather than by the URL, because that flag is
// the only thing separating the two on this provider's single endpoint.
func newMistralChatServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body struct {
			Model    string `json:"model"`
			Stream   bool   `json:"stream"`
			Messages []struct {
				Role string `json:"role"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(writer, "bad request", http.StatusBadRequest)
			return
		}
		if body.Model != "mistral-small-latest" {
			t.Errorf("request model = %q", body.Model)
		}
		if len(body.Messages) != 2 {
			t.Errorf("request messages = %d, want 2", len(body.Messages))
		}
		if body.Stream {
			writer.Header().Set("Content-Type", "text/event-stream")
			for _, chunk := range mistralStreamChunks {
				fmt.Fprintf(writer, "data: %s\n\n", chunk)
			}
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprint(writer, mistralChatResponseJSON)
	}))
	t.Cleanup(server.Close)
	return server
}

const mistralChatResponseJSON = `{"id":"cmpl-1","model":"mistral-small-latest",` +
	`"choices":[{"index":0,"finish_reason":"stop",` +
	`"message":{"role":"assistant","content":"hello there"}}],` +
	`"usage":{"prompt_tokens":9,"completion_tokens":3}}`

var mistralStreamChunks = []string{
	`{"id":"cmpl-1","model":"mistral-small-latest","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"}}]}`,
	`{"id":"cmpl-1","model":"mistral-small-latest","choices":[{"index":0,"delta":{"content":" there"}}]}`,
	`{"id":"cmpl-1","model":"mistral-small-latest","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],` +
		`"usage":{"prompt_tokens":9,"completion_tokens":3}}`,
	"[DONE]",
}
