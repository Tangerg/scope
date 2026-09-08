package protocol_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/models/google/internal/protocol"
)

func TestChat_BehaviorConformance(t *testing.T) {
	streamCase := func(t *testing.T) modeltest.StreamBehaviorCase {
		t.Helper()
		server, lifecycle := modeltest.NewBlockingServer(t, writeGoogleBehaviorChunk)
		return modeltest.StreamBehaviorCase{Streamer: newGoogleBehaviorChat(t, server.URL), Lifecycle: lifecycle}
	}
	modeltest.ChatBehaviorSuite{
		Request: newProtocolChatRequest,
		CallCancellation: func(t *testing.T) modeltest.CallBehaviorCase {
			t.Helper()
			server, lifecycle := modeltest.NewBlockingServer(t, nil)
			return modeltest.CallBehaviorCase{Model: newGoogleBehaviorChat(t, server.URL), Lifecycle: lifecycle}
		},
		StreamCancellation: streamCase,
		EarlyStop:          streamCase,
		FirstError: func(t *testing.T) corechat.Streamer {
			t.Helper()
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(writer, "data: {\"responseId\":\"before-error\",\"modelVersion\":\"gemini-3-pro-001\",\"candidates\":[{\"index\":0,\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"before\"}]}}]}\n\n")
				fmt.Fprint(writer, "data: {\n\n")
				fmt.Fprint(writer, "data: {\"responseId\":\"after-error\",\"modelVersion\":\"gemini-3-pro-001\",\"candidates\":[{\"index\":0,\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"after\"}]}}]}\n\n")
			}))
			t.Cleanup(server.Close)
			return newGoogleBehaviorChat(t, server.URL)
		},
	}.Run(t)
}

func newGoogleBehaviorChat(t *testing.T, baseURL string) *protocol.Chat {
	t.Helper()
	adapter, err := protocol.NewChat(t.Context(), protocol.ChatConfig{
		Provider:       "google",
		Client:         protocol.ClientConfig{APIKey: "test-key", BaseURL: baseURL},
		DefaultOptions: corechat.Options{Model: "gemini-3-pro"},
	})
	if err != nil {
		t.Fatalf("NewChat: %v", err)
	}
	return adapter
}

func writeGoogleBehaviorChunk(writer http.ResponseWriter) {
	writer.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprint(writer, "data: {\"responseId\":\"lifecycle\",\"modelVersion\":\"gemini-3-pro-001\",\"candidates\":[{\"index\":0,\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"ready\"}]}}]}\n\n")
	writer.(http.Flusher).Flush()
}
