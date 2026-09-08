package openai_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/modeltest"
	scopeopenai "github.com/Tangerg/scope/models/protocol/openai"
)

func TestChat_BehaviorConformance(t *testing.T) {
	streamCase := func(t *testing.T) modeltest.StreamBehaviorCase {
		t.Helper()
		server, lifecycle := modeltest.NewBlockingServer(t, writeOpenAIBehaviorChunk)
		return modeltest.StreamBehaviorCase{Streamer: newOpenAIBehaviorChat(t, server.URL), Lifecycle: lifecycle}
	}
	modeltest.ChatBehaviorSuite{
		Request: newCoreChatRequest,
		CallCancellation: func(t *testing.T) modeltest.CallBehaviorCase {
			t.Helper()
			server, lifecycle := modeltest.NewBlockingServer(t, nil)
			return modeltest.CallBehaviorCase{Model: newOpenAIBehaviorChat(t, server.URL), Lifecycle: lifecycle}
		},
		StreamCancellation: streamCase,
		EarlyStop:          streamCase,
		FirstError: func(t *testing.T) corechat.Streamer {
			t.Helper()
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(writer, "data: {\"id\":\"before-error\",\"model\":\"gpt-5.2\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"before\"}}]}\n\n")
				fmt.Fprint(writer, "data: {\n\n")
				fmt.Fprint(writer, "data: {\"id\":\"after-error\",\"model\":\"gpt-5.2\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"after\"}}]}\n\n")
				fmt.Fprint(writer, "data: [DONE]\n\n")
			}))
			t.Cleanup(server.Close)
			return newOpenAIBehaviorChat(t, server.URL)
		},
	}.Run(t)
}

func newOpenAIBehaviorChat(t *testing.T, baseURL string) *scopeopenai.ChatCompletions {
	t.Helper()
	adapter, err := scopeopenai.NewChatCompletions(t.Context(), scopeopenai.ChatCompletionsConfig{
		APIKey:         "test-key",
		DefaultOptions: corechat.Options{Model: "gpt-5.2"},
		BaseURL:        baseURL,
	})
	if err != nil {
		t.Fatalf("NewChatCompletions: %v", err)
	}
	return adapter
}

func writeOpenAIBehaviorChunk(writer http.ResponseWriter) {
	writer.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprint(writer, "data: {\"id\":\"lifecycle\",\"model\":\"gpt-5.2\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ready\"}}]}\n\n")
	writer.(http.Flusher).Flush()
}
