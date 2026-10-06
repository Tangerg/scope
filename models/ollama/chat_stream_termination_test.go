package ollama_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/ollama"
)

// An Ollama stream marks the end with done:true. A body that stops before it
// arrives has delivered a partial answer, and the unary path already refuses
// exactly that response, so the stream must not pass it off as finished.
func TestStreamRejectsBodyThatEndsBeforeDone(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprintln(writer, `{"model":"qwen3:8b","message":{"role":"assistant","content":"half"},"done":false}`)
	}))
	t.Cleanup(server.Close)

	deltas, err := collectOllamaStream(t, server.URL)
	if len(deltas) != 1 {
		t.Fatalf("deltas = %d, want the one chunk that did arrive", len(deltas))
	}
	if !errors.Is(err, corechat.ErrInvalidResponse) {
		t.Fatalf("Stream() error = %v, want ErrInvalidResponse", err)
	}
}

// A stream that reaches done:true is complete and must not be flagged.
func TestStreamAcceptsTerminatedBody(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprintln(writer, `{"model":"qwen3:8b","message":{"role":"assistant","content":"half"},"done":false}`)
		fmt.Fprintln(writer, `{"model":"qwen3:8b","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop"}`)
	}))
	t.Cleanup(server.Close)

	deltas, err := collectOllamaStream(t, server.URL)
	if err != nil {
		t.Fatalf("Stream() error = %v, want nil", err)
	}
	if len(deltas) != 2 {
		t.Fatalf("deltas = %d, want 2", len(deltas))
	}
	if deltas[1].FinishReason != corechat.FinishReasonStop {
		t.Fatalf("terminal FinishReason = %q, want %q", deltas[1].FinishReason, corechat.FinishReasonStop)
	}
}

// Two terminal chunks would leave the finish reason ambiguous.
func TestStreamRejectsSecondDoneChunk(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprintln(writer, `{"model":"qwen3:8b","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop"}`)
		fmt.Fprintln(writer, `{"model":"qwen3:8b","message":{"role":"assistant","content":""},"done":true,"done_reason":"length"}`)
	}))
	t.Cleanup(server.Close)

	if _, err := collectOllamaStream(t, server.URL); err == nil {
		t.Fatal("Stream() error = nil, want a duplicate-terminal error")
	}
}

func collectOllamaStream(t *testing.T, baseURL string) ([]*corechat.ResponseDelta, error) {
	t.Helper()
	adapter, err := ollama.NewChat(t.Context(), ollama.ChatConfig{
		DefaultOptions: corechat.Options{Model: "qwen3:8b"},
		BaseURL:        baseURL,
	})
	if err != nil {
		t.Fatalf("NewChat: %v", err)
	}

	var deltas []*corechat.ResponseDelta
	var streamErr error
	for delta, err := range adapter.Stream(t.Context(), newProtocolChatRequest(t)) {
		if err != nil {
			streamErr = err
			break
		}
		deltas = append(deltas, delta)
	}
	return deltas, streamErr
}

func TestStreamDoesNotCompleteBeforeItsNativeBody(t *testing.T) {
	for name, tail := range map[string]string{
		"content after done":   `{"model":"qwen3:8b","message":{"role":"assistant","content":"late"},"done":false}`,
		"error after done":     `{"error":"tail failure"}`,
		"malformed after done": `{"message":`,
		"second done":          `{"model":"qwen3:8b","message":{"role":"assistant","content":""},"done":true,"done_reason":"length"}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "application/x-ndjson")
				fmt.Fprintln(writer, `{"model":"qwen3:8b","message":{"role":"assistant","content":"answer"},"done":false}`)
				fmt.Fprintln(writer, `{"model":"qwen3:8b","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop"}`)
				fmt.Fprintln(writer, tail)
			}))
			t.Cleanup(server.Close)
			deltas, err := collectOllamaStream(t, server.URL)
			if err == nil {
				t.Fatal("invalid native tail returned success")
			}
			for _, delta := range deltas {
				if delta.FinishReason != "" {
					t.Fatalf("successful completion preceded tail failure: %#v", delta)
				}
			}
			if len(deltas) != 1 || deltas[0].Parts[0].Text != "answer" {
				t.Fatalf("valid partial response changed: %#v", deltas)
			}
		})
	}
}
