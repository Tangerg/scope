package bedrock_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/httpclient"
	"github.com/Tangerg/scope/models/bedrock"
)

func TestCustomHTTPClientKeepsSignedCredentialsAtConfiguredOrigin(t *testing.T) {
	var destinationCalls, sourceCalls atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		destinationCalls.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"embedding":[1,2],"inputTextTokenCount":1}`))
	}))
	t.Cleanup(destination.Close)
	source := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		sourceCalls.Add(1)
		if request.Header.Get("Authorization") == "" || request.Header.Get("X-Amz-Security-Token") != "test-session-token" {
			t.Error("configured origin did not receive its signed credentials")
		}
		writer.Header().Set("Location", destination.URL+request.URL.Path)
		writer.WriteHeader(http.StatusFound)
	}))
	t.Cleanup(source.Close)
	client := source.Client()
	transport := client.Transport
	model, err := bedrock.NewEmbeddingModel(t.Context(), bedrock.EmbeddingModelConfig{
		Region: "us-east-1", BaseURL: source.URL, HTTPClient: client,
		Credentials: &bedrock.Credentials{
			AccessKeyID: "TEST_ACCESS_KEY", SecretAccessKey: "test-secret", SessionToken: "test-session-token",
		},
		DefaultOptions: embedding.Options{Model: "amazon.titan-embed-text-v2:0"},
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := model.Call(t.Context(), &embedding.Request{Texts: []string{"private request text"}})
	if response != nil || !errors.Is(err, httpclient.ErrOriginNotAllowed) || destinationCalls.Load() != 0 || sourceCalls.Load() == 0 {
		t.Fatalf("response=%v error=%v source calls=%d destination calls=%d", response, err, sourceCalls.Load(), destinationCalls.Load())
	}
	if client.Transport != transport || client.CheckRedirect != nil {
		t.Fatal("caller-owned HTTP client was changed")
	}
}
