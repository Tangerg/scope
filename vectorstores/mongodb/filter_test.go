package mongodb

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
)

type filterCollection struct {
	records        []bson.Raw
	scores         map[string]float64
	deleted        []string
	enumerationErr error
	queryErr       error
	beforeDelete   func()
	beforeQuery    func()
	ignoreSelected bool
	queryBatches   []int
	writes         int
	policy         []any
}

func (f *filterCollection) Aggregate(_ context.Context, value any, _ ...options.Lister[options.AggregateOptions]) (*mongo.Cursor, error) {
	pipeline := value.(mongo.Pipeline)
	if len(pipeline) != 0 && pipeline[0][0].Key == "$listSearchIndexes" {
		return mongo.NewCursorFromDocuments(f.policy, nil, nil)
	}
	if len(pipeline) == 0 {
		var rows []any
		for _, record := range f.records {
			rows = append(rows, record)
		}
		return mongo.NewCursorFromDocuments(rows, f.enumerationErr, nil)
	}
	if f.queryErr != nil {
		return nil, f.queryErr
	}
	if len(pipeline) != 2 || pipeline[0][0].Key != "$vectorSearch" || pipeline[1][0].Key != "$addFields" {
		return nil, errors.New("query must preserve all returned hits before Core MinScore")
	}
	if f.beforeQuery != nil {
		f.beforeQuery()
		f.beforeQuery = nil
	}
	query := pipeline[0][0].Value.(bson.M)
	var ids []string
	if selected, ok := query["filter"].(bson.M); ok {
		ids = selected[idField].(bson.M)["$in"].([]string)
		f.queryBatches = append(f.queryBatches, len(ids))
	}
	var hits []bson.M
	for _, record := range f.records {
		id := record.Lookup(idField).StringValue()
		if ids != nil && !f.ignoreSelected && !slices.Contains(ids, id) {
			continue
		}
		var row bson.M
		if err := bson.Unmarshal(record, &row); err != nil {
			return nil, err
		}
		score := 1.0
		if value, exists := f.scores[id]; exists {
			score = value
		}
		row[scoreField] = score
		hits = append(hits, row)
	}
	slices.SortFunc(hits, func(left, right bson.M) int {
		return cmp.Compare(right[scoreField].(float64), left[scoreField].(float64))
	})
	hits = hits[:min(len(hits), query["limit"].(int))]
	var rows []any
	for _, hit := range hits {
		rows = append(rows, hit)
	}
	return mongo.NewCursorFromDocuments(rows, nil, nil)
}

func (f *filterCollection) BulkWrite(_ context.Context, writes []mongo.WriteModel, _ ...options.Lister[options.BulkWriteOptions]) (*mongo.BulkWriteResult, error) {
	f.writes++
	for _, write := range writes {
		replacement := write.(*mongo.ReplaceOneModel)
		if replacement.Collation == nil || replacement.Collation.Locale != "simple" {
			return nil, errors.New("native identity replacement needs binary collation")
		}
		raw, err := bson.Marshal(replacement.Replacement)
		if err != nil {
			return nil, err
		}
		id := bson.Raw(raw).Lookup(idField).StringValue()
		kept := slices.DeleteFunc(f.records, func(record bson.Raw) bool { return record.Lookup(idField).StringValue() == id })
		f.records = append(kept, raw)
	}
	return &mongo.BulkWriteResult{Acknowledged: true, UpsertedCount: int64(len(writes))}, nil
}

func (f *filterCollection) DeleteMany(_ context.Context, value any, builders ...options.Lister[options.DeleteManyOptions]) (*mongo.DeleteResult, error) {
	var settings options.DeleteManyOptions
	for _, builder := range builders {
		for _, apply := range builder.List() {
			if err := apply(&settings); err != nil {
				return nil, err
			}
		}
	}
	if settings.Collation == nil || settings.Collation.Locale != "simple" {
		return nil, errors.New("native identity deletion needs binary collation")
	}
	constraint := value.(bson.M)
	var ids []string
	var observed *string
	if id, ok := constraint[idField].(string); ok {
		ids = []string{id}
		equality := constraint["$expr"].(bson.M)["$eq"].(bson.A)
		if equality[0] != "$"+metadataField {
			return nil, errors.New("conditional delete used wrong metadata field")
		}
		observed = new(equality[1].(bson.M)["$literal"].(string))
	} else {
		ids = constraint[idField].(bson.M)["$in"].([]string)
	}
	if f.beforeDelete != nil {
		f.beforeDelete()
		f.beforeDelete = nil
	}
	var kept []bson.Raw
	var count int64
	for _, record := range f.records {
		id := record.Lookup(idField).StringValue()
		current, currentOK := record.Lookup(metadataField).StringValueOK()
		if slices.Contains(ids, id) && (observed == nil || currentOK && current == *observed) {
			f.deleted = append(f.deleted, id)
			count++
			continue
		}
		kept = append(kept, record)
	}
	f.records = kept
	return &mongo.DeleteResult{Acknowledged: true, DeletedCount: count}, nil
}

func mongoFilterStore(t *testing.T, docs []*document.Document) (*Store, *filterCollection) {
	t.Helper()
	collection := &filterCollection{scores: make(map[string]float64)}
	store := upsertStore(t, collection)
	if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: docs}); err != nil {
		t.Fatal(err)
	}
	return store, collection
}

func nativeIndexPolicy(name, metric string, dimension int) bson.M {
	return bson.M{"name": name, "type": "vectorSearch", "status": "READY", "queryable": true, "latestDefinition": bson.M{"fields": bson.A{bson.M{"type": "vector", "path": embeddingField, "numDimensions": dimension, "similarity": metric}, bson.M{"type": "filter", "path": idField}}}}
}

func nativeRecord(t *testing.T, id, facts string) bson.Raw {
	t.Helper()
	raw, err := bson.Marshal(bson.M{idField: id, contentField: "text", embeddingField: []float32{1, 0}, metadataField: facts})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestFilterConformance(t *testing.T) {
	storetest.FilterConformance(t, storetest.FilterConfig{Query: func(ctx context.Context, docs []*document.Document, predicate filter.Predicate) ([]string, error) {
		store, _ := mongoFilterStore(t, docs)
		response, err := store.Search(ctx, &vectorstore.SearchRequest{Query: "filter conformance", Options: vectorstore.SearchOptions{TopK: len(docs), Filter: predicate}})
		if err != nil {
			return nil, err
		}
		var ids []string
		for _, hit := range response.Results {
			ids = append(ids, hit.Document.ID)
		}
		return ids, nil
	}})
}

func TestDeleteWhereDoesNotSelectArrayForScalarEquality(t *testing.T) {
	store, collection := mongoFilterStore(t, []*document.Document{{ID: "scalar", Text: "text", Metadata: metadata.Map{"value": []byte(`"x"`)}}, {ID: "array", Text: "text", Metadata: metadata.Map{"value": []byte(`["x"]`)}}})
	if err := store.DeleteWhere(t.Context(), filter.EQ("value", "x")); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(collection.deleted, []string{"scalar"}) || len(collection.records) != 1 {
		t.Fatalf("deleted=%v remaining=%v", collection.deleted, collection.records)
	}
}

func TestDeleteWhereDoesNotMutateAfterEnumerationFailure(t *testing.T) {
	store, collection := mongoFilterStore(t, []*document.Document{{ID: "one", Text: "text"}})
	want := errors.New("cursor unavailable")
	collection.enumerationErr = want
	if err := store.DeleteWhere(t.Context(), filter.EQ("value", "x")); !errors.Is(err, want) {
		t.Fatal(err)
	}
	if len(collection.deleted) != 0 {
		t.Fatalf("deleted=%v", collection.deleted)
	}
}

func TestDeleteWhereRetainsChangedMetadata(t *testing.T) {
	store, collection := mongoFilterStore(t, []*document.Document{{ID: "one", Text: "text", Metadata: metadata.Map{"value": []byte(`"X"`)}}})
	collection.beforeDelete = func() { collection.records[0] = nativeRecord(t, "one", `{"value":"x"}`) }
	if err := store.DeleteWhere(t.Context(), filter.EQ("value", "X")); err != nil {
		t.Fatal(err)
	}
	if len(collection.deleted) != 0 || len(collection.records) != 1 {
		t.Fatal("delete discarded a changed metadata value")
	}
}

func TestFilteredSearchMergesBoundedIDBatches(t *testing.T) {
	var docs []*document.Document
	for index := range filterPageSize + 1 {
		docs = append(docs, &document.Document{ID: fmt.Sprintf("%04d", index), Text: "text", Metadata: metadata.Map{"value": []byte(`"x"`)}})
	}
	store, collection := mongoFilterStore(t, docs)
	for index, doc := range docs {
		collection.scores[doc.ID] = float64(index+1) / float64(len(docs))
	}
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "q", Options: vectorstore.SearchOptions{TopK: 1, Filter: filter.EQ("value", "x")}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(collection.queryBatches, []int{filterPageSize, 1}) || len(response.Results) != 1 || response.Results[0].Document.ID != docs[len(docs)-1].ID {
		t.Fatalf("groups=%v response=%v", collection.queryBatches, response)
	}
}

func TestFilteredSearchRejectsChangedSelection(t *testing.T) {
	for _, changeID := range []bool{false, true} {
		t.Run(fmt.Sprint(changeID), func(t *testing.T) {
			store, collection := mongoFilterStore(t, []*document.Document{{ID: "one", Text: "text", Metadata: metadata.Map{"value": []byte(`"x"`)}}})
			collection.beforeQuery = func() {
				id, facts := "one", `{"value":"y"}`
				if changeID {
					id, facts = "other", `{"value":"x"}`
					collection.ignoreSelected = true
				}
				collection.records[0] = nativeRecord(t, id, facts)
			}
			response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "q", Options: vectorstore.SearchOptions{TopK: 1, Filter: filter.EQ("value", "x")}})
			if err == nil || response != nil {
				t.Fatalf("response=%v error=%v", response, err)
			}
		})
	}
}
