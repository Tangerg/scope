package protocol

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"google.golang.org/genai"

	"github.com/Tangerg/scope/core/invocation"
)

func TestEmbeddingRejectsUnrecognizedSDKProjectionBeforeDispatch(t *testing.T) {
	for _, test := range []struct {
		name string
		body map[string]any
	}{
		{"unknown envelope", map[string]any{}},
		{"ambiguous envelope", map[string]any{"instances": []any{}, "content": map[string]any{}}},
		{"invalid config", map[string]any{"instances": []any{}, "parameters": "invalid"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				writer.Header().Set("Content-Type", "application/json")
				_, _ = writer.Write([]byte(`{"predictions":[{"embeddings":{"values":[1,2]}}]}`))
			}))
			t.Cleanup(server.Close)
			projection := &embeddingRequest{autoTruncate: new(false), config: &genai.EmbedContentConfig{}}
			if err := projection.configure(genai.BackendVertexAI); err != nil {
				t.Fatal(err)
			}
			projection.config.HTTPOptions.ExtrasRequestProvider(test.body)
			source := &embeddingBodyProbe{Reader: bytes.NewReader([]byte(`{}`))}
			request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/model:predict", source)
			if err != nil {
				t.Fatal(err)
			}
			client := &http.Client{Transport: &nativeResponseTransport{next: server.Client().Transport, embeddingRequest: projection}}
			response, err := client.Do(request)
			failure, ok := errors.AsType[*invocation.Failure](err)
			if response != nil || !ok || failure.Kind != invocation.FailureNotExecuted || calls.Load() != 0 || source.closed != 1 {
				t.Fatalf("response=%v error=%v calls=%d closed=%d; want rejection before dispatch and closed request body", response, err, calls.Load(), source.closed)
			}
		})
	}
}
