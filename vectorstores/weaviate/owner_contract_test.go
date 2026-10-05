package weaviate

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"testing"

	"github.com/weaviate/weaviate/entities/models"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/embeddingclient"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

type singletonBatcher struct{}

func (s singletonBatcher) Batch(_ context.Context, docs []*document.Document) ([][]*document.Document, error) {
	var batches [][]*document.Document
	for _, doc := range docs {
		batches = append(batches, []*document.Document{doc})
	}
	return batches, nil
}

func TestCoreMetadataSurvivesNativeStringsExactly(t *testing.T) {
	for _, facts := range []metadata.Map{nil, {}, {"huge": json.RawMessage(`1e1000`), "long": json.RawMessage(`1.00000000000000001`), "$native.key": json.RawMessage(`{"nested":[null,{},9007199254740993]}`), "content": json.RawMessage(`"business value"`)}} {
		doc := &document.Document{ID: "f9168c5e-ceb2-4faa-b6bf-329bf39fa1e4", Text: "stored text🙂", Metadata: facts}
		store, _ := indexedNativeStore(t, []*document.Document{doc})
		response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "q"})
		if err != nil || len(response.Results) != 1 {
			t.Fatalf("response=%v error=%v", response, err)
		}
		got := response.Results[0].Document
		if got.ID != doc.ID || got.Text != doc.Text || !got.Metadata.Equal(facts) || (got.Metadata == nil) != (facts == nil) {
			t.Fatalf("native metadata changed: %v", got)
		}
	}
}

func TestIndexPreparesAllModelBatchesBeforeNativeWrite(t *testing.T) {
	for _, failure := range []string{"model", "width", "FLOAT32 overflow"} {
		t.Run(failure, func(t *testing.T) {
			fixture := &nativeFixture{}
			store, _ := newNativeStore(t, fixture)
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
			err = store.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{{ID: "00000000-0000-0000-0000-000000000001", Text: "first"}, {ID: "00000000-0000-0000-0000-000000000002", Text: "second"}}})
			if err == nil || fixture.batches.Load() != 0 || calls != 2 {
				t.Fatalf("error=%v writes=%d calls=%d", err, fixture.batches.Load(), calls)
			}
		})
	}
}

func TestCompleteSourceValidationCannotHideCorruptionBehindPredicate(t *testing.T) {
	for _, corrupt := range []func(*models.Object){
		func(object *models.Object) { object.ID = "F9168C5E-CEB2-4FAA-B6BF-329BF39FA1E4" },
		func(object *models.Object) { object.Properties.(map[string]any)[fieldMetadata] = `{"key":1,"key":2}` },
		func(object *models.Object) { object.Properties.(map[string]any)["legacy"] = true },
		func(object *models.Object) { object.Vector = nil },
	} {
		store, fixture := indexedNativeStore(t, []*document.Document{{ID: "f9168c5e-ceb2-4faa-b6bf-329bf39fa1e4", Text: "text"}})
		corrupt(fixture.objects["f9168c5e-ceb2-4faa-b6bf-329bf39fa1e4"])
		response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "q", Options: vectorstore.SearchOptions{Filter: filter.EQ("value", "outside"), MinScore: 1}})
		if err == nil || response != nil {
			t.Fatalf("corrupt source became success: %v %v", response, err)
		}
		if err = store.DeleteWhere(t.Context(), filter.EQ("value", "outside")); err == nil || fixture.deletes.Load() != 0 {
			t.Fatal("corrupt source allowed deletion")
		}
	}
}

func TestNativeHybridScoresAndInvalidHitsNeverReturnPartialSuccess(t *testing.T) {
	for _, fault := range []string{"score range", "score number", "score NaN", "wrong vector", "duplicate", "surplus", "query failure"} {
		t.Run(fault, func(t *testing.T) {
			store, fixture := indexedNativeStore(t, []*document.Document{{ID: "f9168c5e-ceb2-4faa-b6bf-329bf39fa1e4", Text: "text"}})
			fixture.queryChange = func(items []map[string]any) []map[string]any {
				additional := items[0]["_additional"].(map[string]any)
				switch fault {
				case "score range":
					additional[additionalScore] = "2"
				case "score number":
					additional[additionalScore] = .5
				case "score NaN":
					additional[additionalScore] = "NaN"
				case "wrong vector":
					additional[additionalVector] = nil
				case "duplicate":
					items = append(items, items[0])
				case "surplus":
					items = append(items, items[0], items[0])
				}
				return items
			}
			fixture.queryError = fault == "query failure"
			response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "q", Options: vectorstore.SearchOptions{Mode: vectorstore.SearchModeHybrid, TopK: 2, MinScore: 1}})
			if err == nil || response != nil {
				t.Fatalf("invalid hit became success: %v %v", response, err)
			}
		})
	}
}
