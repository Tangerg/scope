package bedrock_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aws/smithy-go/eventstream"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/models/bedrock"
)

// The behavior suite asks for a provider's real SDK transport, so these cases
// drive the AWS client against an endpoint that speaks the wire format rather
// than a fake in place of the client. Nothing had covered these three: that a
// canceled context stops a call and a stream, that abandoning a stream early
// releases the connection, and that an error mid-stream is the last thing the
// iterator yields.
func TestChat_BehaviorConformance(t *testing.T) {
	streamCase := func(t *testing.T) modeltest.StreamBehaviorCase {
		t.Helper()
		server, lifecycle := modeltest.NewBlockingServer(t, writeBedrockBehaviorEvent)
		return modeltest.StreamBehaviorCase{
			Streamer:  newBedrockBehaviorChat(t, server.URL),
			Lifecycle: lifecycle,
		}
	}
	modeltest.ChatBehaviorSuite{
		Request: newBedrockBehaviorRequest,
		CallCancellation: func(t *testing.T) modeltest.CallBehaviorCase {
			t.Helper()
			server, lifecycle := modeltest.NewBlockingServer(t, nil)
			return modeltest.CallBehaviorCase{
				Model:     newBedrockBehaviorChat(t, server.URL),
				Lifecycle: lifecycle,
			}
		},
		StreamCancellation: streamCase,
		EarlyStop:          streamCase,
		FirstError: func(t *testing.T) corechat.Streamer {
			t.Helper()
			// A good event, then one whose payload is not the JSON its event
			// type promises, then another good one: the iterator must stop at
			// the malformed frame, because a consumer that saw the third delta
			// would have no way to know the second was lost.
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", bedrockEventStreamContentType)
				writer.WriteHeader(http.StatusOK)
				writeBedrockEvent(writer, "contentBlockDelta", mustBedrockJSON(map[string]any{
					"contentBlockIndex": 0,
					"delta":             map[string]any{"text": "before"},
				}))
				writeBedrockEvent(writer, "contentBlockDelta", []byte(`{"delta":`))
				writeBedrockEvent(writer, "contentBlockDelta", mustBedrockJSON(map[string]any{
					"contentBlockIndex": 0,
					"delta":             map[string]any{"text": "after"},
				}))
			}))
			t.Cleanup(server.Close)
			return newBedrockBehaviorChat(t, server.URL)
		},
	}.Run(t)
}

func newBedrockBehaviorRequest(t *testing.T) *corechat.Request {
	t.Helper()
	return &corechat.Request{
		Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("hello"))},
	}
}

func newBedrockBehaviorChat(t *testing.T, baseURL string) *bedrock.Chat {
	t.Helper()
	adapter, err := bedrock.NewChat(t.Context(), bedrock.ChatConfig{
		Region:         "us-east-1",
		BaseURL:        baseURL,
		Credentials:    &bedrock.Credentials{AccessKeyID: "id", SecretAccessKey: "secret"},
		DefaultOptions: corechat.Options{Model: "anthropic.claude-test"},
	})
	if err != nil {
		t.Fatalf("NewChat: %v", err)
	}
	return adapter
}

// ConverseStream replies in the Amazon event-stream framing, so the fixture
// encodes real frames with the SDK's own encoder rather than hand-written
// bytes: a frame this test got wrong would look like a provider fault.
const bedrockEventStreamContentType = "application/vnd.amazon.eventstream"

func writeBedrockBehaviorEvent(writer http.ResponseWriter) {
	writer.Header().Set("Content-Type", bedrockEventStreamContentType)
	writer.WriteHeader(http.StatusOK)
	writeBedrockEvent(writer, "contentBlockDelta", mustBedrockJSON(map[string]any{
		"contentBlockIndex": 0,
		"delta":             map[string]any{"text": "ready"},
	}))
	writer.(http.Flusher).Flush()
}

func writeBedrockEvent(writer http.ResponseWriter, eventType string, payload []byte) {
	message := eventstream.Message{
		Headers: eventstream.Headers{
			{Name: ":message-type", Value: eventstream.StringValue("event")},
			{Name: ":event-type", Value: eventstream.StringValue(eventType)},
			{Name: ":content-type", Value: eventstream.StringValue("application/json")},
		},
		Payload: payload,
	}
	var framed bytes.Buffer
	if err := eventstream.NewEncoder().Encode(&framed, message); err != nil {
		// Encoding a fixture cannot fail for well-formed headers; writing
		// nothing would stall the SDK instead of reporting why.
		panic(err)
	}
	_, _ = writer.Write(framed.Bytes())
	if flusher, ok := writer.(http.Flusher); ok {
		flusher.Flush()
	}
}

func mustBedrockJSON(value any) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}
