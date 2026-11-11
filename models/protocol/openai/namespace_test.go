package openai

import (
	"testing"

	"github.com/Tangerg/scope/core/embedding"
)

func TestModalityExtensionsUseTheBoundProviderNamespace(t *testing.T) {
	t.Parallel()

	model := &EmbeddingModel{provider: "azureopenai"}
	request := &embedding.Request{Texts: []string{"text"}}
	if err := request.Options.Extensions.Set(EmbeddingRequestExtensionKey, map[string]any{embeddingFieldModel: "foreign"}); err != nil {
		t.Fatal(err)
	}
	if _, err := model.buildAPIEmbeddingRequest(request); err != nil {
		t.Fatalf("another provider's namespace was read: %v", err)
	}
	if err := request.Options.Extensions.Set("azureopenai"+EmbeddingRequestExtensionSuffix, map[string]any{embeddingFieldModel: "owned"}); err != nil {
		t.Fatal(err)
	}
	if _, err := model.buildAPIEmbeddingRequest(request); err == nil {
		t.Fatal("the bound namespace could override a Core-owned field")
	}
}
