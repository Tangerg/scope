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

func TestStreamPublishesFinishOnLastDeltaAfterTailUsage(t *testing.T) {
	for _, reason := range []string{"stop", "error"} {
		t.Run(reason, func(t *testing.T) {
			deltas, err := collectMistralStream(t, []string{
				`{"id":"c","model":"mistral-small-latest","choices":[{"index":0,"delta":{"content":"answer"}}]}`,
				fmt.Sprintf(`{"id":"c","model":"mistral-small-latest","choices":[{"index":0,"delta":{},"finish_reason":%q}]}`, reason),
				`{"id":"c","model":"mistral-small-latest","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":12}}`,
				"[DONE]",
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(deltas) != 3 {
				t.Fatalf("deltas = %d, want 3", len(deltas))
			}
			var accumulator corechat.ResponseAccumulator
			for index, delta := range deltas {
				if index < len(deltas)-1 && delta.FinishReason != "" {
					t.Fatalf("delta %d published completion before tail usage", index)
				}
				if addErr := accumulator.Add(delta); addErr != nil {
					t.Fatal(addErr)
				}
			}
			response, err := accumulator.Response()
			if err != nil {
				t.Fatal(err)
			}
			want := corechat.FinishReasonStop
			if reason == "error" {
				want = corechat.FinishReasonOther
				native, found, decodeErr := deltas[2].OutputMetadata.Extra.Decode[string]("mistral/native_finish_reason")
				if decodeErr != nil || !found || native != reason {
					t.Fatalf("terminal native reason = %q, found %v, error %v", native, found, decodeErr)
				}
			}
			if response.Text() != "answer" || response.Output.FinishReason != want || response.Metadata.Usage.OutputTokens != 12 {
				t.Fatalf("aggregated response = %#v", response)
			}
		})
	}
}

func TestStreamDoesNotPublishCompletionBeforeATailFailure(t *testing.T) {
	for name, tail := range map[string]string{
		"text after finish":       `{"id":"c","choices":[{"index":0,"delta":{"content":"late"}}]}`,
		"tool after finish":       `{"id":"c","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"late","function":{"name":"search","arguments":"{}"}}]}}]}`,
		"duplicate finish":        `{"id":"c","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		"malformed trailing JSON": "{",
	} {
		t.Run(name, func(t *testing.T) {
			deltas, err := collectMistralStream(t, []string{
				`{"id":"c","model":"mistral-small-latest","choices":[{"index":0,"delta":{"content":"answer"}}]}`,
				`{"id":"c","model":"mistral-small-latest","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
				tail,
				"[DONE]",
			})
			if err == nil {
				t.Fatal("invalid tail produced a successful stream")
			}
			for _, delta := range deltas {
				if delta.FinishReason != "" {
					t.Fatalf("published completion before tail error: %#v", delta)
				}
			}
		})
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
