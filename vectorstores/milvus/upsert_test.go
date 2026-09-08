package milvus

import (
	"context"
	"strings"
	"testing"

	"github.com/milvus-io/milvus/client/v2/milvusclient"
	"google.golang.org/grpc"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/embeddingclient"
	"github.com/Tangerg/scope/core/vectorstore"
)

// countingCollection answers upserts with a scripted count. The embedded
// interface stays nil so a call to any other operation fails the test.
type countingCollection struct {
	collectionClient
	upserted int64
	calls    int
}

func (c *countingCollection) Upsert(
	ctx context.Context,
	option milvusclient.UpsertOption,
	_ ...grpc.CallOption,
) (milvusclient.UpsertResult, error) {
	c.calls++
	return milvusclient.UpsertResult{UpsertCount: c.upserted}, nil
}

type upsertBatcher struct{}

func (upsertBatcher) Batch(
	ctx context.Context,
	documents []*document.Document,
) ([][]*document.Document, error) {
	return [][]*document.Document{documents}, nil
}

func upsertStore(t *testing.T, client collectionClient) *Store {
	t.Helper()
	embeddings, err := embeddingclient.New(embedding.ModelFunc(
		func(ctx context.Context, request *embedding.Request) (*embedding.Response, error) {
			outputs := make([]*embedding.Output, len(request.Texts))
			for index := range outputs {
				outputs[index] = &embedding.Output{Embedding: []float64{1, 0}}
			}
			return embedding.NewResponse(outputs, nil)
		}))
	if err != nil {
		t.Fatal(err)
	}
	return &Store{
		client:          client,
		collectionName:  "documents",
		embeddingClient: embeddings,
		documentBatcher: upsertBatcher{},
		dimensions:      2,
	}
}

// UpsertCount is Milvus' acknowledgment of the batch, so a short count is a
// partial write and must not be reported as a complete one.
func TestIndexRequiresFullUpsertAcknowledgment(t *testing.T) {
	t.Parallel()

	request := &vectorstore.IndexRequest{Documents: []*document.Document{
		{ID: "one", Text: "first"},
		{ID: "two", Text: "second"},
	}}
	for _, sample := range []struct {
		name     string
		upserted int64
		want     string
	}{
		{name: "every document acknowledged", upserted: 2},
		{name: "short count", upserted: 1, want: "acknowledged 1 of 2 documents"},
		{name: "nothing acknowledged", upserted: 0, want: "acknowledged 0 of 2 documents"},
		{name: "impossible surplus", upserted: 3, want: "acknowledged 3 of 2 documents"},
	} {
		t.Run(sample.name, func(t *testing.T) {
			client := &countingCollection{upserted: sample.upserted}
			err := upsertStore(t, client).Index(t.Context(), request)
			if client.calls != 1 {
				t.Fatalf("Upsert calls = %d, want 1", client.calls)
			}
			if sample.want == "" {
				if err != nil {
					t.Fatalf("Index() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), sample.want) {
				t.Fatalf("Index() = %v, want an error containing %q", err, sample.want)
			}
		})
	}
}
