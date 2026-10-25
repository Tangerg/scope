package protocol_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"google.golang.org/genai"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/models/google/internal/protocol"
)

func TestEmbeddingRejectsNullNativeCoordinates(t *testing.T) {
	for _, vertex := range []bool{false, true} {
		for _, value := range []string{"null", "0"} {
			t.Run(value, func(t *testing.T) {
				calls := 0
				body := `{"embeddings":[{"values":[` + value + `,1]}]}`
				if vertex {
					body = `{"predictions":[{"embeddings":{"values":[` + value + `,1]}}]}`
				}
				server := modeltest.JSONServer(http.StatusOK, body, func(*http.Request) { calls++ })
				defer server.Close()
				config := protocol.ClientConfig{APIKey: "test", BaseURL: server.URL, HTTPClient: server.Client()}
				modelName := protocol.ModelGeminiEmbedding2
				if vertex {
					config.Backend = genai.BackendVertexAI
					config.Project = "test"
					config.Location = "us-central1"
					modelName = "text-embedding-005"
				}
				model, err := protocol.NewEmbeddingModel(t.Context(), protocol.EmbeddingModelConfig{Provider: "google", Client: config, DefaultOptions: embedding.Options{Model: modelName}})
				if err != nil {
					t.Fatal(err)
				}
				request, err := embedding.NewRequest([]string{"text"})
				if err != nil {
					t.Fatal(err)
				}
				response, err := model.Call(t.Context(), request)
				if calls != 1 {
					t.Fatalf("native response validation caused %d requests", calls)
				}
				if value == "null" {
					if !errors.Is(err, embedding.ErrInvalidResponse) {
						t.Fatalf("response=%v error=%v", response, err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if response.Outputs[0].Embedding[0] != 0 {
					t.Fatal(response.Outputs[0].Embedding)
				}
			})
		}
	}
}

func TestEmbeddingMetadataUsesNativePresence(t *testing.T) {
	for _, endpoint := range []string{"gemini batch", "vertex predict", "vertex embed"} {
		for _, reported := range []bool{false, true} {
			t.Run(endpoint+"/"+map[bool]string{false: "absent", true: "zero"}[reported], func(t *testing.T) {
				statistics, metadata := `{}`, `{}`
				if reported {
					statistics = `{"tokenCount":0,"truncated":false}`
					metadata = `{"billableCharacterCount":0}`
				}
				body := `{"embeddings":[{"values":[0,1],"statistics":` + statistics + `}],"metadata":` + metadata + `,"future":9007199254740993}`
				if endpoint == "vertex predict" {
					body = strings.ReplaceAll(`{"predictions":[{"embeddings":{"values":[0,1],"statistics":`+statistics+`}}],"metadata":`+metadata+`,"future":9007199254740993}`, "tokenCount", "token_count")
				}
				if endpoint == "vertex embed" {
					body = `{"embedding":{"values":[0,1]},"usageMetadata":{},"metadata":` + metadata + `,"future":9007199254740993}`
					if reported {
						body = strings.ReplaceAll(body, `"usageMetadata":{}`, `"usageMetadata":{"promptTokenCount":0},"truncated":false`)
					}
				}
				server := modeltest.JSONServer(http.StatusOK, body)
				defer server.Close()
				config := protocol.ClientConfig{APIKey: "test", BaseURL: server.URL, HTTPClient: server.Client()}
				modelName := protocol.ModelGeminiEmbedding2
				if endpoint != "gemini batch" {
					config.Backend, config.Project, config.Location = genai.BackendVertexAI, "test", "us-central1"
					if endpoint == "vertex predict" {
						modelName = "text-embedding-005"
					}
				}
				model, err := protocol.NewEmbeddingModel(t.Context(), protocol.EmbeddingModelConfig{Provider: "google", Client: config, DefaultOptions: embedding.Options{Model: modelName}})
				if err != nil {
					t.Fatal(err)
				}
				request, err := embedding.NewRequest([]string{"text"})
				if err != nil {
					t.Fatal(err)
				}
				response, err := model.Call(t.Context(), request)
				if err != nil {
					t.Fatal(err)
				}
				for name, fields := range map[string]map[string]json.RawMessage{"output": response.Outputs[0].Metadata, "response": response.Metadata.Extra} {
					keys := []string{"google/token_count", "google/truncated"}
					if name == "response" {
						keys = []string{"google/billable_character_count"}
					}
					for _, key := range keys {
						raw, exists := fields[key]
						if exists != reported || exists && string(raw) != "0" && string(raw) != "false" {
							t.Fatalf("%s=%s exists=%t reported=%t", key, raw, exists, reported)
						}
					}
				}
				if !strings.Contains(string(response.Metadata.Extra["google/embedding_response"]), `9007199254740993`) {
					t.Fatal("native response precision was lost")
				}
			})
		}
	}
}
