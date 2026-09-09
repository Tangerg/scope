package anthropic_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/protocol/anthropic"
)

// input_json_delta carries only partial JSON; the id and name come from the
// content_block_start for that index. A Core tool-call delta cannot be built
// without them, so arguments for an unstarted block are buffered. Nothing used
// to flush or report a buffer still held at message_stop: the terminal
// response came back with a normal stop reason and the tool call simply
// absent, its arguments discarded.
func TestStreamRefusesArgumentsForAnUnstartedToolBlock(t *testing.T) {
	t.Parallel()

	model := newToolStreamModel(t, []string{
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"id\":9}"}}`,
	})

	var lastErr error
	for delta, err := range model.Stream(t.Context(), newToolStreamRequest(t)) {
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
	if !strings.Contains(lastErr.Error(), "content block 0") {
		t.Fatalf("Stream error = %v, want an error naming the content block", lastErr)
	}
}

// A started block still streams, so the check refuses only what it must.
func TestStreamAcceptsArgumentsForAStartedToolBlock(t *testing.T) {
	t.Parallel()

	model := newToolStreamModel(t, []string{
		`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu-1","name":"lookup","input":{}}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"id\":9}"}}`,
		`{"type":"content_block_stop","index":0}`,
	})

	var terminal *corechat.ResponseDelta
	for delta, err := range model.Stream(t.Context(), newToolStreamRequest(t)) {
		if err != nil {
			t.Fatalf("Stream: %v", err)
		}
		terminal = delta
	}
	if terminal == nil || terminal.FinishReason != corechat.FinishReasonToolCalls {
		t.Fatalf("terminal delta = %+v, want a tool_use stop reason", terminal)
	}
}

func newToolStreamRequest(t *testing.T) *corechat.Request {
	t.Helper()
	return &corechat.Request{
		Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("look it up"))},
		Options:  toolStreamOptions(),
	}
}

// Anthropic requires max_tokens on every request, so it belongs in both the
// defaults and the request rather than only one of them.
func toolStreamOptions() corechat.Options {
	maxOutputTokens := int64(64)
	return corechat.Options{Model: "claude-opus-4-6", MaxOutputTokens: &maxOutputTokens}
}

// newToolStreamModel wraps middle events in the message_start / message_delta /
// message_stop envelope every Anthropic stream carries.
func newToolStreamModel(t *testing.T, events []string) corechat.Streamer {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		all := append([]string{
			`{"type":"message_start","message":{"id":"msg-tools","type":"message","role":"assistant",` +
				`"model":"claude-opus-4-6","content":[],"stop_reason":null,"stop_sequence":null,` +
				`"usage":{"input_tokens":10,"output_tokens":0}}}`,
		}, events...)
		all = append(all,
			`{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":5}}`,
			`{"type":"message_stop"}`,
		)
		for _, event := range all {
			// Anthropic's SSE stream names each event, and the SDK's decoder
			// dispatches on that name rather than on the payload.
			var envelope struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal([]byte(event), &envelope); err != nil {
				t.Error(err)
				return
			}
			fmt.Fprintf(writer, "event: %s\ndata: %s\n\n", envelope.Type, event)
		}
	}))
	t.Cleanup(server.Close)

	adapter, err := anthropic.NewMessages(t.Context(), anthropic.MessagesConfig{
		APIKey:         "test-key",
		BaseURL:        server.URL,
		DefaultOptions: toolStreamOptions(),
	})
	if err != nil {
		t.Fatalf("NewMessages: %v", err)
	}
	return adapter
}
