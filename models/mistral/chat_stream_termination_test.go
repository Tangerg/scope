package mistral_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/mistral"
)

// A finish_reason is the only thing in a Mistral stream that says the answer
// is whole. Neither the closing [DONE] marker nor a body that simply ends
// carries that claim, so a stream missing it delivered a fragment.
func TestStreamRejectsStreamWithoutFinishReason(t *testing.T) {
	t.Parallel()

	for _, sample := range []struct {
		name   string
		chunks []string
	}{
		{
			name: "done marker without finish reason",
			chunks: []string{
				`{"id":"c","model":"mistral-small-latest","choices":[{"index":0,"delta":{"content":"half"}}]}`,
				"[DONE]",
			},
		},
		{
			name: "body ends mid-stream",
			chunks: []string{
				`{"id":"c","model":"mistral-small-latest","choices":[{"index":0,"delta":{"content":"half"}}]}`,
			},
		},
	} {
		t.Run(sample.name, func(t *testing.T) {
			deltas, err := collectMistralStream(t, sample.chunks)
			if len(deltas) != 1 {
				t.Fatalf("deltas = %d, want the one chunk that did arrive", len(deltas))
			}
			if !errors.Is(err, corechat.ErrInvalidResponse) {
				t.Fatalf("Stream() error = %v, want ErrInvalidResponse", err)
			}
		})
	}
}

// A stream that reports a finish reason is complete and must not be flagged.
func TestStreamAcceptsStreamWithFinishReason(t *testing.T) {
	t.Parallel()

	deltas, err := collectMistralStream(t, []string{
		`{"id":"c","model":"mistral-small-latest","choices":[{"index":0,"delta":{"content":"half"}}]}`,
		`{"id":"c","model":"mistral-small-latest","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		"[DONE]",
	})
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

// model_length is the model's own context filling rather than the caller's
// budget, and both truncate the answer.
func TestStreamMapsModelLengthAsTruncation(t *testing.T) {
	t.Parallel()

	deltas, err := collectMistralStream(t, []string{
		`{"id":"c","model":"mistral-small-latest","choices":[{"index":0,"delta":{},"finish_reason":"model_length"}]}`,
		"[DONE]",
	})
	if err != nil {
		t.Fatalf("Stream() error = %v, want nil", err)
	}
	if deltas[0].FinishReason != corechat.FinishReasonLength {
		t.Fatalf("FinishReason = %q, want %q", deltas[0].FinishReason, corechat.FinishReasonLength)
	}
}

func collectMistralStream(t *testing.T, chunks []string) ([]*corechat.ResponseDelta, error) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		for _, chunk := range chunks {
			fmt.Fprintf(writer, "data: %s\n\n", chunk)
		}
	}))
	t.Cleanup(server.Close)

	model, err := mistral.NewChat(t.Context(), mistral.ChatConfig{
		APIKey:         "test-key",
		BaseURL:        server.URL,
		DefaultOptions: corechat.Options{Model: "mistral-small-latest"},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := &corechat.Request{
		Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("hello"))},
	}

	var deltas []*corechat.ResponseDelta
	var streamErr error
	for delta, err := range model.Stream(t.Context(), request) {
		if err != nil {
			streamErr = err
			break
		}
		deltas = append(deltas, delta)
	}
	return deltas, streamErr
}
