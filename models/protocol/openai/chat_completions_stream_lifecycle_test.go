package openai_test

import (
	"errors"
	"fmt"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/modeltest"
	scopeopenai "github.com/Tangerg/scope/models/protocol/openai"
)

func TestStreamRejectsGenerationAfterFinish(t *testing.T) {
	for name, tail := range map[string]string{
		"text":          `{"index":0,"delta":{"content":"late"}}`,
		"reasoning":     `{"index":0,"delta":{"reasoning_content":"late"}}`,
		"refusal":       `{"index":0,"delta":{"refusal":"late"}}`,
		"tool":          `{"index":0,"delta":{"tool_calls":[{"index":0,"id":"late","type":"function","function":{"name":"lookup","arguments":"{}"}}]}}`,
		"audio":         `{"index":0,"delta":{"audio":{"id":"late","data":"YXVkaW8="}}}`,
		"second finish": `{"index":0,"delta":{},"finish_reason":"length"}`,
	} {
		t.Run(name, func(t *testing.T) {
			adapter := newLifecycleStreamModel(t, []string{
				`{"index":0,"delta":{"content":"answer"},"finish_reason":"stop"}`, tail,
			})
			request := newToolStreamRequest(t)
			response, err := adapter.Call(t.Context(), request)
			if response != nil || !errors.Is(err, corechat.ErrInvalidResponse) {
				t.Fatalf("Call returned %#v, %v; want ErrInvalidResponse", response, err)
			}
			var streamErr error
			for delta, err := range adapter.Stream(t.Context(), request) {
				if err != nil {
					streamErr = err
					break
				}
				if delta.FinishReason != "" {
					t.Fatalf("invalid stream completed: %#v", delta)
				}
			}
			if !errors.Is(streamErr, corechat.ErrInvalidResponse) {
				t.Fatalf("Stream error = %v", streamErr)
			}
		})
	}
}

func TestStreamAcceptsAudioExpiryAndUsageAfterFinish(t *testing.T) {
	adapter := newLifecycleStreamModel(t, []string{
		`{"index":0,"delta":{"content":"answer","audio":{"id":"audio-1","data":"YXVkaW8="}},"finish_reason":"stop"}`,
		`{"index":0,"delta":{"audio":{"expires_at":1770000002}}}`,
	})
	response, err := adapter.Call(t.Context(), newToolStreamRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	parts := response.Output.Message.Parts
	if len(parts) != 2 || parts[0].Text != "answer" || parts[1].Media == nil || parts[1].Media.ID != "audio-1" || response.Metadata.Usage.OutputTokens != 2 || response.Output.FinishReason != corechat.FinishReasonStop {
		t.Fatalf("audio tail lost output or usage: %#v", response)
	}
}

func newLifecycleStreamModel(t *testing.T, choices []string) *scopeopenai.ChatCompletions {
	t.Helper()
	chunks := make([]string, 0, len(choices)+1)
	for _, choice := range choices {
		chunks = append(chunks, fmt.Sprintf(`{"id":"lifecycle","model":"model","choices":[%s]}`, choice))
	}
	chunks = append(chunks, `{"id":"lifecycle","model":"model","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`)
	server := modeltest.OpenAISSEServer(chunks)
	t.Cleanup(server.Close)
	adapter, err := scopeopenai.NewCompatibleChatCompletions(t.Context(), scopeopenai.ChatCompletionsConfig{
		APIKey: "test-key", BaseURL: server.URL, DefaultOptions: corechat.Options{Model: "model"},
	}, scopeopenai.ReasoningContentDialect("test"))
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}
