package mistral_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
)

// A Core tool-call delta cannot carry arguments without an id and a name, so
// an index is buffered until Mistral sends both. Nothing used to flush or
// report a buffer still held when the finish reason arrived: the terminal
// chunk went out with a normal finish reason and the tool call simply absent,
// its arguments discarded.
//
// The refusal happens before the terminal chunk is yielded, because after that
// the caller has already been told the response is complete.
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
	}

	for name, toolCalls := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			model := newToolStreamModel(t, toolCalls)
			var lastErr error
			for delta, err := range model.Stream(t.Context(), newToolStreamRequest()) {
				if err != nil {
					lastErr = err
					break
				}
				if delta.FinishReason != "" {
					t.Fatalf("stream yielded a terminal delta for an unidentified tool call: %+v", delta)
				}
			}
			if lastErr == nil {
				t.Fatal("Stream yielded no error, want the unreported tool call refused")
			}
			if !errors.Is(lastErr, corechat.ErrInvalidResponse) {
				t.Fatalf("Stream error = %v, want ErrInvalidResponse", lastErr)
			}
			if !strings.Contains(lastErr.Error(), "tool call 0") {
				t.Fatalf("Stream error = %v, want an error naming the tool index", lastErr)
			}
		})
	}
}

// An id and a name arriving after the first arguments fragment is ordinary, so
// the check refuses only what it must.
func TestStreamAcceptsAToolCallIdentifiedAfterItsArguments(t *testing.T) {
	t.Parallel()

	model := newToolStreamModel(t, []string{
		`{"index":0,"function":{"arguments":"{\"q\":"}}`,
		`{"index":0,"id":"call-1","type":"function","function":{"name":"search","arguments":"\"scope\"}"}}`,
	})

	var terminal *corechat.ResponseDelta
	for delta, err := range model.Stream(t.Context(), newToolStreamRequest()) {
		if err != nil {
			t.Fatalf("Stream: %v", err)
		}
		if delta.FinishReason != "" {
			terminal = delta
		}
	}
	if terminal == nil || terminal.FinishReason != corechat.FinishReasonToolCalls {
		t.Fatalf("terminal delta = %+v, want a tool_calls finish", terminal)
	}
}

func newToolStreamRequest() *corechat.Request {
	return &corechat.Request{
		Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("search for scope"))},
	}
}

// newToolStreamModel streams one chunk per tool call and closes with the
// tool_calls finish reason Mistral ends a tool stream with.
func newToolStreamModel(t *testing.T, toolCalls []string) corechat.Streamer {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		for _, toolCall := range toolCalls {
			fmt.Fprintf(writer, "data: %s\n\n", fmt.Sprintf(
				`{"id":"cmpl-tools","model":"mistral-large-latest","choices":[{"index":0,`+
					`"delta":{"role":"assistant","tool_calls":[%s]},"finish_reason":null}]}`, toolCall))
		}
		fmt.Fprint(writer, "data: "+`{"id":"cmpl-tools","model":"mistral-large-latest","choices":[{"index":0,`+
			`"delta":{},"finish_reason":"tool_calls"}]}`+"\n\n")
		fmt.Fprint(writer, "data: [DONE]\n\n")
	}))
	t.Cleanup(server.Close)

	return newChatModel(t, server.URL)
}
