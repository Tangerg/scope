package openai_test

import (
	"testing"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/models/protocol/openai"
)

// A provider names the namespace of every extension key a binding writes, so
// one Core rejects as a namespace must fail at construction.
func TestBindingsRejectAProviderThatCannotNameExtensionKeys(t *testing.T) {
	const provider = "my provider"
	if err := (openai.ResponsesDialect{Provider: provider}).Validate(); err == nil {
		t.Error("Responses dialect accepted the provider")
	}
	if _, err := openai.NewChatCompletions(t.Context(), openai.ChatModelConfig{APIKey: "key", BaseURL: "https://api.example"}, openai.DialectConfig{Provider: provider}); err == nil {
		t.Error("Chat Completions accepted the provider")
	}
	if err := (openai.EmbeddingModelConfig{Provider: provider, APIKey: "key", BaseURL: "https://api.example", DefaultOptions: embedding.Options{Model: "m"}}).Validate(); err == nil {
		t.Error("embedding binding accepted the provider")
	}
}
