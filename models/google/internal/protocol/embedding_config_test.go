package protocol

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/embedding"
)

func TestEmbeddingDimensionsUseSDKRepresentation(t *testing.T) {
	for _, provider := range []string{"google", "vertexai"} {
		for _, dimensions := range []int64{1, 64, 128, 3073, math.MaxInt32, math.MaxInt32 + 1} {
			t.Run(provider+"/"+strconv.FormatInt(dimensions, 10), func(t *testing.T) {
				model := &EmbeddingModel{provider: provider}
				request, err := embedding.NewRequest([]string{"hello"})
				if err != nil {
					t.Fatal(err)
				}
				request.Options.Model = "selected-embedding-model"
				request.Options.Dimensions = new(dimensions)
				modelName, _, config, err := model.buildAPIRequest(request)
				if dimensions > math.MaxInt32 {
					if err == nil {
						t.Fatal("accepted dimensions that overflow the SDK representation")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if modelName != request.Options.Model || config.OutputDimensionality == nil || int64(*config.OutputDimensionality) != dimensions {
					t.Fatalf("model = %q, dimensions = %v; want model %q and dimensions %d", modelName, config.OutputDimensionality, request.Options.Model, dimensions)
				}
			})
		}
	}
}

func TestEmbeddingConfigUsesCoreDimensions(t *testing.T) {
	for _, provider := range []string{"google", "vertexai"} {
		for _, extension := range []string{
			`{"outputDimensionality":null}`,
			`{"outputDimensionality":0}`,
			`{"outputDimensionality":256}`,
		} {
			t.Run(provider+"/"+extension, func(t *testing.T) {
				model := &EmbeddingModel{provider: provider, defaultOptions: embedding.Options{Model: ModelGeminiEmbedding2}}
				request, err := embedding.NewRequest([]string{"hello"})
				if err != nil {
					t.Fatal(err)
				}
				if err := request.Options.Extensions.Set(protocolKey(provider, "embedding_request"), json.RawMessage(extension)); err != nil {
					t.Fatal(err)
				}
				if _, _, _, err := model.buildAPIRequest(request); err == nil || !strings.Contains(err.Error(), "owned by options.dimensions") {
					t.Fatalf("native dimensions error = %v", err)
				}
			})
		}
		t.Run(provider+"/native options", func(t *testing.T) {
			model := &EmbeddingModel{provider: provider, defaultOptions: embedding.Options{Model: ModelGeminiEmbedding2}}
			request, err := embedding.NewRequest([]string{"hello"})
			if err != nil {
				t.Fatal(err)
			}
			request.Options.Dimensions = new(int64(256))
			if extensionErr := request.Options.Extensions.Set(protocolKey(provider, "embedding_request"), json.RawMessage(`{"taskType":"RETRIEVAL_DOCUMENT","title":"document"}`)); extensionErr != nil {
				t.Fatal(extensionErr)
			}
			_, contents, config, err := model.buildAPIRequest(request)
			if err != nil {
				t.Fatal(err)
			}
			if config.OutputDimensionality == nil || *config.OutputDimensionality != 256 || config.TaskType != "RETRIEVAL_DOCUMENT" || config.Title != "document" || len(contents) != 1 || contents[0].Parts[0].Text != "hello" {
				t.Fatalf("embedding config = %#v, content = %#v", config, contents)
			}
			for _, extension := range []string{`{"output_dimensionality":256}`, `{"task_type":"RETRIEVAL_DOCUMENT"}`, `{"unknown":true}`} {
				if err := request.Options.Extensions.Set(protocolKey(provider, "embedding_request"), json.RawMessage(extension)); err != nil {
					t.Fatal(err)
				}
				if _, _, _, err := model.buildAPIRequest(request); err == nil {
					t.Fatalf("noncanonical embedding extension accepted: %s", extension)
				}
			}
		})
	}
}
