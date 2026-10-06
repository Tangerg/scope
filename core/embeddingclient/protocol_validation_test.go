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
	client, err := embeddingclient.New(embedding.ModelFunc(func(_ context.Context, request *embedding.Request) (*embedding.Response, error) {
		calls++
		received = request.Texts
		return responseFor(request.Texts), nil
	}))
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
		client, err := embeddingclient.New(embedding.ModelFunc(func(_ context.Context, request *embedding.Request) (*embedding.Response, error) {
			response := responseFor(request.Texts)
			response.Metadata = responseMetadata
			return response, nil
		}))
		if err != nil {
			t.Fatal(err)
		}
		if vectors, callErr := client.EmbedTexts(t.Context(), []string{"text"}); !errors.Is(callErr, embedding.ErrInvalidResponse) || vectors != nil {
			t.Fatalf("invalid response = %v, %v", vectors, callErr)
		}
	}
}
