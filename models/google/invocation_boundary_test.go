package google_test

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/invocation"
	"github.com/Tangerg/scope/models/google"
	"github.com/Tangerg/scope/models/google/vertexai"
)

type invocationFailureTransport func(*http.Request) (*http.Response, error)

func (i invocationFailureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	defer request.Body.Close()
	return i(request)
}

type invocationFailureBody struct{ cause error }

func (i invocationFailureBody) Read([]byte) (int, error) { return 0, i.cause }
func (i invocationFailureBody) Close() error             { return nil }

func TestNativeInvocationOwnsExternalFailureEvidence(t *testing.T) {
	for _, binding := range []string{"chat", "embedding", "vertex-embedding", "image", "vertex-image"} {
		for _, phase := range []string{"transport", "success-body", "error-body", "partial-stream"} {
			t.Run(binding+"/"+phase, func(t *testing.T) {
				cause := errors.New("nested operation refused")
				nested := &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: cause}
				calls := 0
				client := &http.Client{Transport: invocationFailureTransport(func(request *http.Request) (*http.Response, error) {
					calls++
					if phase == "transport" {
						return nil, nested
					}
					status := http.StatusOK
					if phase == "error-body" {
						status = http.StatusBadRequest
					}
					var body io.ReadCloser = invocationFailureBody{cause: nested}
					if phase == "partial-stream" && binding == "chat" {
						prefix := "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"partial\"}]}}]}\n\n"
						body = io.NopCloser(io.MultiReader(strings.NewReader(prefix), body))
					}
					return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: body, Request: request}, nil
				})}
				call := nativeFailureCall(t, binding, client)
				err := call()
				if calls != 1 || invocation.ClassifyFailure(err) != invocation.FailureUnknown || !errors.Is(err, cause) {
					t.Fatalf("calls=%d kind=%s error=%v", calls, invocation.ClassifyFailure(err), err)
				}
			})
		}
	}
}

func nativeFailureCall(t *testing.T, binding string, client *http.Client) func() error {
	t.Helper()
	vertexClient := vertexai.ClientConfig{Project: "test", Location: "global", BaseURL: "https://vertex.example.test", HTTPClient: client}
	switch binding {
	case "chat":
		model, err := google.NewChat(t.Context(), google.ChatConfig{APIKey: "test", HTTPClient: client, DefaultOptions: chat.Options{Model: google.ModelGemini36Flash}})
		if err != nil {
			t.Fatal(err)
		}
		return func() error {
			_, err := model.Call(t.Context(), &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("hello"))}})
			return err
		}
	case "embedding", "vertex-embedding":
		var model embedding.Model
		var err error
		options := embedding.Options{Model: google.ModelGeminiEmbedding2}
		if binding == "embedding" {
			model, err = google.NewEmbeddingModel(t.Context(), google.EmbeddingModelConfig{APIKey: "test", HTTPClient: client, DefaultOptions: options})
		} else {
			model, err = vertexai.NewEmbeddingModel(t.Context(), vertexai.EmbeddingModelConfig{Client: vertexClient, DefaultOptions: options})
		}
		if err != nil {
			t.Fatal(err)
		}
		return func() error {
			_, err := model.Call(t.Context(), &embedding.Request{Texts: []string{"hello"}})
			return err
		}
	case "image", "vertex-image":
		var model image.Model
		var err error
		options := image.Options{Model: google.ModelGemini3ProImage}
		if binding == "image" {
			model, err = google.NewImageModel(t.Context(), google.ImageModelConfig{APIKey: "test", HTTPClient: client, DefaultOptions: options})
		} else {
			model, err = vertexai.NewImageModel(t.Context(), vertexai.ImageModelConfig{Client: vertexClient, DefaultOptions: options})
		}
		if err != nil {
			t.Fatal(err)
		}
		return func() error {
			_, err := model.Call(t.Context(), &image.Request{Prompt: "hello"})
			return err
		}
	default:
		t.Fatalf("unknown binding %q", binding)
		return nil
	}
}

func TestVertexEmbeddingLocalSDKRejectionIsNotExecuted(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: invocationFailureTransport(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("unexpected HTTP")
	})}
	model, err := vertexai.NewEmbeddingModel(t.Context(), vertexai.EmbeddingModelConfig{
		Client:         vertexai.ClientConfig{Project: "test", Location: "global", BaseURL: "https://vertex.example.test", HTTPClient: client},
		DefaultOptions: embedding.Options{Model: google.ModelGeminiEmbedding2},
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := model.Call(t.Context(), &embedding.Request{Texts: []string{"first", "second"}})
	if response != nil || calls != 0 || invocation.ClassifyFailure(err) != invocation.FailureNotExecuted {
		t.Fatalf("response=%#v calls=%d error=%v", response, calls, err)
	}
}
