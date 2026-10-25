package providerconformance_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/models/bedrock"
	"github.com/Tangerg/scope/models/google"
	"github.com/Tangerg/scope/models/mistral"
	"github.com/Tangerg/scope/models/ollama"
	"github.com/Tangerg/scope/models/protocol/anthropic"
	"github.com/Tangerg/scope/models/protocol/openai"
)

func TestChatLocalRejectionPreservesNonExecutionEvidence(t *testing.T) {
	for name, create := range map[string]func(*testing.T, string) (chat.Model, error){
		"openai-chat": func(t *testing.T, url string) (chat.Model, error) {
			return openai.NewChatCompletions(t.Context(), openai.ChatCompletionsConfig{APIKey: "test", BaseURL: url, DefaultOptions: chat.Options{Model: "test"}})
		},
		"openai-responses": func(t *testing.T, url string) (chat.Model, error) {
			return openai.NewResponses(t.Context(), openai.ResponsesConfig{APIKey: "test", BaseURL: url, DefaultOptions: chat.Options{Model: "test"}})
		},
		"anthropic": func(t *testing.T, url string) (chat.Model, error) {
			return anthropic.NewMessages(t.Context(), anthropic.MessagesConfig{APIKey: "test", BaseURL: url, DefaultOptions: chat.Options{Model: "test", MaxOutputTokens: new(int64(32))}})
		},
		"google": func(t *testing.T, url string) (chat.Model, error) {
			return google.NewChat(t.Context(), google.ChatConfig{APIKey: "test", BaseURL: url, DefaultOptions: chat.Options{Model: "test"}})
		},
		"mistral": func(t *testing.T, url string) (chat.Model, error) {
			return mistral.NewChat(t.Context(), mistral.ChatConfig{APIKey: "test", BaseURL: url, DefaultOptions: chat.Options{Model: "test"}})
		},
		"ollama": func(t *testing.T, url string) (chat.Model, error) {
			return ollama.NewChat(t.Context(), ollama.ChatConfig{BaseURL: url, DefaultOptions: chat.Options{Model: "test"}})
		},
		"bedrock": func(t *testing.T, url string) (chat.Model, error) {
			return bedrock.NewChat(t.Context(), bedrock.ChatConfig{BaseURL: url, Region: "us-east-1", Credentials: &bedrock.Credentials{AccessKeyID: "test", SecretAccessKey: "test"}, DefaultOptions: chat.Options{Model: "test"}})
		},
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			server := modeltest.JSONServer(http.StatusBadRequest, `{}`, func(*http.Request) { calls++ })
			defer server.Close()
			model, err := create(t, server.URL)
			if err != nil {
				t.Fatal(err)
			}
			_, err = model.Call(t.Context(), nil)
			if !errors.Is(err, chat.ErrNotExecuted) || !errors.Is(err, chat.ErrDefiniteFailure) || calls != 0 {
				t.Fatalf("error=%v provider calls=%d", err, calls)
			}
		})
	}
}
