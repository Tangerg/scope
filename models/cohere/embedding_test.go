package cohere_test

import (
	"testing"

	cohere "github.com/cohere-ai/cohere-go/v2"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/modeltest"
	scohere "github.com/Tangerg/scope/models/cohere"
)

const cohereEmbeddingResponseJSON = `{
  "id": "embed-id",
  "embeddings": {"float": [[0.1, 0.2], [0.3, 0.4]]},
  "texts": ["foo", "bar"],
  "meta": {"billed_units": {"input_tokens": 2}}
}`

func TestEmbeddingModel(t *testing.T) {
	modeltest.RunEmbeddingContract(t, modeltest.EmbeddingContract{
		ModelID:      "embed-english-v3.0",
		Response:     cohereEmbeddingResponseJSON,
		ExpectedPath: "/v2/embed",
		Build: func(t *testing.T, baseURL string) embedding.Model {
			t.Helper()
			var extensions metadata.Extensions
			if err := extensions.Set(scohere.EmbeddingRequestExtensionKey, cohere.V2EmbedRequest{InputType: cohere.EmbedInputTypeSearchDocument}); err != nil {
				t.Fatal(err)
			}
			model, err := scohere.NewEmbeddingModel(t.Context(), scohere.EmbeddingModelConfig{
				APIKey: "test-key",
				DefaultOptions: embedding.Options{
					Model: "embed-english-v3.0", Extensions: extensions,
				},
				BaseURL: baseURL,
			})
			if err != nil {
				t.Fatal(err)
			}
			return model
		},
	})
}
