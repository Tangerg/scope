package qdrant

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"testing"

	qdrantclient "github.com/qdrant/go-client/qdrant"

	"github.com/Tangerg/scope/core/document"

	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

func TestMetadataHasOneLosslessCoreCodec(t *testing.T) {
	for _, facts := range []metadata.Map{nil, {}, {"huge": json.RawMessage(`1e1000`), "decimal": json.RawMessage(`1.00000000000000001`), "nested": json.RawMessage(`{"list":[null,true,{}]}`), "content": json.RawMessage(`"user value"`)}} {
		store, _ := qdrantFilterStore(t, []*document.Document{{ID: "1", Text: "text🙂", Metadata: facts}})
		response, err := store.Query(t.Context(), &vectorstore.Query{Space: fixtureSpace, Values: []float64{1, 0}})
		if err != nil || len(response.Results) != 1 {
			t.Fatalf("response=%v err=%v", response, err)
		}
		doc := response.Results[0].Document
		if doc.ID != "1" || doc.Text != "text🙂" || !doc.Metadata.Equal(facts) || (doc.Metadata == nil) != (facts == nil) {
			t.Fatalf("metadata changed: %#v", doc)
		}
	}
}

func TestEveryNativeVectorIsPreparedBeforePublication(t *testing.T) {
	for _, coords := range [][]float64{{1}, {math.MaxFloat64, 0}} {
		store, fixture := qdrantFilterStore(t, nil)
		batch := fixtureBatch([]*document.Document{{ID: "1", Text: "first"}, {ID: "2", Text: "last"}})
		batch.Records[1].Values = coords
		if err := store.Upsert(t.Context(), batch); err == nil || fixture.upserts != 0 {
			t.Fatalf("invalid vector published: %d, %v", fixture.upserts, err)
		}
	}
}

func TestNativeMetadataConditionRetainsConcurrentUpdate(t *testing.T) {
	facts, err := metadata.FromValues(map[string]any{"value": "a"})
	if err != nil {
		t.Fatal(err)
	}
	store, fixture := qdrantFilterStore(t, []*document.Document{{ID: "1", Text: "changed", Metadata: facts}, {ID: "2", Text: "retained selection", Metadata: facts}})
	fixture.beforeDelete = func() {
		changed, encodeErr := encodeRecord(&document.Document{ID: "1", Text: "changed", Metadata: metadata.Map{"value": []byte(`"b"`)}})
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		fixture.points[0].Payload = changed.Payload
	}
	if err = store.DeleteWhere(t.Context(), filter.EQ("value", "a")); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(fixture.deleted, []string{"2"}) || len(fixture.points) != 1 || fixture.points[0].Id.GetNum() != 1 {
		t.Fatalf("deleted a changed native fact: %v", fixture.deleted)
	}
}

func TestNativeRanksMergeBeforeCoreNormalization(t *testing.T) {
	for _, metric := range []qdrantclient.Distance{qdrantclient.Distance_Cosine, qdrantclient.Distance_Dot, qdrantclient.Distance_Euclid, qdrantclient.Distance_Manhattan} {
		t.Run(metric.String(), func(t *testing.T) {
			var docs []*document.Document
			for i := range 257 {
				facts, err := metadata.FromValues(map[string]any{"value": "a", "unique": i})
				if err != nil {
					t.Fatal(err)
				}
				docs = append(docs, &document.Document{ID: fmt.Sprint(i + 1), Text: "text", Metadata: facts})
			}
			store, fixture := qdrantFilterStore(t, docs)
			store.schema.metric, fixture.metric = metric, metric
			for _, doc := range docs {
				fixture.scores[doc.ID] = 40
				if metric == qdrantclient.Distance_Cosine {
					fixture.scores[doc.ID] = .4
				}
			}
			fixture.scores[docs[len(docs)-1].ID] = 50
			if metric == qdrantclient.Distance_Cosine {
				fixture.scores[docs[len(docs)-1].ID] = .5
			}
			if metric == qdrantclient.Distance_Euclid || metric == qdrantclient.Distance_Manhattan {
				fixture.scores[docs[len(docs)-1].ID] = 0
			}
			response, err := store.Query(t.Context(), &vectorstore.Query{Space: fixtureSpace, Values: []float64{1, 0}, Options: vectorstore.SearchOptions{TopK: 1, Filter: filter.EQ("value", "a")}})
			if err != nil || len(response.Results) != 1 || response.Results[0].Document.ID != docs[len(docs)-1].ID {
				t.Fatalf("ranking=%v error=%v", response, err)
			}
		})
	}
}

func TestReturnedRecordsAreValidated(t *testing.T) {
	for name, mutate := range map[string]func(*qdrantclient.PointStruct){
		"missing content": func(point *qdrantclient.PointStruct) { delete(point.Payload, contentField) },
		"expanded payload": func(point *qdrantclient.PointStruct) {
			point.Payload["legacy"] = &qdrantclient.Value{Kind: &qdrantclient.Value_StringValue{StringValue: "x"}}
		},
		"wrong metadata type": func(point *qdrantclient.PointStruct) {
			point.Payload[metadataField] = &qdrantclient.Value{Kind: &qdrantclient.Value_IntegerValue{IntegerValue: 1}}
		},
		"invalid metadata": func(point *qdrantclient.PointStruct) {
			point.Payload[metadataField] = &qdrantclient.Value{Kind: &qdrantclient.Value_StringValue{StringValue: `{"x":1,"x":2}`}}
		},
		"wrong width": func(point *qdrantclient.PointStruct) { point.Vectors = qdrantclient.NewVectorsDense([]float32{1}) },
		"nonfinite vector": func(point *qdrantclient.PointStruct) {
			point.Vectors = qdrantclient.NewVectorsDense([]float32{float32(math.Inf(1)), 0})
		},
	} {
		t.Run(name, func(t *testing.T) {
			store, fixture := qdrantFilterStore(t, []*document.Document{{ID: "1", Text: "text"}})
			mutate(fixture.points[0])
			response, err := store.Query(t.Context(), &vectorstore.Query{Space: fixtureSpace, Values: []float64{1, 0}, Options: vectorstore.SearchOptions{MinScore: 1}})
			if err == nil || response != nil {
				t.Fatalf("malformed source became empty success: %v, %v", response, err)
			}
			if err = store.DeleteWhere(t.Context(), filter.GT("value", 2)); err == nil || len(fixture.deleted) > 0 {
				t.Fatalf("incomplete source allowed deletion: %v", err)
			}
		})
	}
}

func TestNativeQueryFailuresNeverReturnPartialSuccess(t *testing.T) {
	for _, failure := range []string{"nil hit", "wrong vector", "nonfinite score", "negative distance", "duplicate", "surplus", "RPC error"} {
		t.Run(failure, func(t *testing.T) {
			store, fixture := qdrantFilterStore(t, []*document.Document{{ID: "1", Text: "text"}})
			if failure == "negative distance" {
				store.schema.metric = qdrantclient.Distance_Euclid
				fixture.metric = store.schema.metric
			}
			fixture.queryHook = func(*qdrantclient.QueryPoints) (*qdrantclient.QueryResponse, error) {
				point := fixture.points[0]
				hit := &qdrantclient.ScoredPoint{Id: point.Id, Payload: point.Payload, Vectors: nativeOutput(point.Vectors), Score: 0}
				page := &qdrantclient.QueryResponse{Result: []*qdrantclient.ScoredPoint{hit}}
				switch failure {
				case "nil hit":
					page.Result[0] = nil
				case "wrong vector":
					hit.Vectors = nil
				case "nonfinite score":
					hit.Score = float32(math.NaN())
				case "negative distance":
					hit.Score = -1
				case "duplicate":
					page.Result = append(page.Result, hit)
				case "surplus":
					page.Result = append(page.Result, hit, hit)
				case "RPC error":
					return nil, errors.New("native query failed")
				}
				return page, nil
			}
			response, err := store.Query(t.Context(), &vectorstore.Query{Space: fixtureSpace, Values: []float64{1, 0}, Options: vectorstore.SearchOptions{TopK: 2, MinScore: 1}})
			if err == nil || response != nil {
				t.Fatalf("invalid hit produced response=%v error=%v", response, err)
			}
		})
	}
}

func TestNativeFilterNeverScrolls(t *testing.T) {
	store, fixture := qdrantFilterStore(t, []*document.Document{{ID: "1", Text: "text"}})
	if _, err := store.Query(t.Context(), &vectorstore.Query{Space: fixtureSpace, Values: []float64{1, 0}, Options: vectorstore.SearchOptions{Filter: filter.IsNull("value")}}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteWhere(t.Context(), filter.IsNull("value")); err != nil {
		t.Fatal(err)
	}
	if fixture.scrolls != 0 {
		t.Fatalf("native predicate triggered %d full-source scrolls", fixture.scrolls)
	}
}

func TestReservedStorageFieldRejectsEntireBatchBeforePublication(t *testing.T) {
	store, fixture := qdrantFilterStore(t, nil)
	docs := []*document.Document{{ID: "1", Text: "valid"}, {ID: "2", Text: "forged", Metadata: metadata.Map{document.StoredProvenanceKey: json.RawMessage("null")}}}
	if err := store.CheckDocuments(t.Context(), fixtureSpace, docs); !errors.Is(err, vectorstore.ErrInvalidDocument) {
		t.Fatalf("admission accepted a forged source: %v", err)
	}
	if err := store.Upsert(t.Context(), fixtureBatch(docs)); !errors.Is(err, vectorstore.ErrInvalidDocument) || fixture.upserts != 0 {
		t.Fatalf("invalid batch produced %d native writes: %v", fixture.upserts, err)
	}
}
