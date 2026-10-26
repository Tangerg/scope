package bedrock_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/models/bedrock"
)

func TestChatRejectsUnknownNativeControlsBeforeIO(t *testing.T) {
	for _, native := range []string{
		`{"max_tokens":10}`,
		`{"guardrail":{"identifier":"guard","version":"1","stream_processing_mode":"async"}}`,
		`null`,
	} {
		t.Run(native, func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				writer.WriteHeader(http.StatusBadRequest)
			}))
			defer server.Close()
			model := newBedrockBehaviorChat(t, server.URL)
			request := newBedrockBehaviorRequest(t)
			if err := request.Options.Extensions.Set(bedrock.ChatRequestExtensionKey, json.RawMessage(native)); err != nil {
				t.Fatal(err)
			}
			response, err := model.Call(t.Context(), request)
			if response != nil || chat.ClassifyFailure(err) != chat.CallFailureNotExecuted || calls.Load() != 0 {
				t.Fatalf("response=%v error=%v provider calls=%d", response, err, calls.Load())
			}
		})
	}
}

func TestEmbeddingRejectsUnknownNativeControlsBeforeIO(t *testing.T) {
	for _, native := range []string{`{"dimensions":256}`, `{"truncate_input":false}`, `null`} {
		t.Run(native, func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				writer.Header().Set("Content-Type", "application/json")
				_, _ = writer.Write([]byte(`{"embedding":[1,2],"inputTextTokenCount":1}`))
			}))
			defer server.Close()
			var extensions metadata.Extensions
			if err := extensions.Set(bedrock.EmbeddingRequestExtensionKey, json.RawMessage(native)); err != nil {
				t.Fatal(err)
			}
			model, err := bedrock.NewEmbeddingModel(t.Context(), bedrock.EmbeddingModelConfig{
				Region:      "us-east-1",
				BaseURL:     server.URL,
				Credentials: &bedrock.Credentials{AccessKeyID: "test", SecretAccessKey: "test"},
				DefaultOptions: embedding.Options{
					Model: "amazon.titan-embed-text-v2:0", Extensions: extensions,
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			request, err := embedding.NewRequest([]string{"text"})
			if err != nil {
				t.Fatal(err)
			}
			response, err := model.Call(t.Context(), request)
			if response != nil || err == nil || calls.Load() != 0 {
				t.Fatalf("response=%v error=%v provider calls=%d", response, err, calls.Load())
			}
		})
	}
}
