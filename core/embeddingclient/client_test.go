package embeddingclient_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/embeddingclient"
)

type pointerModel struct{}

func (*pointerModel) Call(context.Context, *embedding.Request) (*embedding.Response, error) {
	return nil, nil
}

func responseFor(texts []string) *embedding.Response {
	outputs := make([]*embedding.Output, len(texts))
	for i := range texts {
		outputs[i], _ = embedding.NewOutput([]float64{1, 2, 3, 4}, nil)
	}
	response, _ := embedding.NewResponse(outputs, &embedding.ResponseMetadata{Model: "fake"})
	return response
}

func TestClientEmbedsIndependentVectors(t *testing.T) {
	var captured *embedding.Request
	var providerResponses []*embedding.Response
	model := embedding.ModelFunc(func(_ context.Context, request *embedding.Request) (*embedding.Response, error) {
		captured = request
		response := responseFor(request.Texts)
		providerResponses = append(providerResponses, response)
		return response, nil
	})
	client, err := embeddingclient.New(model)
	if err != nil {
		t.Fatal(err)
	}

	vectors, err := client.EmbedTexts(t.Context(), []string{"a", "b"})
	if err != nil || len(vectors) != 2 || len(captured.Texts) != 2 {
		t.Fatalf("EmbedTexts() = %#v, %v", vectors, err)
	}
	vectors[0][0] = 99
	if providerResponses[0].Outputs[0].Embedding[0] == 99 {
		t.Fatal("returned vectors alias the provider response")
	}
	if vector, err := client.EmbedText(t.Context(), "one"); err != nil || len(vector) != 4 {
		t.Fatalf("EmbedText() = %#v, %v", vector, err)
	}
}

func TestClientRejectsInvalidBoundaries(t *testing.T) {
	if _, err := embeddingclient.New(nil); !errors.Is(err, embeddingclient.ErrNilModel) {
		t.Fatalf("New(nil) error = %v, want ErrNilModel", err)
	}
	var typedNil *pointerModel
	if _, err := embeddingclient.New(typedNil); !errors.Is(err, embeddingclient.ErrNilModel) {
		t.Fatalf("New(typed nil) error = %v, want ErrNilModel", err)
	}

	var zeroClient embeddingclient.Client
	if _, err := zeroClient.EmbedTexts(t.Context(), []string{"text"}); !errors.Is(err, embeddingclient.ErrNilModel) {
		t.Fatalf("zero Client error = %v, want ErrNilModel", err)
	}

	nilResponse, _ := embeddingclient.New(embedding.ModelFunc(func(context.Context, *embedding.Request) (*embedding.Response, error) {
		return nil, nil
	}))
	if vectors, err := nilResponse.EmbedTexts(t.Context(), []string{"text"}); vectors != nil || !errors.Is(err, embedding.ErrInvalidResponse) {
		t.Fatalf("absent model response lost its classification: %v, %v", vectors, err)
	}

	want := errors.New("boom")
	failed, _ := embeddingclient.New(embedding.ModelFunc(func(context.Context, *embedding.Request) (*embedding.Response, error) {
		return nil, want
	}))
	if _, err := failed.EmbedTexts(t.Context(), []string{"text"}); !errors.Is(err, want) {
		t.Fatalf("EmbedTexts error = %v, want %v", err, want)
	}
}

func TestClientValidatesResults(t *testing.T) {
	tests := []struct {
		name     string
		response *embedding.Response
	}{
		{name: "wrong output count", response: &embedding.Response{}},
		{name: "nil output", response: &embedding.Response{Outputs: []*embedding.Output{nil}}},
		{name: "empty vector", response: &embedding.Response{Outputs: []*embedding.Output{{}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, _ := embeddingclient.New(embedding.ModelFunc(func(context.Context, *embedding.Request) (*embedding.Response, error) {
				return tt.response, nil
			}))
			if _, err := client.EmbedTexts(t.Context(), []string{"text"}); err == nil {
				t.Fatal("EmbedTexts accepted an invalid output")
			}
		})
	}

	client, _ := embeddingclient.New(embedding.ModelFunc(func(_ context.Context, request *embedding.Request) (*embedding.Response, error) {
		return responseFor(request.Texts), nil
	}))
	if _, err := client.EmbedTexts(t.Context(), []string{""}); err == nil {
		t.Fatal("EmbedTexts accepted empty text")
	}
}
