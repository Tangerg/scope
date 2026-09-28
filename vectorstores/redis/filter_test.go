package redis

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"testing"

	goredis "github.com/redis/go-redis/v9"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/embeddingclient"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
)

type filterRecords struct {
	goredis.UniversalClient
	records        map[string]goredis.Document
	pages          [][]string
	cursors        []uint64
	scans          int
	scanErr        error
	deleted        []string
	beforeDelete   func()
	beforeQuery    func()
	queryBatches   []int
	ignoreSelected bool
}

func (f *filterRecords) Scan(ctx context.Context, cursor uint64, _ string, _ int64) *goredis.ScanCmd {
	command := goredis.NewScanCmd(ctx, nil)
	if f.scanErr != nil {
		command.SetErr(f.scanErr)
		return command
	}
	if f.pages != nil {
		if f.scans >= len(f.pages) {
			command.SetErr(errors.New("unexpected scan"))
			return command
		}
		command.SetVal(f.pages[f.scans], f.cursors[f.scans])
		f.scans++
		return command
	}
	var keys []string
	for key := range f.records {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	command.SetVal(keys, 0)
	return command
}

func (f *filterRecords) HGet(ctx context.Context, key, field string) *goredis.StringCmd {
	command := goredis.NewStringCmd(ctx)
	if value, ok := f.records[key].Fields[field]; ok {
		command.SetVal(value)
	} else {
		command.SetErr(goredis.Nil)
	}
	return command
}

func (f *filterRecords) Eval(ctx context.Context, _ string, keys []string, args ...any) *goredis.Cmd {
	command := goredis.NewCmd(ctx)
	if f.beforeDelete != nil {
		f.beforeDelete()
		f.beforeDelete = nil
	}
	if f.records[keys[0]].Fields[args[0].(string)] == args[1].(string) {
		f.deleted = append(f.deleted, keys[0])
		delete(f.records, keys[0])
		command.SetVal(int64(1))
	} else {
		command.SetVal(int64(0))
	}
	return command
}

func (f *filterRecords) FTSearchWithArgs(ctx context.Context, _ string, _ string, options *goredis.FTSearchOptions) *goredis.FTSearchCmd {
	if f.beforeQuery != nil {
		f.beforeQuery()
		f.beforeQuery = nil
	}
	f.queryBatches = append(f.queryBatches, len(options.InKeys))
	var docs []goredis.Document
	for key, record := range f.records {
		if f.ignoreSelected || len(options.InKeys) == 0 || slices.Contains(options.InKeys, any(key)) {
			docs = append(docs, record)
		}
	}
	slices.SortFunc(docs, func(a, b goredis.Document) int {
		left, _ := strconv.ParseFloat(a.Fields[distanceFieldName], 64)
		right, _ := strconv.ParseFloat(b.Fields[distanceFieldName], 64)
		if left < right {
			return -1
		}
		if left > right {
			return 1
		}
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	if len(docs) > options.Limit {
		docs = docs[:options.Limit]
	}
	command := new(goredis.FTSearchCmd)
	command.SetVal(goredis.FTSearchResult{Docs: docs, Total: len(docs)})
	return command
}

func redisFilterStore(t *testing.T, docs []*document.Document) (*Store, *filterRecords) {
	t.Helper()
	client := &filterRecords{records: make(map[string]goredis.Document)}
	for _, doc := range docs {
		raw, err := jsonv2.Marshal(doc.Metadata)
		if err != nil {
			t.Fatal(err)
		}
		key := DefaultKeyPrefix + doc.ID
		client.records[key] = goredis.Document{ID: key, Fields: map[string]string{DefaultContentField: doc.Text, DefaultMetadataJSONField: string(raw), distanceFieldName: "0"}}
	}
	store := &Store{client: client, keyPrefix: DefaultKeyPrefix, metadataJSONField: DefaultMetadataJSONField, contentField: DefaultContentField, embeddingField: DefaultEmbeddingField, distanceMetric: DistanceCosine}
	model := embedding.ModelFunc(func(context.Context, *embedding.Request) (*embedding.Response, error) {
		return embedding.NewResponse([]*embedding.Output{{Embedding: []float64{1, 0}}}, nil)
	})
	var err error
	store.embeddingClient, err = embeddingclient.New(model)
	if err != nil {
		t.Fatal(err)
	}
	return store, client
}

func TestFilterConformance(t *testing.T) {
	storetest.FilterConformance(t, storetest.FilterConfig{Query: func(ctx context.Context, docs []*document.Document, predicate filter.Predicate) ([]string, error) {
		store, _ := redisFilterStore(t, docs)
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

func TestDeleteWhereEnumeratesAllPagesBeforeDeleting(t *testing.T) {
	store, client := redisFilterStore(t, nil)
	client.pages = [][]string{{"embedding:a"}, {}, {"embedding:b"}}
	client.cursors = []uint64{7, 9, 0}
	for _, id := range []string{"a", "b"} {
		client.records["embedding:"+id] = goredis.Document{Fields: map[string]string{DefaultMetadataJSONField: `{"value":"x"}`}}
	}
	if err := store.DeleteWhere(t.Context(), filter.EQ("value", "x")); err != nil {
		t.Fatal(err)
	}
	slices.Sort(client.deleted)
	if !slices.Equal(client.deleted, []string{"embedding:a", "embedding:b"}) {
		t.Fatalf("deleted %v", client.deleted)
	}
	if client.scans != 3 {
		t.Fatalf("scans %d", client.scans)
	}
}

func TestDeleteWhereRejectsForeignKeyBeforeDeleting(t *testing.T) {
	store, client := redisFilterStore(t, nil)
	client.pages = [][]string{{"other:victim"}}
	client.cursors = []uint64{0}
	if err := store.DeleteWhere(t.Context(), filter.EQ("value", "x")); err == nil {
		t.Fatal("accepted foreign key")
	}
	if len(client.deleted) != 0 {
		t.Fatalf("deleted %v", client.deleted)
	}
	if _, err := store.toDocument(goredis.Document{ID: "other:victim"}); err == nil {
		t.Fatal("accepted foreign search hit")
	}
}

func TestDeleteWherePreservesArrayAndCaseVariants(t *testing.T) {
	store, client := redisFilterStore(t, nil)
	for id, value := range map[string]string{"scalar": `"Acme,*"`, "array": `["Acme,*"]`, "lower": `"acme,*"`, "empty": `[]`} {
		client.records["embedding:"+id] = goredis.Document{Fields: map[string]string{DefaultMetadataJSONField: fmt.Sprintf(`{"value":%s}`, value)}}
	}
	if err := store.DeleteWhere(t.Context(), filter.EQ("value", "Acme,*")); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(client.deleted, []string{"embedding:scalar"}) {
		t.Fatalf("deleted %v", client.deleted)
	}
}

func TestDeleteWhereRetainsChangedMetadata(t *testing.T) {
	store, client := redisFilterStore(t, nil)
	client.records["embedding:one"] = goredis.Document{Fields: map[string]string{DefaultMetadataJSONField: `{"value":"X"}`}}
	client.beforeDelete = func() {
		client.records["embedding:one"].Fields[DefaultMetadataJSONField] = `{"value":"x"}`
	}
	if err := store.DeleteWhere(t.Context(), filter.EQ("value", "X")); err != nil {
		t.Fatal(err)
	}
	if len(client.deleted) != 0 || len(client.records) != 1 {
		t.Fatalf("deleted %v; survivors %d", client.deleted, len(client.records))
	}
}

func TestFilteredSearchRejectsChangedSelection(t *testing.T) {
	for _, changeID := range []bool{false, true} {
		t.Run(fmt.Sprint("change_id_", changeID), func(t *testing.T) {
			values, err := metadata.FromValues(map[string]any{"value": "x"})
			if err != nil {
				t.Fatal(err)
			}
			store, client := redisFilterStore(t, []*document.Document{{ID: "one", Text: "text", Metadata: values}})
			client.beforeQuery = func() {
				if changeID {
					record := client.records["embedding:one"]
					delete(client.records, "embedding:one")
					record.ID = "embedding:other"
					client.records[record.ID] = record
					client.ignoreSelected = true
				} else {
					client.records["embedding:one"].Fields[DefaultMetadataJSONField] = `{"value":"y"}`
				}
			}
			response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 1, Filter: filter.EQ("value", "x")}})
			if err == nil || response != nil {
				t.Fatalf("response %+v, error %v", response, err)
			}
		})
	}
}

func TestFilteredSearchMergesBoundedKeyBatches(t *testing.T) {
	values, err := metadata.FromValues(map[string]any{"value": "x"})
	if err != nil {
		t.Fatal(err)
	}
	var docs []*document.Document
	for index := range filterPageSize + 1 {
		docs = append(docs, &document.Document{ID: fmt.Sprintf("%04d", index), Text: "text", Metadata: values})
	}
	store, client := redisFilterStore(t, docs)
	for index, doc := range docs {
		client.records[DefaultKeyPrefix+doc.ID].Fields[distanceFieldName] = strconv.FormatFloat(1-float64(index)/float64(len(docs)), 'g', -1, 64)
	}
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 1, Filter: filter.EQ("value", "x")}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(client.queryBatches, []int{filterPageSize, 1}) {
		t.Fatalf("query batches %v", client.queryBatches)
	}
	if len(response.Results) != 1 || response.Results[0].Document.ID != docs[len(docs)-1].ID {
		t.Fatalf("results %+v", response.Results)
	}
}

func TestRingFilteringRejectsIncompleteEnumeration(t *testing.T) {
	client := goredis.NewRing(&goredis.RingOptions{Addrs: map[string]string{}})
	t.Cleanup(func() { _ = client.Close() })
	store := &Store{client: client}
	if _, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{Filter: filter.EQ("value", "x")}}); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("Search error %v", err)
	}
	if err := store.DeleteWhere(t.Context(), filter.EQ("value", "x")); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("DeleteWhere error %v", err)
	}
}
