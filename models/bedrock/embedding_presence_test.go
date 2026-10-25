package bedrock_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/models/bedrock"
)

func TestEmbeddingRequiresNativeCoordinatesAndPreservesUsagePresence(t *testing.T) {
	for _, family := range []string{"titan", "cohere"} {
		for _, test := range []struct {
			name, value, usage string
			valid, known       bool
		}{
			{"null", "null", "", false, false}, {"missing_usage", "0", "", true, false}, {"zero_usage", "0", `,"inputTextTokenCount":0`, true, true},
		} {
			t.Run(family+test.name, func(t *testing.T) {
				body := `{"embedding":[` + test.value + `,1]` + test.usage + `}`
				modelName := "amazon.titan-embed-text-v2:0"
				var extensions metadata.Extensions
				if family == "cohere" {
					body = `{"embeddings":{"float":[[` + test.value + `,1]]}}`
					modelName = "cohere.embed-v4:0"
					if err := extensions.Set(bedrock.EmbeddingRequestExtensionKey, bedrock.EmbeddingRequestOptions{InputType: "search_document"}); err != nil {
						t.Fatal(err)
					}
				}
				server := modeltest.JSONServer(http.StatusOK, body)
				defer server.Close()
				model, err := bedrock.NewEmbeddingModel(t.Context(), bedrock.EmbeddingModelConfig{Region: "us-east-1", Credentials: &bedrock.Credentials{AccessKeyID: "test", SecretAccessKey: "test"}, BaseURL: server.URL, DefaultOptions: embedding.Options{Model: modelName, Extensions: extensions}})
				if err != nil {
					t.Fatal(err)
				}
				request, err := embedding.NewRequest([]string{"text"})
				if err != nil {
					t.Fatal(err)
				}
				response, err := model.Call(t.Context(), request)
				if !test.valid {
					if !errors.Is(err, embedding.ErrInvalidResponse) {
						t.Fatalf("response=%v error=%v", response, err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if (response.Metadata.Usage != nil) != (family == "titan" && test.known) {
					t.Fatalf("usage=%v", response.Metadata.Usage)
				}
			})
		}
	}
}
