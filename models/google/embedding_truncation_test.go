package google_test

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/invocation"
	"github.com/Tangerg/scope/models/google"
	"github.com/Tangerg/scope/models/google/vertexai"
)

func TestVertexEmbeddingPreservesTruncationPresence(t *testing.T) {
	for _, endpoint := range []struct {
		name, model, action, container, response string
	}{
		{"predict", "text-embedding-005", ":predict", "parameters", `{"predictions":[{"embeddings":{"values":[1,2]}}]}`},
		{"embed content", "gemini-embedding-2", ":embedContent", "embedContentConfig", `{"embedding":{"values":[1,2]}}`},
	} {
		for _, source := range []string{"request", "defaults"} {
			for _, raw := range []string{"", "false", "true"} {
				for _, dimensions := range []*int64{nil, new(int64(2))} {
					name := endpoint.name + "/" + source + "/" + raw
					if dimensions != nil {
						name += "/dimensions"
					}
					t.Run(name, func(t *testing.T) {
						server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
							if !strings.HasSuffix(request.URL.Path, endpoint.action) {
								t.Errorf("request path = %q, want action %s", request.URL.Path, endpoint.action)
							}
							var body map[string]json.RawMessage
							if err := jsonv2.UnmarshalRead(request.Body, &body); err != nil {
								t.Error(err)
								return
							}
							var config map[string]json.RawMessage
							if value, present := body[endpoint.container]; present {
								if err := jsonv2.Unmarshal(value, &config); err != nil {
									t.Error(err)
									return
								}
							}
							if got := string(config["autoTruncate"]); got != raw {
								t.Errorf("autoTruncate = %q, want %q", got, raw)
							}
							if dimensions != nil && string(config["outputDimensionality"]) != "2" {
								t.Errorf("outputDimensionality = %s, want 2", config["outputDimensionality"])
							}
							writer.Header().Set("Content-Type", "application/json")
							_, _ = writer.Write([]byte(endpoint.response))
						}))
						t.Cleanup(server.Close)
						defaults := embedding.Options{Model: endpoint.model, Dimensions: dimensions}
						request := &embedding.Request{Texts: []string{"canonical text"}}
						if raw != "" {
							extensions := &request.Options.Extensions
							if source == "defaults" {
								extensions = &defaults.Extensions
							}
							if err := extensions.Set(vertexai.EmbeddingRequestExtensionKey, json.RawMessage(`{"autoTruncate":`+raw+`}`)); err != nil {
								t.Fatal(err)
							}
						}
						model, err := vertexai.NewEmbeddingModel(t.Context(), vertexai.EmbeddingModelConfig{
							Client:         vertexai.ClientConfig{Project: "test-project", Location: "global", BaseURL: server.URL, HTTPClient: server.Client()},
							DefaultOptions: defaults,
						})
						if err != nil {
							t.Fatal(err)
						}
						if response, err := model.Call(t.Context(), request); err != nil || response == nil {
							t.Fatalf("response=%v error=%v", response, err)
						}
					})
				}
			}
		}
	}
}

func TestGeminiEmbeddingRejectsTruncationBeforeIO(t *testing.T) {
	for _, source := range []string{"request", "defaults"} {
		for _, raw := range []string{"false", "true"} {
			t.Run(source+"/"+raw, func(t *testing.T) {
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
					calls.Add(1)
					writer.Header().Set("Content-Type", "application/json")
					_, _ = writer.Write([]byte(`{"embeddings":[{"values":[1,2]}]}`))
				}))
				t.Cleanup(server.Close)
				defaults := embedding.Options{Model: "gemini-embedding-001"}
				request := &embedding.Request{Texts: []string{"canonical text"}}
				extensions := &request.Options.Extensions
				if source == "defaults" {
					extensions = &defaults.Extensions
				}
				if err := extensions.Set(google.EmbeddingRequestExtensionKey, json.RawMessage(`{"autoTruncate":`+raw+`}`)); err != nil {
					t.Fatal(err)
				}
				model, err := google.NewEmbeddingModel(t.Context(), google.EmbeddingModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: defaults})
				if err != nil {
					t.Fatal(err)
				}
				response, err := model.Call(t.Context(), request)
				failure, ok := errors.AsType[*invocation.Failure](err)
				if response != nil || !errors.Is(err, errors.ErrUnsupported) || !ok || failure.Kind != invocation.FailureNotExecuted || calls.Load() != 0 {
					t.Fatalf("response=%v error=%v calls=%d; want unsupported option before dispatch", response, err, calls.Load())
				}
			})
		}
	}
}

func TestVertexEmbeddingTruncationRemainsPerRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body struct {
			Instances []struct {
				Content string `json:"content"`
			} `json:"instances"`
			Parameters struct {
				AutoTruncate *bool `json:"autoTruncate"`
			} `json:"parameters"`
		}
		if err := jsonv2.UnmarshalRead(request.Body, &body); err != nil {
			t.Error(err)
			return
		}
		if len(body.Instances) != 1 || body.Parameters.AutoTruncate == nil {
			t.Errorf("missing input or autoTruncate in %+v", body)
			return
		}
		if got, want := *body.Parameters.AutoTruncate, body.Instances[0].Content == "true"; got != want {
			t.Errorf("autoTruncate=%v, want %v for this request", got, want)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"predictions":[{"embeddings":{"values":[1,2]}}]}`))
	}))
	t.Cleanup(server.Close)
	defaults := embedding.Options{Model: "text-embedding-005"}
	if err := defaults.Extensions.Set(vertexai.EmbeddingRequestExtensionKey, json.RawMessage(`{"autoTruncate":true}`)); err != nil {
		t.Fatal(err)
	}
	model, err := vertexai.NewEmbeddingModel(t.Context(), vertexai.EmbeddingModelConfig{
		Client:         vertexai.ClientConfig{Project: "test-project", Location: "global", BaseURL: server.URL, HTTPClient: server.Client()},
		DefaultOptions: defaults,
	})
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var group sync.WaitGroup
	for _, raw := range []string{"true", "false"} {
		group.Go(func() {
			<-start
			request := &embedding.Request{Texts: []string{raw}}
			if raw == "false" {
				if err := request.Options.Extensions.Set(vertexai.EmbeddingRequestExtensionKey, json.RawMessage(`{"autoTruncate":false}`)); err != nil {
					t.Error(err)
					return
				}
			}
			if response, err := model.Call(t.Context(), request); err != nil || response == nil {
				t.Errorf("response=%v error=%v", response, err)
			}
		})
	}
	close(start)
	group.Wait()
}
