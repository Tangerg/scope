package mongodb

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/filter/keyword"
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
	if len(pipeline) > 0 && pipeline[0][0].Key == "$match" {
		limit := pipeline[1][0].Value.(int64) - 1
		maxBytes := pipeline[4][0].Value.(bson.M)["within"].(bson.M)["$and"].(bson.A)[1].(bson.M)["$lte"].(bson.A)[1].(int64)
		var sum int64
		var rows []any
		for i, record := range f.records {
			sum += int64(len(record))
			fits := int64(i) < limit && sum <= maxBytes
			row := bson.M{"within": fits}
			if fits {
				row["document"] = record
			}
			rows = append(rows, row)
			if !fits {
				break
			}
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
		if field, ok := selected[idField].(bson.M); ok {
			ids = field["$in"].([]string)
			f.queryBatches = append(f.queryBatches, len(ids))
		}
	}
	var hits []bson.M
	for _, record := range f.records {
		id := record.Lookup(idField).StringValue()
		if where, ok := query["filter"].(bson.M); ok && !f.ignoreSelected {
			if criteria, ok := where[termsField].(bson.M); ok {
				var terms []string
				_ = record.Lookup(termsField).Unmarshal(&terms)
				if !slices.Contains(terms, criteria["$in"].([]string)[0]) {
					continue
				}
			}
		}
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
	var term string
	if id, ok := constraint[idField].(string); ok {
		ids = []string{id}
		equality := constraint["$expr"].(bson.M)["$eq"].(bson.A)
		if equality[0] != "$"+metadataField {
			return nil, errors.New("conditional delete used wrong metadata field")
		}
		observed = new(equality[1].(bson.M)["$literal"].(string))
	} else if token, ok := constraint[termsField].(string); ok {
		term = token
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
		var terms []string
		_ = record.Lookup(termsField).Unmarshal(&terms)
		if (slices.Contains(ids, id) || term != "" && slices.Contains(terms, term)) && (observed == nil || currentOK && current == *observed) {
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
	collection := &filterCollection{policy: []any{nativeIndexPolicy(DefaultVectorIndexName, "cosine", 2)}, scores: make(map[string]float64)}
	store := upsertStore(t, collection)
	if len(docs) == 0 {
		return store, collection
	}
	if err := store.Upsert(t.Context(), fixtureBatch(docs)); err != nil {
		t.Fatal(err)
	}
	return store, collection
}

func nativeIndexPolicy(name, metric string, dimension int) bson.M {
	return bson.M{"name": name, "type": "vectorSearch", "status": "READY", "queryable": true, "latestDefinition": bson.M{"fields": bson.A{bson.M{"type": "vector", "path": fixtureSpace.StorageKey(), "numDimensions": dimension, "similarity": metric}, bson.M{"type": "filter", "path": termsField}, bson.M{"type": "filter", "path": idField}}}}
}

func nativeRecord(t *testing.T, id, facts string) bson.Raw {
	t.Helper()
	var m metadata.Map
	_ = m.UnmarshalJSON([]byte(facts))
	terms, err := keyword.Project(m)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := bson.Marshal(bson.M{idField: id, contentField: "text", fixtureSpace.StorageKey(): []float32{1, 0}, metadataField: facts, termsField: terms})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestFilterConformance(t *testing.T) {
	storetest.FilterConformance(t, storetest.FilterConfig{Query: func(ctx context.Context, docs []*document.Document, predicate filter.Predicate) ([]string, error) {
		store, _ := mongoFilterStore(t, docs)
		response, err := fixtureQuery(ctx, store, &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("filter conformance"), Options: vectorstore.SearchOptions{TopK: len(docs), Filter: predicate}})
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
	if err := store.DeleteMatching(t.Context(), filter.EQ("value", "x"), fixtureBudget()); !errors.Is(err, want) {
		t.Fatal(err)
	}
	if len(collection.deleted) != 0 {
		t.Fatalf("deleted=%v", collection.deleted)
	}
}

func TestDeleteWhereRetainsChangedMetadata(t *testing.T) {
	store, collection := mongoFilterStore(t, []*document.Document{{ID: "one", Text: "text", Metadata: metadata.Map{"value": []byte(`"X"`)}}})
	collection.beforeDelete = func() { collection.records[0] = nativeRecord(t, "one", `{"value":"x"}`) }
	if err := store.DeleteMatching(t.Context(), filter.EQ("value", "X"), fixtureBudget()); err != nil {
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
	response, err := fixtureQuery(t.Context(), store, &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("q"), Options: vectorstore.SearchOptions{TopK: 1, Filter: filter.EQ("value", "x")}})
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
			response, err := fixtureQuery(t.Context(), store, &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("q"), Options: vectorstore.SearchOptions{TopK: 1, Filter: filter.EQ("value", "x")}})
			if err == nil || response != nil {
				t.Fatalf("response=%v error=%v", response, err)
			}
		})
	}
}

const fixtureSpace vectorstore.SpaceID = "paired-v1"

func fixtureVector(_ string) []float64 { return []float64{1, 0} }
func fixtureBatch(docs []*document.Document) *vectorstore.Batch {
	b := &vectorstore.Batch{Space: fixtureSpace}
	for _, doc := range docs {
		b.Records = append(b.Records, vectorstore.Record{Document: doc, Values: []float64{1, 0}})
	}
	return b
}
func fixtureBudget() vectorstore.ScanBudget {
	return vectorstore.ScanBudget{MaxRecords: 10000, MaxBytes: 32 << 20, Timeout: time.Minute}
}
func fixtureQuery(ctx context.Context, s *Store, q *vectorstore.Query) (*vectorstore.SearchResponse, error) {
	if q.Options.Filter != nil {
		return s.Scan(ctx, q, fixtureBudget())
	}
	return s.Query(ctx, q)
}

func TestNativeEqualityDoesNotEnumerateOrCoerceArrays(t *testing.T) {
	s, collection := mongoFilterStore(t, []*document.Document{{ID: "scalar", Text: "text", Metadata: metadata.Map{"k": []byte(`"x"`)}}, {ID: "array", Text: "text", Metadata: metadata.Map{"k": []byte(`["x"]`)}}})
	collection.enumerationErr = errors.New("enumeration not authorized")
	q := &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("q"), Options: vectorstore.SearchOptions{Filter: filter.EQ("k", "x")}}
	hits, err := s.Query(t.Context(), q)
	if err != nil || len(hits.Results) != 1 || hits.Results[0].Document.ID != "scalar" {
		t.Fatalf("reply=%v err=%v", hits, err)
	}
	q.Options.Filter = filter.EQ("k", 1)
	if hits, err = s.Query(t.Context(), q); !errors.Is(err, vectorstore.ErrUnsupportedFilter) || hits != nil {
		t.Fatalf("unrepresentable filter=%v err=%v", hits, err)
	}
}

func TestScanRejectsUnpaidNonmatchesBeforePublication(t *testing.T) {
	s, collection := mongoFilterStore(t, []*document.Document{{ID: "one", Text: "text"}, {ID: "two", Text: "text"}})
	q := &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("q"), Options: vectorstore.SearchOptions{Filter: filter.IsNull("k")}}
	for _, budget := range []vectorstore.ScanBudget{{MaxBytes: 10240, MaxRecords: 1, Timeout: time.Second}, {MaxBytes: 1, MaxRecords: 2, Timeout: time.Second}} {
		if got, err := s.Scan(t.Context(), q, budget); !errors.Is(err, vectorstore.ErrScanBudget) || got != nil {
			t.Fatalf("got=%v err=%v", got, err)
		}
		if err := s.DeleteMatching(t.Context(), q.Options.Filter, budget); !errors.Is(err, vectorstore.ErrScanBudget) || len(collection.deleted) != 0 {
			t.Fatalf("deleted=%v err=%v", collection.deleted, err)
		}
	}
}

func TestSpaceDriftPreventsMutation(t *testing.T) {
	s, collection := mongoFilterStore(t, nil)
	definition := collection.policy[0].(bson.M)["latestDefinition"].(bson.M)
	definition["fields"].(bson.A)[0].(bson.M)["path"] = vectorstore.SpaceID("other-model-same-width").StorageKey()
	err := s.Upsert(t.Context(), fixtureBatch([]*document.Document{{ID: "one", Text: "text"}}))
	if !errors.Is(err, vectorstore.ErrIncompatibleSpace) || collection.writes != 0 {
		t.Fatalf("writes=%d err=%v", collection.writes, err)
	}
}
