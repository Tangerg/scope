package protocol_test

import (
	jsonv2 "encoding/json/v2"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"google.golang.org/genai"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/models/google/internal/protocol"
)

func TestVertexEmbeddingForwardsSmallModelDimensions(t *testing.T) {
	const dimensions = 64
	model := newVertexEmbeddingWithHandler(t, func(writer http.ResponseWriter, request *http.Request) {
		var body struct {
			Parameters struct {
				OutputDimensionality int64 `json:"outputDimensionality"`
			} `json:"parameters"`
		}
		if err := jsonv2.UnmarshalRead(request.Body, &body); err != nil {
			t.Error(err)
		}
		if body.Parameters.OutputDimensionality != dimensions {
			t.Errorf("outputDimensionality = %d, want %d", body.Parameters.OutputDimensionality, dimensions)
		}
		if !strings.HasSuffix(request.URL.Path, "/text-embedding-005:predict") {
			t.Errorf("embedding model route = %q", request.URL.Path)
		}
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(writer, `{"predictions":[{"embeddings":{"values":[%s1]}}]}`, strings.Repeat("0,", dimensions-1))
	})
	request, err := embedding.NewRequest([]string{"hello"})
	if err != nil {
		t.Fatal(err)
	}
	request.Options.Dimensions = new(int64(dimensions))
	response, err := model.Call(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Outputs) != 1 || len(response.Outputs[0].Embedding) != dimensions || response.Metadata.Model != "text-embedding-005" {
		t.Fatalf("response = %#v", response)
	}
}

func TestVertexEmbeddingRejectsInvalidDimensionsBeforeIO(t *testing.T) {
	var calls atomic.Int32
	model := newVertexEmbeddingWithHandler(t, func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writer.WriteHeader(http.StatusBadRequest)
	})
	for _, dimensions := range []int64{-1, 0, math.MaxInt32 + 1} {
		t.Run(fmt.Sprint(dimensions), func(t *testing.T) {
			request, err := embedding.NewRequest([]string{"hello"})
			if err != nil {
				t.Fatal(err)
			}
			request.Options.Dimensions = new(dimensions)
			if response, err := model.Call(t.Context(), request); response != nil || err == nil {
				t.Errorf("response = %#v, error = %v; want invalid dimensions", response, err)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("HTTP calls = %d, want no I/O", calls.Load())
	}
}

func newVertexEmbeddingWithHandler(t *testing.T, handler http.HandlerFunc) *protocol.EmbeddingModel {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	model, err := protocol.NewEmbeddingModel(t.Context(), protocol.EmbeddingModelConfig{
		Provider: "vertexai",
		Client: protocol.ClientConfig{
			Backend: genai.BackendVertexAI, Project: "test-project", Location: "us-central1",
			BaseURL: server.URL, HTTPClient: server.Client(),
		},
		DefaultOptions: embedding.Options{Model: "text-embedding-005"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return model
}
