package bedrock_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/models/bedrock"
)

func TestEmbeddingValidatesDefaultDimensions(t *testing.T) {
	server := modeltest.JSONServer(http.StatusOK, `{"embedding":[1,2],"inputTextTokenCount":1}`)
	defer server.Close()
	model, err := bedrock.NewEmbeddingModel(t.Context(), bedrock.EmbeddingModelConfig{Region: "us-east-1", Credentials: &bedrock.Credentials{AccessKeyID: "test", SecretAccessKey: "test"}, BaseURL: server.URL, DefaultOptions: embedding.Options{Model: "amazon.titan-embed-text-v2:0", Dimensions: new(int64(256))}})
	if err != nil {
		t.Fatal(err)
	}
	request, _ := embedding.NewRequest([]string{"text"})
	if _, err := model.Call(t.Context(), request); !errors.Is(err, embedding.ErrInvalidResponse) {
		t.Fatalf("default dimensions ignored: %v", err)
	}
	if request.Options.Dimensions != nil {
		t.Fatal("Call mutated dimensions")
	}
}
