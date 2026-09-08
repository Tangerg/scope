package mistral_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/modeltest"
)

// The behavior contract this adapter had nobody checking: that a canceled
// context stops a call and a stream, that abandoning a stream early releases
// the provider connection, and that an error mid-stream is the last thing the
// iterator yields rather than something a later chunk papers over.
func TestChat_BehaviorConformance(t *testing.T) {
	streamCase := func(t *testing.T) modeltest.StreamBehaviorCase {
		t.Helper()
		server, lifecycle := modeltest.NewBlockingServer(t, writeMistralBehaviorChunk)
		return modeltest.StreamBehaviorCase{
			Streamer:  newMistralConformanceChat(t, server.URL),
			Lifecycle: lifecycle,
		}
	}
	modeltest.ChatBehaviorSuite{
		Request: newMistralConformanceRequest,
		CallCancellation: func(t *testing.T) modeltest.CallBehaviorCase {
			t.Helper()
			server, lifecycle := modeltest.NewBlockingServer(t, nil)
			return modeltest.CallBehaviorCase{
				Model:     newMistralConformanceChat(t, server.URL),
				Lifecycle: lifecycle,
			}
		},
		StreamCancellation: streamCase,
		EarlyStop:          streamCase,
		FirstError: func(t *testing.T) corechat.Streamer {
			t.Helper()
			// A chunk that is not JSON, followed by one that is: the iterator
			// must stop at the first, because a consumer that saw the third
			// chunk would have no way to know the second was lost.
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(writer, "data: %s\n\n", mistralStreamChunks[0])
				fmt.Fprint(writer, "data: {\n\n")
				fmt.Fprintf(writer, "data: %s\n\n", mistralStreamChunks[1])
			}))
			t.Cleanup(server.Close)
			return newMistralConformanceChat(t, server.URL)
		},
	}.Run(t)
}

func writeMistralBehaviorChunk(writer http.ResponseWriter) {
	writer.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprintf(writer, "data: %s\n\n", mistralStreamChunks[0])
	writer.(http.Flusher).Flush()
}
