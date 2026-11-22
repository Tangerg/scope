package protocol

import (
	"testing"

	"github.com/Tangerg/scope/core/embedding"
)

// A provider names the namespace of every extension key a binding writes, so
// one Core rejects as a namespace must fail at construction.
func TestBindingsRejectAProviderThatCannotNameExtensionKeys(t *testing.T) {
	client := ClientConfig{APIKey: "key"}
	if err := (ChatConfig{Provider: "my provider", Client: client}).Validate(); err == nil {
		t.Error("chat binding accepted the provider")
	}
	if err := (EmbeddingModelConfig{Provider: "my provider", Client: client, DefaultOptions: embedding.Options{Model: "m"}}).Validate(); err == nil {
		t.Error("embedding binding accepted the provider")
	}
	// The model may come from each request; Core requires it of the resolved options.
	if err := (EmbeddingModelConfig{Provider: "google", Client: client}).Validate(); err != nil {
		t.Errorf("embedding binding refused defaults without a model: %v", err)
	}
}
