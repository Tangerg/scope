package openai_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
	scopeopenai "github.com/Tangerg/scope/models/protocol/openai"
)

// A Core tool-call delta cannot carry arguments without an id and a name, so
// the adapter buffers an index until OpenAI sends both -- which it does on the
// first delta at that index. Nothing used to flush or report a buffer still
// held when the stream ended: the terminal response came back with a normal
// finish reason and the tool call simply absent, its arguments discarded.
func TestStreamRefusesAToolCallItNeverIdentified(t *testing.T) {
	t.Parallel()

	tests := map[string][]string{
		"arguments without id or name": {
			`{"index":0,"function":{"arguments":"{\"q\":\"scope\"}"}}`,
		},
		"name but no id": {
			`{"index":0,"function":{"name":"search","arguments":"{\"q\":\"scope\"}"}}`,
		},
		"id but no name": {
			`{"index":0,"id":"call-1","function":{"arguments":"{\"q\":\"scope\"}"}}`,
		},
		// A second index left unidentified is just as lost as a first one.
		"one identified and one not": {
			`{"index":0,"id":"call-1","type":"function","function":{"name":"search","arguments":"{}"}}`,
			`{"index":1,"function":{"arguments":"{\"q\":\"scope\"}"}}`,
		},
	}

	for name, toolDeltas := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			model := newToolStreamModel(t, toolDeltas)
			var lastErr error
			deltas := 0
			for delta, err := range model.Stream(t.Context(), newToolStreamRequest(t)) {
				if err != nil {
					lastErr = err
					break
				}
				if delta.FinishReason != "" {
					t.Fatalf("stream yielded a terminal delta for an unidentified tool call: %+v", delta)
				}
				deltas++
			}
			if lastErr == nil {
				t.Fatalf("Stream yielded %d deltas and no error, want the unreported tool call refused", deltas)
			}
			if !errors.Is(lastErr, corechat.ErrInvalidResponse) {
				t.Fatalf("Stream error = %v, want ErrInvalidResponse", lastErr)
			}
			if !strings.Contains(lastErr.Error(), "tool_calls[") {
				t.Fatalf("Stream error = %v, want an error naming the tool index", lastErr)
			}
		})
	}
}

// The identified case still streams, so the check refuses only what it must:
// an id and a name arriving in a later chunk than the first arguments
// fragment is ordinary, and OpenAI's own examples show it.
func TestStreamAcceptsAToolCallIdentifiedAfterItsArguments(t *testing.T) {
	t.Parallel()

	model := newToolStreamModel(t, []string{
		`{"index":0,"function":{"arguments":"{\"q\":"}}`,
		`{"index":0,"id":"call-1","type":"function","function":{"name":"search","arguments":"\"scope\"}"}}`,
	})

	var terminal *corechat.ResponseDelta
	for delta, err := range model.Stream(t.Context(), newToolStreamRequest(t)) {
		if err != nil {
			t.Fatalf("Stream: %v", err)
		}
		terminal = delta
	}
	if terminal == nil || terminal.FinishReason != corechat.FinishReasonToolCalls {
		t.Fatalf("terminal delta = %+v, want a tool_calls finish", terminal)
	}
}

func newToolStreamRequest(t *testing.T) *corechat.Request {
	t.Helper()
	return &corechat.Request{
		Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("search for scope"))},
		Options:  corechat.Options{Model: "gpt-5.2"},
	}
}

// newToolStreamModel streams one chunk per tool delta and closes with a
// tool_calls finish reason, which is the shape OpenAI ends a tool stream with.
func newToolStreamModel(t *testing.T, toolDeltas []string) corechat.Streamer {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		for _, toolDelta := range toolDeltas {
			fmt.Fprintf(writer, "data: %s\n\n", fmt.Sprintf(
				`{"id":"chatcmpl-tools","object":"chat.completion.chunk","created":1770000001,"model":"gpt-5.2",`+
					`"choices":[{"index":0,"delta":{"tool_calls":[%s]}}]}`, toolDelta))
		}
		fmt.Fprint(writer, "data: "+`{"id":"chatcmpl-tools","object":"chat.completion.chunk","created":1770000001,"model":"gpt-5.2",`+
			`"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`+"\n\n")
		fmt.Fprint(writer, "data: [DONE]\n\n")
	}))
	t.Cleanup(server.Close)

	adapter, err := scopeopenai.NewChatCompletions(t.Context(), scopeopenai.ChatCompletionsConfig{
		APIKey:         "test-key",
		BaseURL:        server.URL,
		DefaultOptions: corechat.Options{Model: "gpt-5.2"},
	})
	if err != nil {
		t.Fatalf("NewChatCompletions: %v", err)
	}
	return adapter
}
