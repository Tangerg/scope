package openai_test

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	openaisdk "github.com/openai/openai-go/v3"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/models/protocol/openai"
)

func newEmbeddingModel(t *testing.T, baseURL, modelID string) *openai.EmbeddingModel {
	t.Helper()
	opts := embedding.Options{Model: modelID}
	err := opts.Validate()
	if err != nil {
		t.Fatalf("NewOptions: %v", err)
	}
	m, err := openai.NewEmbeddingModel(t.Context(), openai.EmbeddingModelConfig{
		Provider:       "openai",
		APIKey:         "test-key",
		DefaultOptions: opts,
		BaseURL:        baseURL,
	})
	if err != nil {
		t.Fatalf("NewEmbeddingModel: %v", err)
	}
	return m
}

func TestEmbeddingModel_Call_Mock(t *testing.T) {
	resp := openaisdk.CreateEmbeddingResponse{
		Object: "list",
		Model:  "text-embedding-3-small",
		Data: []openaisdk.Embedding{
			{Object: "embedding", Index: 0, Embedding: []float64{0.1, 0.2, 0.3}},
			{Object: "embedding", Index: 1, Embedding: []float64{0.4, 0.5, 0.6}},
		},
		Usage: openaisdk.CreateEmbeddingResponseUsage{
			PromptTokens: 8,
			TotalTokens:  8,
		},
	}
	body, _ := jsonv2.Marshal(resp)

	var seenURL string
	srv := modeltest.JSONServer(http.StatusOK, string(body), func(r *http.Request) {
		seenURL = r.URL.Path
	})
	t.Cleanup(srv.Close)

	m := newEmbeddingModel(t, srv.URL, "text-embedding-3-small")
	req, err := embedding.NewRequest([]string{"hello", "world"})
	if err != nil {
		t.Fatal(err)
	}

	out, err := m.Call(t.Context(), req)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !strings.HasSuffix(seenURL, "/embeddings") {
		t.Errorf("URL = %q; want suffix /embeddings", seenURL)
	}
	if len(out.Outputs) != 2 {
		t.Fatalf("got %d outputs; want 2", len(out.Outputs))
	}
	if out.Metadata.Usage == nil || out.Metadata.Usage.InputTokens != 8 {
		t.Errorf("usage = %+v; want InputTokens=8", out.Metadata.Usage)
	}
}

func TestEmbeddingDimensionsValidateEffectiveRequest(t *testing.T) {
	for _, defaults := range []bool{false, true} {
		t.Run(fmt.Sprintf("defaults=%v", defaults), func(t *testing.T) {
			dimension := int64(2)
			server := modeltest.JSONServer(http.StatusOK, `{"model":"test-model","data":[{"index":0,"embedding":[1,2,3]}]}`, func(request *http.Request) {
				var body struct {
					Dimensions int64 `json:"dimensions"`
				}
				if err := jsonv2.UnmarshalRead(request.Body, &body); err != nil {
					t.Error(err)
				}
				if body.Dimensions != 2 {
					t.Errorf("dimensions = %d", body.Dimensions)
				}
			})
			defer server.Close()
			config := openai.EmbeddingModelConfig{Provider: "test", APIKey: "test", BaseURL: server.URL, DefaultOptions: embedding.Options{Model: "test-model"}}
			request, err := embedding.NewRequest([]string{"text"})
			if err != nil {
				t.Fatal(err)
			}
			if defaults {
				config.DefaultOptions.Dimensions = &dimension
			} else {
				request.Options.Dimensions = &dimension
			}
			model, err := openai.NewEmbeddingModel(t.Context(), config)
			if err != nil {
				t.Fatal(err)
			}
			_, err = model.Call(t.Context(), request)
			if !errors.Is(err, embedding.ErrInvalidResponse) {
				t.Fatalf("Call = %v", err)
			}
			if defaults && request.Options.Dimensions != nil {
				t.Fatal("Call mutated request options")
			}
		})
	}
}

func TestEmbeddingUsagePresence(t *testing.T) {
	for _, test := range []struct {
		usage  string
		known  bool
		tokens int64
	}{
		{"", false, 0}, {`,"usage":null`, false, 0}, {`,"usage":{}`, false, 0}, {`,"usage":{"total_tokens":4}`, false, 0},
		{`,"usage":{"prompt_tokens":0,"total_tokens":0}`, true, 0}, {`,"usage":{"prompt_tokens":4,"total_tokens":4}`, true, 4},
	} {
		t.Run(test.usage, func(t *testing.T) {
			server := modeltest.JSONServer(http.StatusOK, `{"model":"test-model","data":[{"index":0,"embedding":[1,2]}]`+test.usage+`}`)
			defer server.Close()
			model := newEmbeddingModel(t, server.URL, "test-model")
			request, err := embedding.NewRequest([]string{"text"})
			if err != nil {
				t.Fatal(err)
			}
			response, err := model.Call(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			usage := response.Metadata.Usage
			if (usage != nil) != test.known || usage != nil && usage.InputTokens != test.tokens {
				t.Fatalf("usage = %#v", usage)
			}
		})
	}
}
