package mongodb

import (
	"context"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/embeddingclient"
	"github.com/Tangerg/scope/core/vectorstore"
)

// countingCollection answers a bulk write with a scripted result. The embedded
// interface stays nil so a call to any other operation fails the test.
type countingCollection struct {
	DocumentCollection
	result *mongo.BulkWriteResult
	sent   int
}

func (c *countingCollection) BulkWrite(
	_ context.Context,
	models []mongo.WriteModel,
	_ ...options.Lister[options.BulkWriteOptions],
) (*mongo.BulkWriteResult, error) {
	c.sent = len(models)
	return c.result, nil
}

// constantModel is a stand-in embedding model for configuration tests that
// never reach an embedding call.
type constantModel struct{}

func (constantModel) Call(_ context.Context, request *embedding.Request) (*embedding.Response, error) {
	outputs := make([]*embedding.Output, len(request.Texts))
	for index := range outputs {
		outputs[index] = &embedding.Output{Embedding: []float64{1, 0}}
	}
	return embedding.NewResponse(outputs, nil)
}

type upsertBatcher struct{}

func (upsertBatcher) Batch(
	_ context.Context,
	documents []*document.Document,
) ([][]*document.Document, error) {
	return [][]*document.Document{documents}, nil
}

func upsertStore(t *testing.T, collection DocumentCollection) *Store {
	t.Helper()
	client, err := embeddingclient.New(embedding.ModelFunc(
		func(_ context.Context, request *embedding.Request) (*embedding.Response, error) {
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
		collection:      collection,
		embeddingPath:   DefaultEmbeddingPath,
		contentField:    DefaultContentField,
		metadataField:   DefaultMetadataField,
		embeddingClient: client,
		documentBatcher: upsertBatcher{},
	}
}

// MatchedCount covers the replacements that found an existing document and
// UpsertedCount those that inserted one, so their sum is MongoDB's own count of
// applied writes and anything short of the batch is a partial write.
func TestIndexRequiresEveryReplacementToApply(t *testing.T) {
	t.Parallel()

	request := &vectorstore.IndexRequest{Documents: []*document.Document{
		{ID: "one", Text: "first"},
		{ID: "two", Text: "second"},
	}}
	for _, sample := range []struct {
		name   string
		result *mongo.BulkWriteResult
		want   string
	}{
		{
			name:   "every document replaced",
			result: &mongo.BulkWriteResult{Acknowledged: true, MatchedCount: 2},
		},
		{
			name:   "one replaced and one inserted",
			result: &mongo.BulkWriteResult{Acknowledged: true, MatchedCount: 1, UpsertedCount: 1},
		},
		{
			name:   "one document unaccounted for",
			result: &mongo.BulkWriteResult{Acknowledged: true, MatchedCount: 1},
			want:   "applied 1 of 2 documents",
		},
		{
			name:   "nothing applied",
			result: &mongo.BulkWriteResult{Acknowledged: true},
			want:   "applied 0 of 2 documents",
		},
		{
			name:   "unacknowledged write concern",
			result: &mongo.BulkWriteResult{},
			want:   "unacknowledged (w: 0)",
		},
		{
			name:   "no result at all",
			result: nil,
			want:   "returned no result",
		},
	} {
		t.Run(sample.name, func(t *testing.T) {
			collection := &countingCollection{result: sample.result}
			err := upsertStore(t, collection).Index(t.Context(), request)
			if collection.sent != 2 {
				t.Fatalf("write models sent = %d, want 2", collection.sent)
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

var _ DocumentCollection = (*mongo.Collection)(nil)
