package openai_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/modeltest"
)

func TestResponses_BehaviorConformance(t *testing.T) {
	streamCase := func(t *testing.T) modeltest.StreamBehaviorCase {
		t.Helper()
		server, lifecycle := modeltest.NewBlockingServer(t, writeResponsesBehaviorChunk)
		return modeltest.StreamBehaviorCase{Streamer: newResponsesModel(t, server.URL, "test-model"), Lifecycle: lifecycle}
	}
	modeltest.RunChatBehaviorContract(t, modeltest.ChatBehaviorContract{
		Request: newResponsesChatRequest,
		CallCancellation: func(t *testing.T) modeltest.CallBehaviorCase {
			t.Helper()
			server, lifecycle := modeltest.NewBlockingServer(t, nil)
			return modeltest.CallBehaviorCase{Model: newResponsesModel(t, server.URL, "test-model"), Lifecycle: lifecycle}
		},
		StreamCancellation: streamCase,
		EarlyStop:          streamCase,
		FirstError: func(t *testing.T) chat.Streamer {
			t.Helper()
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writeResponsesBehaviorChunk(writer)
				fmt.Fprint(writer, "event: response.output_text.delta\ndata: {\n\n")
				writeResponsesCompletion(writer)
			}))
			t.Cleanup(server.Close)
			return newResponsesModel(t, server.URL, "test-model")
		},
	})
}

func writeResponsesBehaviorChunk(writer http.ResponseWriter) {
	writer.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprint(writer, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"lifecycle\",\"model\":\"test-model\",\"status\":\"in_progress\"}}\n\n")
	fmt.Fprint(writer, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"message-1\",\"output_index\":0,\"content_index\":0,\"delta\":\"ready\"}\n\n")
	writer.(http.Flusher).Flush()
}
