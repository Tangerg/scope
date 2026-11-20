package protocol_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/models/google/internal/protocol"
)

// The SDK defaults the Gemini API to v1 since v1.78.0, but the bindings rely on
// surfaces served only on v1beta.
func TestGeminiAPIBindingSelectsItsAPIVersion(t *testing.T) {
	for _, test := range []struct{ configured, wantPrefix string }{
		{"", "/v1beta/"},
		{"v1", "/v1/"},
	} {
		var path string
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			path = request.URL.Path
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"embeddings":[{"values":[1,2]}]}`))
		}))
		model, err := protocol.NewEmbeddingModel(t.Context(), protocol.EmbeddingModelConfig{
			Provider:       "google",
			Client:         protocol.ClientConfig{APIKey: "test-key", BaseURL: server.URL, APIVersion: test.configured},
			DefaultOptions: embedding.Options{Model: protocol.ModelGeminiEmbedding2},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := model.Call(t.Context(), &embedding.Request{Texts: []string{"text"}}); err != nil {
			t.Fatal(err)
		}
		server.Close()
		if !strings.HasPrefix(path, test.wantPrefix) {
			t.Fatalf("APIVersion %q requested %q, want prefix %q", test.configured, path, test.wantPrefix)
		}
	}
}
