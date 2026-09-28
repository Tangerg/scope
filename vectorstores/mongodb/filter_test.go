package mongodb

import (
	"bytes"
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
	DocumentCollection
	records        []bson.Raw
	deleted        []string
	enumerationErr error
	beforeDelete   func()
	beforeQuery    func()
	ignoreSelected bool
	queryBatches   []int
}

func (f *filterCollection) Aggregate(_ context.Context, value any, _ ...options.Lister[options.AggregateOptions]) (*mongo.Cursor, error) {
	pipeline := value.(mongo.Pipeline)
	var ids []string
	var limit int
	if pipeline[0][0].Key == "$vectorSearch" {
		if f.beforeQuery != nil {
			f.beforeQuery()
			f.beforeQuery = nil
		}
		query := pipeline[0][0].Value.(bson.M)
		limit = query["limit"].(int)
		if scope, ok := query["filter"].(bson.M); ok {
			idFilter, ok := scope[defaultIDField].(bson.M)
			if !ok {
				return nil, errors.New("vector filter must use selected IDs")
			}
			ids = idFilter["$in"].([]string)
			f.queryBatches = append(f.queryBatches, len(ids))
		}
	} else if f.enumerationErr != nil {
		return nil, f.enumerationErr
	}
	var rows []any
	for _, record := range f.records {
		if !f.ignoreSelected && ids != nil && !slices.Contains(ids, record.Lookup(defaultIDField).StringValue()) {
			continue
		}
		rows = append(rows, record)
	}
	if limit > 0 {
		slices.SortFunc(rows, func(left, right any) int {
			leftScore := left.(bson.Raw).Lookup(scoreField).Double()
			rightScore := right.(bson.Raw).Lookup(scoreField).Double()
			if leftScore > rightScore {
				return -1
			}
			if leftScore < rightScore {
				return 1
			}
			return 0
		})
	}
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	return mongo.NewCursorFromDocuments(rows, nil, nil)
}

func (f *filterCollection) DeleteMany(_ context.Context, value any, builders ...options.Lister[options.DeleteManyOptions]) (*mongo.DeleteResult, error) {
	constraint := value.(bson.M)
	id := constraint[defaultIDField].(string)
	var settings options.DeleteManyOptions
	for _, builder := range builders {
		for _, apply := range builder.List() {
			if err := apply(&settings); err != nil {
				return nil, err
			}
		}
	}
	if settings.Collation == nil || settings.Collation.Locale != "simple" {
		return nil, errors.New("conditional deletion requires binary collation")
	}
	var observed bson.RawValue
	if expression, exists := constraint["$expr"]; exists {
		equality := expression.(bson.M)["$eq"].(bson.A)
		if equality[0] != "$"+DefaultMetadataField {
			return nil, errors.New("conditional deletion compared the wrong field")
		}
		observed = equality[1].(bson.M)["$literal"].(bson.RawValue)
	} else if constraint[DefaultMetadataField].(bson.M)["$exists"] != false {
		return nil, errors.New("conditional deletion omitted metadata observation")
	}
	if f.beforeDelete != nil {
		f.beforeDelete()
		f.beforeDelete = nil
	}
	var kept []bson.Raw
	var count int64
	for _, record := range f.records {
		current := record.Lookup(DefaultMetadataField)
		if record.Lookup(defaultIDField).StringValue() == id && current.Type == observed.Type && bytes.Equal(current.Value, observed.Value) {
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
	collection := &filterCollection{}
	for _, doc := range docs {
		values, err := doc.Metadata.Values()
		if err != nil {
			t.Fatal(err)
		}
		converted, err := metadataDocument(values)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := bson.Marshal(bson.M{defaultIDField: doc.ID, DefaultContentField: doc.Text, DefaultMetadataField: converted, scoreField: 1.0})
		if err != nil {
			t.Fatal(err)
		}
		collection.records = append(collection.records, raw)
	}
	return upsertStore(t, collection), collection
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
	collection := &filterCollection{}
	for _, record := range []bson.M{{"_id": "scalar", "metadata": bson.M{"value": "x"}}, {"_id": "array", "metadata": bson.M{"value": bson.A{"x"}}}} {
		raw, err := bson.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		collection.records = append(collection.records, raw)
	}
	store := upsertStore(t, collection)
	if err := store.DeleteWhere(t.Context(), filter.EQ("value", "x")); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(collection.deleted, []string{"scalar"}) {
		t.Fatalf("deleted %v", collection.deleted)
	}
	if len(collection.records) != 1 || collection.records[0].Lookup("_id").StringValue() != "array" {
		t.Fatalf("survivors %v", collection.records)
	}
}

func TestDeleteWhereDoesNotMutateAfterEnumerationFailure(t *testing.T) {
	want := errors.New("cursor unavailable")
	collection := &filterCollection{enumerationErr: want}
	store := upsertStore(t, collection)
	if err := store.DeleteWhere(t.Context(), filter.EQ("value", "x")); !errors.Is(err, want) {
		t.Fatalf("error %v", err)
	}
	if len(collection.deleted) != 0 {
		t.Fatalf("deleted %v", collection.deleted)
	}
}

func TestDeleteWhereRetainsChangedMetadata(t *testing.T) {
	for name, replacement := range map[string]any{
		"case":  bson.M{"value": "x"},
		"array": bson.A{bson.M{"value": "X"}},
	} {
		t.Run(name, func(t *testing.T) {
			values, err := metadata.FromValues(map[string]any{"value": "X"})
			if err != nil {
				t.Fatal(err)
			}
			store, collection := mongoFilterStore(t, []*document.Document{{ID: "one", Text: "text", Metadata: values}})
			collection.beforeDelete = func() {
				raw, err := bson.Marshal(bson.M{defaultIDField: "one", DefaultMetadataField: replacement})
				if err != nil {
					t.Fatal(err)
				}
				collection.records[0] = raw
			}
			if err := store.DeleteWhere(t.Context(), filter.EQ("value", "X")); err != nil {
				t.Fatal(err)
			}
			if len(collection.deleted) != 0 || len(collection.records) != 1 {
				t.Fatalf("deleted %v; survivors %d", collection.deleted, len(collection.records))
			}
		})
	}
}

func TestFilteredSearchMergesBoundedIDBatches(t *testing.T) {
	values, err := metadata.FromValues(map[string]any{"value": "x"})
	if err != nil {
		t.Fatal(err)
	}
	var docs []*document.Document
	for index := range filterPageSize + 1 {
		docs = append(docs, &document.Document{ID: fmt.Sprintf("%04d", index), Text: "text", Metadata: values})
	}
	store, collection := mongoFilterStore(t, docs)
	for index, doc := range docs {
		raw, marshalErr := bson.Marshal(bson.M{defaultIDField: doc.ID, DefaultContentField: doc.Text, DefaultMetadataField: bson.M{"value": "x"}, scoreField: float64(index+1) / float64(len(docs))})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		collection.records[index] = raw
	}
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 1, Filter: filter.EQ("value", "x")}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(collection.queryBatches, []int{filterPageSize, 1}) {
		t.Fatalf("query batches %v", collection.queryBatches)
	}
	if len(response.Results) != 1 || response.Results[0].Document.ID != docs[len(docs)-1].ID {
		t.Fatalf("results %+v", response.Results)
	}
}

func TestFilteredSearchRejectsChangedSelection(t *testing.T) {
	for _, changeID := range []bool{false, true} {
		t.Run(fmt.Sprint("change_id_", changeID), func(t *testing.T) {
			values, err := metadata.FromValues(map[string]any{"value": "x"})
			if err != nil {
				t.Fatal(err)
			}
			store, collection := mongoFilterStore(t, []*document.Document{{ID: "one", Text: "text", Metadata: values}})
			collection.beforeQuery = func() {
				id, value := "one", "y"
				if changeID {
					id, value = "other", "x"
					collection.ignoreSelected = true
				}
				raw, marshalErr := bson.Marshal(bson.M{defaultIDField: id, DefaultContentField: "text", DefaultMetadataField: bson.M{"value": value}, scoreField: 1.0})
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				collection.records[0] = raw
			}
			response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 1, Filter: filter.EQ("value", "x")}})
			if err == nil || response != nil {
				t.Fatalf("response %+v, error %v", response, err)
			}
		})
	}
}
