package openai_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	openaisdk "github.com/openai/openai-go/v3"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/httpclient"
	"github.com/Tangerg/scope/models/openai"
)

const overloadedMessage = "fixture overloaded"

type generationBinding struct {
	path      string
	construct func(t *testing.T, baseURL string, maxRetries *int) corechat.Model
}

var generationBindings = map[string]generationBinding{
	"responses": {path: "/v1/responses", construct: func(t *testing.T, baseURL string, maxRetries *int) corechat.Model {
		model, err := openai.NewResponses(t.Context(), openai.ChatModelConfig{
			APIKey:         "fixture-key",
			BaseURL:        baseURL,
			DefaultOptions: corechat.Options{Model: "fixture-model"},
			MaxRetries:     maxRetries,
		})
		if err != nil {
			t.Fatal(err)
		}
		return model
	}},
	"chat completions": {path: "/v1/chat/completions", construct: func(t *testing.T, baseURL string, maxRetries *int) corechat.Model {
		model, err := openai.NewChatCompletions(t.Context(), openai.ChatModelConfig{
			APIKey:         "fixture-key",
			BaseURL:        baseURL,
			DefaultOptions: corechat.Options{Model: "fixture-model"},
			MaxRetries:     maxRetries,
		})
		if err != nil {
			t.Fatal(err)
		}
		return model
	}},
}

func callOverloaded(t *testing.T, binding generationBinding, maxRetries *int) (int32, error) {
	t.Helper()
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != binding.path {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		posts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After-Ms", "1")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"` + overloadedMessage + `","type":"server_error"}}`))
	}))
	defer server.Close()
	model := binding.construct(t, server.URL+"/v1", maxRetries)
	_, err := model.Call(t.Context(), &corechat.Request{Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("hello"))}})
	return posts.Load(), err
}

func TestZeroMaxRetriesSendsOneGenerationRequest(t *testing.T) {
	for name, binding := range generationBindings {
		t.Run(name, func(t *testing.T) {
			posts, err := callOverloaded(t, binding, new(0))
			if posts != 1 {
				t.Fatalf("posts = %d, want 1", posts)
			}
			status, ok := errors.AsType[*httpclient.ResponseError](err)
			if !ok || status.HTTPStatus() != http.StatusServiceUnavailable {
				t.Fatalf("error lost the HTTP status: %v", err)
			}
			apiErr, ok := errors.AsType[*openaisdk.Error](err)
			if !ok || apiErr.Message != overloadedMessage {
				t.Fatalf("error lost the provider message: %v", err)
			}
		})
	}
}

func TestOmittedMaxRetriesKeepsTheSDKRetryPolicy(t *testing.T) {
	// The SDK retries a 503 twice by default.
	for name, binding := range generationBindings {
		t.Run(name, func(t *testing.T) {
			if posts, err := callOverloaded(t, binding, nil); posts != 3 || err == nil {
				t.Fatalf("posts = %d error = %v, want 3 posts and an error", posts, err)
			}
		})
	}
}

func TestNegativeMaxRetriesIsRejected(t *testing.T) {
	if err := (openai.ChatModelConfig{APIKey: "fixture-key", MaxRetries: new(-1)}).Validate(); err == nil {
		t.Fatal("Validate accepted a negative retry count")
	}
}
