package milvus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/milvus-io/milvus-proto/go-api/v2/milvuspb"
	"github.com/milvus-io/milvus-proto/go-api/v2/schemapb"
	"github.com/milvus-io/milvus/client/v2/entity"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/embeddingclient"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
)

func mustPredicate(t *testing.T, source string) filter.Predicate {
	t.Helper()
	predicate, err := filter.Parse(source)
	if err != nil {
		t.Fatal(err)
	}
	return predicate
}

func TestNativeFilterConformance(t *testing.T) {
	storetest.FilterConformance(t, storetest.FilterConfig{Query: func(ctx context.Context, docs []*document.Document, predicate filter.Predicate) ([]string, error) {
		store, service := indexedStore(t, entity.COSINE, docs, nil)
		response, err := store.Search(ctx, &vectorstore.SearchRequest{Query: "q", Options: vectorstore.SearchOptions{Filter: predicate, TopK: len(docs)}})
		if err != nil {
			return nil, err
		}
		var ids []string
		for _, hit := range response.Results {
			ids = append(ids, hit.Document.ID)
		}
		if err := store.DeleteWhere(ctx, predicate); err != nil {
			return nil, err
		}
		if len(service.records) != len(docs)-len(ids) {
			return nil, errors.New("native deletion changed unexpected identities")
		}
		for _, record := range service.records {
			if slices.Contains(ids, record.id) {
				return nil, errors.New("native deletion retained a selected identity")
			}
		}
		return ids, nil
	}})
}

func TestNativeMetadataAndIdentitiesRoundTrip(t *testing.T) {
	docs := []*document.Document{
		{ID: "nil", Text: "nil", Metadata: nil}, {ID: "empty", Text: "empty", Metadata: metadata.Map{}},
		{ID: "quoted\"\\🙂", Text: "native", Metadata: metadata.Map{"huge": json.RawMessage(`1e1000`), "exact": json.RawMessage(`1.00000000000000001`), "$native.key": json.RawMessage(`{"nested":[null,{},9007199254740993]}`), "id": json.RawMessage(`"business ID"`)}},
	}
	store, service := indexedStore(t, entity.IP, docs, nil)
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "q", Options: vectorstore.SearchOptions{TopK: len(docs)}})
	if err != nil {
		t.Fatal(err)
	}
	for _, doc := range docs {
		if !slices.ContainsFunc(response.Results, func(hit *vectorstore.SearchResult) bool {
			return hit.Document.ID == doc.ID && hit.Document.Text == doc.Text && hit.Document.Metadata.Equal(doc.Metadata) && (hit.Document.Metadata == nil) == (doc.Metadata == nil)
		}) {
			t.Fatalf("changed metadata: %v", doc)
		}
	}
	replacement := &document.Document{ID: docs[2].ID, Text: "replacement", Metadata: metadata.Map{"new": json.RawMessage(`true`)}}
	if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{replacement}}); err != nil {
		t.Fatal(err)
	}
	if len(service.records) != 3 {
		t.Fatalf("replacement added identity: %v", service.records)
	}
	if err := store.DeleteIDs(t.Context(), []string{replacement.ID, replacement.ID, "unknown"}); err != nil {
		t.Fatal(err)
	}
	if len(service.records) != 2 {
		t.Fatalf("literal deletion changed identities: %v", service.records)
	}
}

func TestNativePolicyOwnsScoresAndRejectsBadSchema(t *testing.T) {
	for _, metric := range []entity.MetricType{entity.COSINE, entity.L2, entity.IP} {
		t.Run(string(metric), func(t *testing.T) {
			store, service := indexedStore(t, metric, []*document.Document{{ID: "one", Text: "one"}}, nil)
			service.metric = entity.IP
			service.records[0].vector = []float32{8, 0}
			response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "q"})
			if err != nil || len(response.Results) != 1 || response.Results[0].Score != vectorstore.ScoreFromInnerProduct(8) {
				t.Fatalf("native policy changed: %v %v", response, err)
			}
		})
	}
	for _, corrupt := range []string{"old metadata", "extra field", "missing field", "automatic ID", "dynamic", "nullable", "default", "width", "duplicate field", "unknown metric"} {
		t.Run(corrupt, func(t *testing.T) {
			service := newNativeService(entity.COSINE)
			switch corrupt {
			case "old metadata":
				service.schema.Fields[2].DataType = schemapb.DataType_JSON
			case "extra field":
				service.schema.Fields = append(service.schema.Fields, entity.NewField().WithName("metadata_filter").WithDataType(entity.FieldTypeJSON).ProtoMessage())
			case "missing field":
				service.schema.Fields = service.schema.Fields[:3]
			case "automatic ID":
				service.schema.Fields[0].AutoID = true
			case "dynamic":
				service.schema.EnableDynamicField = true
			case "nullable":
				service.schema.Fields[2].Nullable = true
			case "default":
				service.schema.Fields[2].DefaultValue = &schemapb.ValueField{Data: &schemapb.ValueField_StringData{StringData: "{}"}}
			case "width":
				service.schema.Fields[3].TypeParams = nil
			case "duplicate field":
				service.schema.Fields[2].Name = fieldContent
			case "unknown metric":
				service.metric = entity.MetricType("unknown")
			}
			store, err := NewStore(t.Context(), StoreConfig{Client: nativeClient(t, service), CollectionName: "documents", EmbeddingModel: fixtureModel(nil), DocumentBatcher: testBatcher{}})
			if store != nil || !errors.Is(err, ErrSchemaMismatch) {
				t.Fatalf("invalid native schema accepted: %v %v", store, err)
			}
		})
	}
}

func TestInvalidSourceIsRejectedOutsidePredicateAndBeforePublication(t *testing.T) {
	for _, corrupt := range []string{"JSON duplicate", "JSON array", "JSON malformed", "empty ID", "empty text", "infinite vector", "zero cosine vector"} {
		t.Run(corrupt, func(t *testing.T) {
			store, service := indexedStore(t, entity.COSINE, []*document.Document{{ID: "one", Text: "one"}}, nil)
			switch corrupt {
			case "JSON duplicate":
				service.records[0].metadata = `{"x":1,"x":2}`
			case "JSON array":
				service.records[0].metadata = `[]`
			case "JSON malformed":
				service.records[0].metadata = `{`
			case "empty ID":
				service.records[0].id = " "
			case "empty text":
				service.records[0].text = ""
			case "infinite vector":
				service.records[0].vector[0] = float32(math.Inf(1))
			case "zero cosine vector":
				service.records[0].vector = []float32{0, 0}
			}
			response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "q", Options: vectorstore.SearchOptions{Filter: mustPredicate(t, "x == 'absent'")}})
			if err == nil || response != nil || service.searches != 0 {
				t.Fatalf("invalid source hidden: %v %v", response, err)
			}
			if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{{ID: "new", Text: "new"}}}); err == nil || service.writes != 1 {
				t.Fatalf("invalid source permitted publication: %v writes=%d", err, service.writes)
			}
			if err := store.DeleteWhere(t.Context(), mustPredicate(t, "x == 'absent'")); err == nil || service.deletions != 0 {
				t.Fatalf("invalid source permitted deletion: %v", err)
			}
		})
	}
}

func TestLowScoreDoesNotHideInvalidNativeHits(t *testing.T) {
	for _, corrupt := range []string{"empty ID", "empty text", "bad metadata", "bad score", "NaN", "infinite", "changed metadata", "duplicate ID", "missing column"} {
		t.Run(corrupt, func(t *testing.T) {
			store, service := indexedStore(t, entity.COSINE, []*document.Document{{ID: "one", Text: "one", Metadata: metadata.Map{"x": json.RawMessage(`1`)}}}, nil)
			service.searchHook = func(response *milvuspb.SearchResults) {
				rows := response.Results
				rows.Scores[0] = -0.5
				switch corrupt {
				case "empty ID":
					rows.FieldsData[0].GetScalars().GetStringData().Data[0] = ""
				case "empty text":
					rows.FieldsData[1].GetScalars().GetStringData().Data[0] = ""
				case "bad metadata":
					rows.FieldsData[2].GetScalars().GetStringData().Data[0] = "[]"
				case "bad score":
					rows.Scores[0] = -2
				case "NaN":
					rows.Scores[0] = float32(math.NaN())
				case "infinite":
					rows.Scores[0] = float32(math.Inf(1))
				case "changed metadata":
					rows.FieldsData[2].GetScalars().GetStringData().Data[0] = `{"x":2}`
				case "duplicate ID":
					rows.Ids.GetStrId().Data[0] = "other"
				case "missing column":
					rows.FieldsData = rows.FieldsData[:3]
				}
			}
			response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "q", Options: vectorstore.SearchOptions{MinScore: 0.8, Filter: mustPredicate(t, "x == 1")}})
			if err == nil || response != nil {
				t.Fatalf("invalid low-score hit hidden: %v %v", response, err)
			}
		})
	}
}

func TestSourcePaginationAndGroupsPreserveLiteralIdentitiesAndRawRank(t *testing.T) {
	var docs []*document.Document
	for position := range 129 {
		text := "first"
		if position == 128 {
			text = "last"
		}
		docs = append(docs, &document.Document{ID: fmt.Sprintf("id_%03d_\"\\🙂", position), Text: text})
	}
	store, service := indexedStore(t, entity.IP, docs, func(text string) []float64 {
		if text == "last" {
			return []float64{1e20, 0}
		}
		return []float64{1e10, 0}
	})
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "q", Options: vectorstore.SearchOptions{TopK: 1}})
	if err != nil || len(response.Results) != 1 || response.Results[0].Document.ID != docs[128].ID {
		t.Fatalf("native rank changed: %v %v", response, err)
	}
	if !slices.Equal(service.groups, []int{128, 1}) {
		t.Fatalf("native identity groups: %v", service.groups)
	}
	if response.Results[0].Score != 1 {
		t.Fatal("fixture did not expose saturated Core scores")
	}
	service.queryHook = func(response *milvuspb.QueryResults) {
		response.FieldsData[0].GetScalars().GetStringData().Data[0] = docs[0].ID
	}
	response, err = store.Search(t.Context(), &vectorstore.SearchRequest{Query: "q"})
	if err == nil || response != nil {
		t.Fatalf("nonadvancing native source accepted: %v %v", response, err)
	}
}

func TestDeletionKeepsChangedMetadataAndNeverDropsTheGuard(t *testing.T) {
	store, service := indexedStore(t, entity.COSINE, []*document.Document{{ID: "one", Text: "one", Metadata: metadata.Map{"x": json.RawMessage(`"old"`)}}}, nil)
	service.beforeDelete = func() { service.records[0].metadata = `{"x":"NEW"}` }
	if err := store.DeleteWhere(t.Context(), mustPredicate(t, "x == 'old'")); err != nil {
		t.Fatal(err)
	}
	if len(service.records) != 1 {
		t.Fatal("metadata update lost its identity")
	}
	service.beforeDelete = nil
	if err := store.DeleteWhere(t.Context(), mustPredicate(t, "x == 'NEW'")); err != nil {
		t.Fatal(err)
	}
	if len(service.records) != 0 {
		t.Fatal("current matching metadata retained")
	}
}

func TestIndexPreparesAllBatchesBeforeNativeWrites(t *testing.T) {
	for _, failure := range []string{"model", "width", "FLOAT32 overflow", "cosine underflow"} {
		t.Run(failure, func(t *testing.T) {
			store, service := nativeStore(t, entity.COSINE, nil)
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
					case "cosine underflow":
						vector = []float64{math.SmallestNonzeroFloat64, 0}
					}
				}
				return embedding.NewResponse([]*embedding.Output{{Embedding: vector}}, nil)
			}))
			if err != nil {
				t.Fatal(err)
			}
			store.embeddingClient, store.documentBatcher = model, testBatcher{singleton: true}
			if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{{ID: "one", Text: "one"}, {ID: "two", Text: "two"}}}); err == nil || service.writes != 0 || calls != 2 {
				t.Fatalf("late input published prefix: %v writes=%d calls=%d", err, service.writes, calls)
			}
		})
	}
}

func TestIndexRequiresExactNativeAcknowledgment(t *testing.T) {
	for _, failure := range []string{"short count", "surplus", "wrong ID", "missing IDs"} {
		t.Run(failure, func(t *testing.T) {
			store, service := nativeStore(t, entity.COSINE, nil)
			service.upsertHook = func(response *milvuspb.MutationResult) {
				switch failure {
				case "short count":
					response.UpsertCnt = 0
				case "surplus":
					response.UpsertCnt = 2
				case "wrong ID":
					response.IDs.GetStrId().Data[0] = "wrong"
				case "missing IDs":
					response.IDs = nil
				}
			}
			if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{{ID: "one", Text: "one"}}}); err == nil || service.writes != 1 {
				t.Fatalf("bad acknowledgment became success: %v", err)
			}
		})
	}
}

func TestNativeCapacitiesAndFailuresAreNotOverridden(t *testing.T) {
	store, service := nativeStore(t, entity.IP, nil)
	for _, field := range service.schema.Fields {
		if field.Name == fieldID {
			field.TypeParams[0].Value = "3"
		}
	}
	if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{{ID: "fits", Text: "one"}}}); !errors.Is(err, ErrDocumentIDTooLong) || service.writes != 0 {
		t.Fatalf("native capacity overridden: %v", err)
	}
	if err := store.DeleteIDs(t.Context(), []string{"one", "long"}); err == nil || service.deletions != 0 {
		t.Fatal("invalid late ID deleted prefix")
	}
	if err := store.DeleteIDs(t.Context(), []string{"one", " "}); err == nil || service.deletions != 0 {
		t.Fatal("blank late ID deleted prefix")
	}
	store.documentBatcher = testBatcher{err: errors.New("batcher failed")}
	if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{{ID: "one", Text: "one"}}}); err == nil || service.writes != 0 {
		t.Fatal("batcher failure published data")
	}
	service.nativeError = errors.New("native transport failed")
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "q"})
	if response != nil || err == nil || !strings.Contains(err.Error(), "native transport failed") {
		t.Fatalf("native failure hidden: %v %v", response, err)
	}
}
