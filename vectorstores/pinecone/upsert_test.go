package pinecone

import (
	"context"
	"strings"
	"testing"

	"github.com/pinecone-io/go-pinecone/v4/pinecone"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/embeddingclient"
	"github.com/Tangerg/scope/core/vectorstore"
)

// countingIndex answers upserts with a scripted count. The embedded interface
// stays nil so a call to any other operation fails the test.
type countingIndex struct {
	indexConnection
	upserted uint32
	sent     int
}

func (c *countingIndex) UpsertVectors(ctx context.Context, in []*pinecone.Vector) (uint32, error) {
	c.sent = len(in)
	return c.upserted, nil
}

type upsertBatcher struct{}

func (upsertBatcher) Batch(
	ctx context.Context,
	documents []*document.Document,
) ([][]*document.Document, error) {
	return [][]*document.Document{documents}, nil
}

func upsertStore(t *testing.T, index indexConnection) *Store {
	t.Helper()
	client, err := embeddingclient.New(embedding.ModelFunc(
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
		index:           index,
		embeddingClient: client,
		documentBatcher: upsertBatcher{},
		distanceMetric:  DistanceCosine,
	}
}

// UpsertedCount is Pinecone's acknowledgment of the batch, so a short count is
// a partial write and must not be reported as a complete one.
func TestIndexRequiresFullUpsertAcknowledgment(t *testing.T) {
	t.Parallel()

	request := &vectorstore.IndexRequest{Documents: []*document.Document{
		{ID: "one", Text: "first"},
		{ID: "two", Text: "second"},
	}}
	for _, sample := range []struct {
		name     string
		upserted uint32
		want     string
	}{
		{name: "every vector acknowledged", upserted: 2},
		{name: "short count", upserted: 1, want: "upsert acknowledged 1 of 2 vectors"},
		{name: "nothing acknowledged", upserted: 0, want: "upsert acknowledged 0 of 2 vectors"},
		{name: "impossible surplus", upserted: 3, want: "upsert acknowledged 3 of 2 vectors"},
	} {
		t.Run(sample.name, func(t *testing.T) {
			index := &countingIndex{upserted: sample.upserted}
			err := upsertStore(t, index).Index(t.Context(), request)
			if index.sent != 2 {
				t.Fatalf("vectors sent = %d, want 2", index.sent)
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
