package mongodb

import (
	"context"
	"errors"
	"math"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/embeddingclient"
	"github.com/Tangerg/scope/core/vectorstore"
)

type singletonBatcher struct{}

func (s singletonBatcher) Batch(_ context.Context, docs []*document.Document) ([][]*document.Document, error) {
	var batches [][]*document.Document
	for _, doc := range docs {
		batches = append(batches, []*document.Document{doc})
	}
	return batches, nil
}

func TestIndexPreparesAllBatchesBeforeNativePublication(t *testing.T) {
	for _, failure := range []string{"model", "width", "FLOAT32 overflow"} {
		t.Run(failure, func(t *testing.T) {
			collection := &filterCollection{}
			store := upsertStore(t, collection)
			calls := 0
			model, err := embeddingclient.New(embedding.ModelFunc(func(context.Context, *embedding.Request) (*embedding.Response, error) {
				calls++
				vector := []float64{1, 0}
				if calls == 2 {
					switch failure {
					case "model":
						return nil, errors.New("late model failure")
					case "width":
						vector = []float64{1}
					case "FLOAT32 overflow":
						vector = []float64{math.MaxFloat64, 0}
					}
				}
				return embedding.NewResponse([]*embedding.Output{{Embedding: vector}}, nil)
			}))
			if err != nil {
				t.Fatal(err)
			}
			store.embeddingClient, store.documentBatcher = model, singletonBatcher{}
			err = store.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{{ID: "one", Text: "first"}, {ID: "two", Text: "second"}}})
			if err == nil || collection.writes != 0 || calls != 2 {
				t.Fatalf("error=%v writes=%d model calls=%d", err, collection.writes, calls)
			}
		})
	}
}

func TestNativeScoresMustKeepTheirBoundedScaleEvenBelowMinScore(t *testing.T) {
	for _, score := range []float64{-1, 1.01, math.NaN(), math.Inf(1)} {
		store, collection := mongoFilterStore(t, []*document.Document{{ID: "one", Text: "text"}})
		collection.scores["one"] = score
		response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "q", Options: vectorstore.SearchOptions{MinScore: 1}})
		if err == nil || response != nil {
			t.Fatalf("native score %v became success: %v %v", score, response, err)
		}
	}
}

func TestNativeEnumerationAndQueryRejectRepeatedIdentity(t *testing.T) {
	store, collection := mongoFilterStore(t, []*document.Document{{ID: "one", Text: "text"}})
	collection.records = append(collection.records, collection.records[0])
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "q"})
	if err == nil || response != nil {
		t.Fatal("repeated source became success")
	}
	collection.records = collection.records[:1]
	collection.beforeQuery = func() { collection.records = append(collection.records, collection.records[0]) }
	response, err = store.Search(t.Context(), &vectorstore.SearchRequest{Query: "q", Options: vectorstore.SearchOptions{TopK: 2}})
	if err == nil || response != nil {
		t.Fatal("repeated native hit became success")
	}
}

func TestDeleteIDsValidatesWholeBatchAndUsesNativeIdentity(t *testing.T) {
	store, collection := mongoFilterStore(t, []*document.Document{{ID: "A", Text: "text"}})
	if err := store.DeleteIDs(t.Context(), []string{"A", " "}); err == nil || len(collection.deleted) != 0 {
		t.Fatal("invalid late identity published deletion")
	}
	if err := store.DeleteIDs(t.Context(), []string{"a"}); err != nil || len(collection.deleted) != 0 {
		t.Fatal("case variant advanced another identity")
	}
	if err := store.DeleteIDs(t.Context(), []string{"A", "A"}); err != nil || len(collection.deleted) != 1 {
		t.Fatal("native identity was not deleted exactly once")
	}
}

func TestSourceRequiresNativeVectorAndNoScoreProjection(t *testing.T) {
	for _, value := range []any{bson.A{1.0, math.Inf(1)}, bson.A{1, 0}, "not a vector"} {
		store, collection := mongoFilterStore(t, []*document.Document{{ID: "one", Text: "text"}})
		raw, err := bson.Marshal(bson.M{idField: "one", contentField: "text", embeddingField: value, metadataField: "null"})
		if err != nil {
			t.Fatal(err)
		}
		collection.records[0] = raw
		response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "q"})
		if err == nil || response != nil {
			t.Fatal("invalid native vector became success")
		}
	}
}
