package ollama

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/embedding"
)

func TestEmbeddingNativeFieldsCannotOwnCoreInputs(t *testing.T) {
	for _, raw := range []string{
		`{"dimensions":8}`, `{"dimensions":0}`, `{"dimensions":null}`,
		`{"model":"other"}`, `{"input":["other"]}`, `{"input":null}`,
	} {
		t.Run(raw, func(t *testing.T) {
			request := &embedding.Request{Texts: []string{"hello"}}
			if err := request.Options.Extensions.Set(EmbeddingRequestExtensionKey, json.RawMessage(raw)); err != nil {
				t.Fatal(err)
			}
			model := &EmbeddingModel{defaultOptions: embedding.Options{Model: "embedding-model"}}
			if _, err := model.buildAPIRequest(request); err == nil || !strings.Contains(err.Error(), "owned by Core") {
				t.Fatalf("native request %s: error = %v; want Core ownership rejection", raw, err)
			}
		})
	}
}

func TestEmbeddingKeepsProviderOnlyOptions(t *testing.T) {
	request := &embedding.Request{Texts: []string{"hello"}, Options: embedding.Options{Dimensions: new(int64(8))}}
	if err := request.Options.Extensions.Set(EmbeddingRequestExtensionKey, json.RawMessage(`{"truncate":false,"keep_alive":"5m","options":{"num_ctx":4096}}`)); err != nil {
		t.Fatal(err)
	}
	model := &EmbeddingModel{defaultOptions: embedding.Options{Model: "embedding-model"}}
	params, err := model.buildAPIRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if inputs, ok := params.Input.([]string); !ok || len(inputs) != 1 || inputs[0] != "hello" {
		t.Fatalf("embedding input = %#v", params.Input)
	}
	if params.Model != "embedding-model" || params.Dimensions != 8 || params.Truncate == nil || *params.Truncate || params.KeepAlive == nil || params.Options["num_ctx"] != float64(4096) {
		t.Fatalf("embedding wire request = %#v", params)
	}
}
