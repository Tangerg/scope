package embeddingclient_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/embeddingclient"
)

func TestClientRejectsUnencodableInputBeforeCallingModel(t *testing.T) {
	calls := 0
	var received []string
	client, err := embeddingclient.New(embeddingclient.Config{Model: embedding.ModelFunc(func(_ context.Context, request *embedding.Request) (*embedding.Response, error) {
		calls++
		received = request.Texts
		return responseFor(request.Texts), nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	if vectors, callErr := client.EmbedTexts(t.Context(), []string{"valid", "\xff"}); !errors.Is(callErr, embedding.ErrInvalidRequest) || vectors != nil || calls != 0 {
		t.Fatalf("invalid batch = %v, %v; model calls = %d", vectors, callErr, calls)
	}
	texts := []string{"值\x00", "text"}
	if vectors, callErr := client.EmbedTexts(t.Context(), texts); callErr != nil || len(vectors) != 2 || calls != 1 || !reflect.DeepEqual(received, texts) {
		t.Fatalf("valid batch = %v, %v; model calls = %d, texts = %q", vectors, callErr, calls, received)
	}
}

func TestClientRejectsUnencodableResponseMetadata(t *testing.T) {
	for _, responseMetadata := range []*embedding.ResponseMetadata{
		{Model: "\xff"},
		{CreatedAt: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)},
	} {
		client, err := embeddingclient.New(embeddingclient.Config{Model: embedding.ModelFunc(func(_ context.Context, request *embedding.Request) (*embedding.Response, error) {
			response := responseFor(request.Texts)
			response.Metadata = responseMetadata
			return response, nil
		})})
		if err != nil {
			t.Fatal(err)
		}
		if vectors, callErr := client.EmbedTexts(t.Context(), []string{"text"}); !errors.Is(callErr, embedding.ErrInvalidResponse) || vectors != nil {
			t.Fatalf("invalid response = %v, %v", vectors, callErr)
		}
	}
}

func TestClientChecksResponseAgainstOriginalBatch(t *testing.T) {
	client, err := embeddingclient.New(embeddingclient.Config{Model: embedding.ModelFunc(func(_ context.Context, request *embedding.Request) (*embedding.Response, error) {
		request.Texts = append(request.Texts, "foreign")
		return responseFor(request.Texts), nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := client.EmbedTexts(t.Context(), []string{"source"}); !errors.Is(err, embedding.ErrInvalidResponse) || got != nil {
		t.Fatalf("mutated batch accepted: %v, %v", got, err)
	}
}

func TestClientKeepsConfiguredDimensionsAcrossCalls(t *testing.T) {
	dimensions := int64(4)
	model := embedding.ModelFunc(func(_ context.Context, request *embedding.Request) (*embedding.Response, error) {
		if *request.Options.Dimensions != 4 {
			t.Fatalf("submitted dimensions = %d, want 4", *request.Options.Dimensions)
		}
		*request.Options.Dimensions = 1
		return &embedding.Response{Outputs: []*embedding.Output{{Embedding: []float64{7}}}}, nil
	})
	client, err := embeddingclient.New(embeddingclient.Config{Model: model, Options: embedding.Options{Dimensions: &dimensions}})
	if err != nil {
		t.Fatal(err)
	}
	dimensions = 0
	if _, err := embeddingclient.New(embeddingclient.Config{Model: model, Options: embedding.Options{Dimensions: &dimensions}}); !errors.Is(err, embedding.ErrInvalidOptions) {
		t.Fatalf("invalid dimensions = %v", err)
	}
	for range 2 {
		if got, err := client.EmbedText(t.Context(), "one"); got != nil || !errors.Is(err, embedding.ErrInvalidResponse) {
			t.Fatalf("mutated dimensions accepted: %v, %v", got, err)
		}
	}
}

func TestClientChecksCancellationAroundModelCall(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	calls := 0
	client, err := embeddingclient.New(embeddingclient.Config{Model: embedding.ModelFunc(func(_ context.Context, req *embedding.Request) (*embedding.Response, error) {
		calls++
		cancel()
		return responseFor(req.Texts), nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if got, err := client.EmbedText(ctx, "one"); got != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled batch: %v, %v", got, err)
		}
	}
	if calls != 1 {
		t.Fatalf("model calls = %d, want 1", calls)
	}
}
