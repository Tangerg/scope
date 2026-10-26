package mistral_test

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/models/mistral"
)

const mistralEmbedResponseJSON = `{
  "object": "list",
  "model": "mistral-embed",
  "data": [
    {"object":"embedding","index":0,"embedding":[0.1,0.2,0.3]},
    {"object":"embedding","index":1,"embedding":[0.4,0.5,0.6]}
  ],
  "usage": {"prompt_tokens": 6, "total_tokens": 6}
}`

func TestEmbeddingModel(t *testing.T) {
	modeltest.RunEmbeddingContract(t, modeltest.EmbeddingContract{
		InputField:         "input",
		ExpectedEmbeddings: [][]float64{{0.1, 0.2, 0.3}, {0.4, 0.5, 0.6}},
		ModelID:            mistral.ModelEmbed,
		Response:           mistralEmbedResponseJSON,
		Build: func(t *testing.T, baseURL string) embedding.Model {
			t.Helper()
			opts := embedding.Options{Model: mistral.ModelEmbed}
			err := opts.Validate()
			if err != nil {
				t.Fatalf("NewOptions: %v", err)
			}
			m, err := mistral.NewEmbeddingModel(t.Context(), mistral.EmbeddingModelConfig{
				APIKey:         "test-key",
				DefaultOptions: opts,
				BaseURL:        baseURL,
			})
			if err != nil {
				t.Fatalf("NewEmbeddingModel: %v", err)
			}
			return m
		},
	})
}

func TestEmbeddingRejectsCompetingNativeControls(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		http.Error(writer, "unexpected provider request", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	model, err := mistral.NewEmbeddingModel(t.Context(), mistral.EmbeddingModelConfig{
		APIKey: "test-key", BaseURL: server.URL, DefaultOptions: embedding.Options{Model: mistral.ModelCodestralEmbed},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, native := range []string{
		`{"output_dimension":3}`, `{"dimensions":3}`, `{"model":"other"}`, `{"input":["other"]}`,
		`{"encoding_format":"base64"}`, `{"output_dtype":"int8"}`, `null`,
	} {
		t.Run(native, func(t *testing.T) {
			request, err := embedding.NewRequest([]string{"text"})
			if err != nil {
				t.Fatal(err)
			}
			if err := request.Options.Extensions.Set(mistral.EmbeddingRequestExtensionKey, json.RawMessage(native)); err != nil {
				t.Fatal(err)
			}
			if _, err := model.Call(t.Context(), request); err == nil {
				t.Fatal("competing embedding control was accepted")
			}
		})
	}
	if hits.Load() != 0 {
		t.Fatalf("provider calls = %d", hits.Load())
	}
}

func TestEmbeddingPreservesMetadataPrecision(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body struct {
			Metadata map[string]json.RawMessage `json:"metadata"`
		}
		if err := jsonv2.UnmarshalRead(request.Body, &body); err != nil {
			t.Error(err)
			return
		}
		if string(body.Metadata["document_id"]) != "9007199254740993" {
			t.Errorf("document_id = %s", body.Metadata["document_id"])
		}
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprint(writer, `{"data":[{"index":0,"embedding":[1]}],"model":"codestral-embed-2505","usage":{"prompt_tokens":0}}`)
	}))
	t.Cleanup(server.Close)
	model, err := mistral.NewEmbeddingModel(t.Context(), mistral.EmbeddingModelConfig{
		APIKey: "test-key", BaseURL: server.URL, DefaultOptions: embedding.Options{Model: mistral.ModelCodestralEmbed},
	})
	if err != nil {
		t.Fatal(err)
	}
	request, _ := embedding.NewRequest([]string{"text"})
	if setErr := request.Options.Extensions.Set(mistral.EmbeddingRequestExtensionKey, json.RawMessage(`{"metadata":{"document_id":9007199254740993}}`)); setErr != nil {
		t.Fatal(setErr)
	}
	response, err := model.Call(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if response.Metadata.Usage == nil || response.Metadata.Usage.InputTokens != 0 {
		t.Fatalf("explicit zero usage = %#v", response.Metadata.Usage)
	}
}

func TestEmbeddingRejectsMalformedNativeResults(t *testing.T) {
	for _, data := range []string{
		`null`, `[]`, `[null]`, `[{"embedding":[1]}]`, `[{"index":null,"embedding":[1]}]`,
		`[{"index":0}]`, `[{"index":0,"embedding":[]}]`, `[{"index":0,"embedding":[null]}]`,
		`[{"index":-1,"embedding":[1]}]`, `[{"index":1,"embedding":[1]}]`,
		`[{"index":0,"embedding":[1]},{"index":0,"embedding":[2]}]`,
	} {
		t.Run(data, func(t *testing.T) {
			server := modeltest.JSONServer(http.StatusOK, `{"model":"mistral-embed","data":`+data+`}`)
			t.Cleanup(server.Close)
			model, err := mistral.NewEmbeddingModel(t.Context(), mistral.EmbeddingModelConfig{
				APIKey: "test-key", BaseURL: server.URL, DefaultOptions: embedding.Options{Model: mistral.ModelEmbed},
			})
			if err != nil {
				t.Fatal(err)
			}
			request, _ := embedding.NewRequest([]string{"text"})
			if response, err := model.Call(t.Context(), request); response != nil || !errors.Is(err, embedding.ErrInvalidResponse) {
				t.Fatalf("response = %#v, error = %v", response, err)
			}
		})
	}
}
