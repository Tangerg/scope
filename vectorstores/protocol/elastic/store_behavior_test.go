package elastic_test

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
	corevector "github.com/Tangerg/scope/core/vector"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
	"github.com/Tangerg/scope/vectorstores/protocol/elastic"
	"github.com/Tangerg/scope/vectorstores/protocol/elastic/elastictest"
)

const behaviorSpace vectorstore.SpaceID = "paired-v1"

// behaviorEngine is the smallest engine Store can drive: a declared dimension,
// raw scores already in Core's range and a fake nearest-neighbor clause. Every
// Store behavior below is engine-neutral, so it is proven once against it.
type behaviorEngine struct {
	// uncomparable admits a schema Store cannot compare across re-admissions.
	uncomparable bool
}

type behaviorField struct {
	Dimensions int `json:"dims"`
}

func (behaviorEngine) Name() string { return "behavior" }

func (behaviorEngine) SettingNames() []string { return nil }

func (b behaviorEngine) Admit(embedding jsontext.Value, _ elastic.IndexSettings) (elastic.Schema, error) {
	var field behaviorField
	if err := jsonv2.Unmarshal(embedding, &field); err != nil || field.Dimensions <= 0 {
		return nil, fmt.Errorf("%w: behavior embedding needs a dimension", elastic.ErrIncompatibleIndex)
	}
	schema := behaviorSchema{dimensions: field.Dimensions}
	if b.uncomparable {
		return listSchema{behaviorSchema: schema}, nil
	}
	return schema, nil
}

type listSchema struct {
	behaviorSchema
	_ []string
}

type behaviorSchema struct{ dimensions int }

func (b behaviorSchema) ValidateIndexedVector(vector []float32) error {
	return b.ValidateQueryVector(vector)
}

func (b behaviorSchema) ValidateQueryVector(vector []float32) error {
	if len(vector) != b.dimensions {
		return errors.New("behavior: dimension mismatch")
	}
	return corevector.Coordinates[float32](vector).Validate()
}

func (behaviorSchema) Score(raw float64) (vectorstore.Score, error) {
	if raw < 0 || raw > 1 {
		return 0, fmt.Errorf("behavior: score %g is outside [0, 1]", raw)
	}
	return vectorstore.ScoreFromValue(raw), nil
}

func (behaviorSchema) CheckFilteredSearch() error { return nil }

type behaviorSyntax struct {
	Source bool         `json:"_source"`
	KNN    *behaviorKNN `json:"behavior_knn,omitzero"`
}

// behaviorKNN omits an unrestricted candidate set, as real engines omit their
// ID filter, so an empty set still restricts the search to nothing.
type behaviorKNN struct {
	Vector []float32 `json:"vector"`
	K      int       `json:"k"`
	IDs    []string  `json:"ids,omitzero"`
}

func (behaviorSchema) SearchSyntax(neighbors *elastic.Neighbors) (jsontext.Value, error) {
	syntax := behaviorSyntax{Source: true}
	if neighbors != nil {
		syntax.KNN = &behaviorKNN{Vector: neighbors.Vector, K: neighbors.K, IDs: neighbors.IDs}
	}
	return jsonv2.Marshal(syntax)
}

func behaviorNeighbors(body []byte) ([]string, bool, error) {
	var search behaviorSyntax
	if err := jsonv2.Unmarshal(body, &search); err != nil || search.KNN == nil {
		return nil, false, err
	}
	return search.KNN.IDs, true, nil
}

func newBehaviorIndex(t *testing.T) *elastictest.Index {
	t.Helper()
	return elastictest.NewIndex(t, behaviorSpace, elastictest.Engine{Embedding: behaviorField{Dimensions: 2}, Neighbors: behaviorNeighbors})
}

func behaviorStore(t *testing.T, index *elastictest.Index, maxBulkBytes int) (*elastic.Store, error) {
	t.Helper()
	return elastic.NewStore(t.Context(), elastic.StoreConfig{Engine: behaviorEngine{}, Perform: index.Perform, IndexName: elastictest.IndexName, Space: behaviorSpace, MaxBulkBytes: maxBulkBytes})
}

func mustBehaviorStore(t *testing.T, index *elastictest.Index) *elastic.Store {
	t.Helper()
	store, err := behaviorStore(t, index, 0)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func behaviorBatch(docs []*document.Document) *vectorstore.Batch {
	batch := &vectorstore.Batch{Space: behaviorSpace}
	for _, doc := range docs {
		batch.Records = append(batch.Records, vectorstore.Record{Document: doc, Values: []float64{1, 0}})
	}
	return batch
}

func behaviorBudget() vectorstore.ScanBudget {
	return vectorstore.ScanBudget{MaxRecords: 10_000, MaxBytes: 16 << 20, Timeout: time.Minute}
}

func behaviorQuery(ctx context.Context, store *elastic.Store, query *vectorstore.Query) (*vectorstore.SearchResponse, error) {
	if query.Options.Filter != nil {
		return store.QueryByScan(ctx, query, behaviorBudget())
	}
	return store.Query(ctx, query)
}

func nearest(options vectorstore.SearchOptions) *vectorstore.Query {
	return &vectorstore.Query{Space: behaviorSpace, Values: []float64{1, 0}, Options: options}
}

func TestStoreFilterConformanceThroughNativeMembership(t *testing.T) {
	for _, operation := range []string{"search", "delete"} {
		t.Run(operation, func(t *testing.T) {
			index := newBehaviorIndex(t)
			store := mustBehaviorStore(t, index)
			storetest.FilterConformance(t, storetest.FilterConfig{Query: func(ctx context.Context, docs []*document.Document, predicate filter.Predicate) ([]string, error) {
				clear(index.Rows)
				if err := store.Upsert(ctx, behaviorBatch(docs)); err != nil {
					return nil, err
				}
				if operation == "delete" {
					if err := store.DeleteMatching(ctx, predicate, behaviorBudget()); err != nil {
						return nil, err
					}
					var deleted []string
					for _, doc := range docs {
						if _, present := index.Rows[doc.ID]; !present {
							deleted = append(deleted, doc.ID)
						}
					}
					return deleted, nil
				}
				response, err := behaviorQuery(ctx, store, nearest(vectorstore.SearchOptions{TopK: len(docs), Filter: predicate}))
				if err != nil {
					return nil, err
				}
				var ids []string
				for _, hit := range response.Results {
					ids = append(ids, hit.Document.ID)
				}
				return ids, nil
			}})
		})
	}
}

func TestStoreMetadataRoundTripHasOneOpaqueRepresentation(t *testing.T) {
	index := newBehaviorIndex(t)
	store := mustBehaviorStore(t, index)
	for _, facts := range []metadata.Map{nil, {}, {"content": json.RawMessage(`"value"`), "embedding": json.RawMessage(`{"nested":[1,null]}`), "metadata_json": json.RawMessage(`9007199254740993`), "decimal": json.RawMessage(`1.0000000000000000001`), "huge": json.RawMessage(`1e1000`)}} {
		clear(index.Rows)
		id := " spaced / * ? 中文🙂 "
		if err := store.Upsert(t.Context(), behaviorBatch([]*document.Document{{ID: id, Text: "text", Metadata: facts}})); err != nil {
			t.Fatal(err)
		}
		response, err := behaviorQuery(t.Context(), store, nearest(vectorstore.SearchOptions{}))
		if err != nil {
			t.Fatal(err)
		}
		if len(response.Results) != 1 || response.Results[0].Document.ID != id || !facts.Equal(response.Results[0].Document.Metadata) || (facts == nil) != (response.Results[0].Document.Metadata == nil) {
			t.Fatalf("facts changed: %#v", response)
		}
		if err := store.DeleteIDs(t.Context(), []string{id, id, "unknown"}); err != nil || len(index.Rows) != 0 {
			t.Fatalf("explicit deletion: %v", err)
		}
	}
}

func TestStoreRejectsLateBatchFailuresWithoutPublication(t *testing.T) {
	for _, fault := range []string{"dimension", "overflow", "metadata", "ID limit"} {
		t.Run(fault, func(t *testing.T) {
			index := newBehaviorIndex(t)
			store := mustBehaviorStore(t, index)
			docs := []*document.Document{{ID: "one", Text: "text"}, {ID: "two", Text: "text"}}
			batch := behaviorBatch(docs)
			switch fault {
			case "dimension":
				batch.Records[1].Values = []float64{1}
			case "overflow":
				batch.Records[1].Values = []float64{1e100, 0}
			case "metadata":
				docs[1].Metadata = metadata.Map{"bad": json.RawMessage(`broken`)}
			case "ID limit":
				docs[1].ID = strings.Repeat("x", 513)
			}
			if err := store.Upsert(t.Context(), batch); err == nil || index.Writes != 0 {
				t.Fatalf("failure=%v, native writes=%d", err, index.Writes)
			}
		})
	}
}

func TestStoreRefusesAnIncompatibleRecordLayout(t *testing.T) {
	for _, fault := range []string{"dynamic", "extra property", "routing", "runtime fields", "derived fields", "dynamic templates", "source disabled", "source pruned", "source mode", "metadata indexed", "metadata object", "content keyword", "missing embedding", "foreign space"} {
		t.Run(fault, func(t *testing.T) {
			index := newBehaviorIndex(t)
			mapping := &index.Mapping
			switch fault {
			case "dynamic":
				mapping.Dynamic = "true"
			case "extra property":
				mapping.Properties["shadow"] = elastictest.Field{Type: "keyword"}
			case "routing":
				mapping.Routing.Required = true
			case "runtime fields":
				mapping.Runtime = map[string]any{"shadow": map[string]string{"type": "keyword"}}
			case "derived fields":
				mapping.Derived = map[string]any{"shadow": map[string]string{"type": "keyword"}}
			case "dynamic templates":
				mapping.DynamicTemplates = []any{map[string]any{"strings": map[string]any{"match_mapping_type": "string"}}}
			case "source disabled":
				mapping.Source.Enabled = new(false)
			case "source pruned":
				mapping.Source.Excludes = []string{elastic.EmbeddingField}
			case "source mode":
				mapping.Source.Mode = "synthetic"
			case "metadata indexed":
				mapping.Properties[elastic.MetadataField] = elastictest.Field{Type: "keyword", Index: new(true), DocValues: new(false)}
			case "metadata object":
				mapping.Properties[elastic.MetadataField] = elastictest.Field{Type: "object"}
			case "content keyword":
				mapping.Properties[elastic.ContentField] = elastictest.Field{Type: "keyword"}
			case "missing embedding":
				delete(mapping.Properties, elastic.EmbeddingField)
				mapping.Properties["shadow"] = elastictest.Field{Type: "keyword"}
			case "foreign space":
				mapping.Meta.Space = "other-v1"
			}
			store, err := behaviorStore(t, index, 0)
			if store != nil || err == nil {
				t.Fatalf("incompatible layout accepted: %#v, %v", store, err)
			}
		})
	}
}

func TestStoreConditionalDeletionReportsNativeConflict(t *testing.T) {
	index := newBehaviorIndex(t)
	store := mustBehaviorStore(t, index)
	if err := store.Upsert(t.Context(), behaviorBatch([]*document.Document{{ID: "one", Text: "text"}})); err != nil {
		t.Fatal(err)
	}
	index.BeforeDelete = func() {
		record := index.Rows["one"]
		record.MetadataJSON = `{"value":"new"}`
		index.Rows["one"] = record
	}
	if err := store.DeleteMatching(t.Context(), filter.IsNull("unused"), behaviorBudget()); err == nil || len(index.Rows) != 1 {
		t.Fatalf("stale deletion: %v", err)
	}
	if err := store.DeleteMatching(t.Context(), filter.IsNull("unused"), behaviorBudget()); err != nil || len(index.Rows) != 0 {
		t.Fatalf("current deletion: %v", err)
	}
}

func TestStoreRanksFilteredCandidatesByRawScoreAcrossBoundedGroups(t *testing.T) {
	index := newBehaviorIndex(t)
	store := mustBehaviorStore(t, index)
	docs := make([]*document.Document, 513)
	for i := range docs {
		docs[i] = &document.Document{ID: fmt.Sprintf("%04d", i), Text: "text"}
	}
	if err := store.Upsert(t.Context(), behaviorBatch(docs)); err != nil {
		t.Fatal(err)
	}
	for i, doc := range docs {
		index.Scores[doc.ID] = float64(i) / float64(len(docs))
	}
	response, err := behaviorQuery(t.Context(), store, nearest(vectorstore.SearchOptions{TopK: 1, Filter: filter.IsNull("unused")}))
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 || response.Results[0].Document.ID != "0512" || !slices.Equal(index.Groups, []int{512, 1}) {
		t.Fatalf("raw native rank changed: %#v, groups=%v", response, index.Groups)
	}
}

func TestStoreSearchRejectsDamageWithoutPartialResponse(t *testing.T) {
	for _, fault := range []string{"timeout", "terminated early", "missing shards", "failed shard", "missing total", "missing hits", "missing source", "missing metadata", "shadow metadata", "missing vector", "null coordinate", "missing tokens", "routing", "wrong index", "wrong membership", "repeated ID", "missing score", "bad score", "late corrupt hit"} {
		t.Run(fault, func(t *testing.T) {
			index := newBehaviorIndex(t)
			store := mustBehaviorStore(t, index)
			if err := store.Upsert(t.Context(), behaviorBatch([]*document.Document{{ID: "one", Text: "text"}})); err != nil {
				t.Fatal(err)
			}
			hit := index.Hit("one")
			page := index.Page([]elastic.Hit{hit}, 1, "")
			source := map[string]json.RawMessage{}
			if err := jsonv2.Unmarshal(hit.Source, &source); err != nil {
				t.Fatal(err)
			}
			switch fault {
			case "timeout":
				page.TimedOut = new(true)
			case "terminated early":
				page.TerminatedEarly = new(true)
			case "missing shards":
				page.Shards = nil
			case "failed shard":
				page.Shards.Failed = new(1)
			case "missing total":
				page.Hits.Total = nil
			case "missing hits":
				page.Hits.Hits = nil
			case "missing source":
				hit.Source = nil
			case "missing metadata":
				delete(source, elastic.MetadataField)
			case "shadow metadata":
				source["metadata"] = json.RawMessage(`{}`)
			case "missing vector":
				delete(source, elastic.EmbeddingField)
			case "null coordinate":
				source[elastic.EmbeddingField] = json.RawMessage(`[1,null]`)
			case "missing tokens":
				hit.SeqNo = nil
			case "routing":
				hit.Fields = map[string][]json.RawMessage{"_routing": {json.RawMessage(`"tenant"`)}}
			case "wrong index":
				hit.Index = "other"
			case "wrong membership":
				hit.ID = "outside"
			case "missing score":
				hit.Score = nil
			case "bad score":
				hit.Score = new(float64(3))
			}
			if strings.Contains(fault, "metadata") || fault == "missing vector" || fault == "null coordinate" {
				var err error
				if hit.Source, err = jsonv2.Marshal(source); err != nil {
					t.Fatal(err)
				}
			}
			if page.Hits.Hits != nil {
				*page.Hits.Hits = []elastic.Hit{hit}
			}
			switch fault {
			case "repeated ID":
				*page.Hits.Hits = append(*page.Hits.Hits, hit)
				page.Hits.Total.Value = new(int64(2))
			case "late corrupt hit":
				hit.ID = "two"
				hit.Source = json.RawMessage(`{"content":"text"}`)
				*page.Hits.Hits = append(*page.Hits.Hits, hit)
				page.Hits.Total.Value = new(int64(2))
			}
			index.Reply = page
			options := vectorstore.SearchOptions{TopK: 2}
			if fault == "wrong membership" {
				options.Filter = filter.IsNull("unused")
			}
			if response, err := behaviorQuery(t.Context(), store, nearest(options)); err == nil || response != nil {
				t.Fatalf("damaged output succeeded: %#v, %v", response, err)
			}
		})
	}
}

func TestStoreScanRejectsDamagedSourceAndCursor(t *testing.T) {
	for _, fault := range []string{"missing metadata", "missing content", "shadow source", "missing cursor", "wrong total", "missing shards"} {
		t.Run(fault, func(t *testing.T) {
			index := newBehaviorIndex(t)
			record := elastic.StoredDocument{Content: "text", Embedding: []float32{1, 0}, MetadataJSON: "null"}
			switch fault {
			case "missing metadata":
				record.MetadataJSON = ""
			case "missing content":
				record.Content = ""
			}
			index.Rows["one"] = record
			hit := index.Hit("one")
			if fault == "shadow source" {
				hit.Source = json.RawMessage(`{"content":"text","embedding":[1,0],"metadata_json":"null","metadata":{}}`)
			}
			page := index.Page([]elastic.Hit{hit}, 1, "cursor")
			switch fault {
			case "missing cursor":
				page.ScrollID = ""
			case "wrong total":
				page.Hits.Total.Value = new(int64(2))
			case "missing shards":
				page.Shards = nil
			}
			index.ScanReply = page
			store, err := behaviorStore(t, index, 0)
			if err != nil {
				t.Fatalf("construction enumerated source records: %v", err)
			}
			if response, err := behaviorQuery(t.Context(), store, nearest(vectorstore.SearchOptions{Filter: filter.IsNull("absent")})); err == nil || response != nil {
				t.Fatalf("damaged scan accepted: %#v, %v", response, err)
			}
		})
	}
}

func TestStoreNativeLimitsAndMalformedRecordsFailBeforeRanking(t *testing.T) {
	index := newBehaviorIndex(t)
	index.Settings.Settings["index.max_result_window"] = "1"
	store := mustBehaviorStore(t, index)
	if response, err := behaviorQuery(t.Context(), store, nearest(vectorstore.SearchOptions{TopK: 2})); !errors.Is(err, vectorstore.ErrInvalidOptions) || response != nil {
		t.Fatalf("native window ignored: %#v, %v", response, err)
	}
	if err := store.Upsert(t.Context(), behaviorBatch([]*document.Document{{ID: "one", Text: "text"}})); err != nil {
		t.Fatal(err)
	}
	record := index.Rows["one"]
	record.MetadataJSON = ""
	index.Rows["one"] = record
	if response, err := behaviorQuery(t.Context(), store, nearest(vectorstore.SearchOptions{TopK: 1, Filter: filter.IsNull("absent")})); err == nil || response != nil || len(index.Groups) != 0 {
		t.Fatalf("malformed record reached native ranking: %#v, %v", response, err)
	}
	if err := store.DeleteIDs(t.Context(), []string{"one", ""}); err == nil || len(index.Rows) != 1 {
		t.Fatal("invalid explicit deletion partially published")
	}
}

func TestStoreUnfilteredSearchDoesNotAuditUnreturnedRecords(t *testing.T) {
	index := newBehaviorIndex(t)
	index.Rows["one"] = elastic.StoredDocument{Content: "text", MetadataJSON: "null", Embedding: []float32{1, 0}}
	index.Rows["unrelated"] = elastic.StoredDocument{Content: "text", MetadataJSON: "broken", Embedding: []float32{0, 1}}
	index.Scores["one"], index.Scores["unrelated"] = 1, 0
	store := mustBehaviorStore(t, index)
	response, err := behaviorQuery(t.Context(), store, nearest(vectorstore.SearchOptions{TopK: 1}))
	if err != nil || len(response.Results) != 1 || response.Results[0].Document.ID != "one" {
		t.Fatalf("native query = %v, %v", response, err)
	}
	if index.Cleared != 0 || !slices.Equal(index.Groups, []int{0}) {
		t.Fatal("unfiltered query opened a source cursor or restricted identities")
	}
}

func TestStoreOrdinaryQueryNeverScansAndExplicitScanRequiresCompleteBudget(t *testing.T) {
	index := newBehaviorIndex(t)
	store := mustBehaviorStore(t, index)
	if err := store.Upsert(t.Context(), behaviorBatch([]*document.Document{{ID: "one", Text: "one"}, {ID: "two", Text: "two"}})); err != nil {
		t.Fatal(err)
	}
	query := nearest(vectorstore.SearchOptions{Filter: filter.IsNull("tenant")})
	if response, err := store.Query(t.Context(), query); !errors.Is(err, vectorstore.ErrUnsupportedFilter) || response != nil || index.Cleared != 0 || len(index.Groups) != 0 {
		t.Fatalf("implicit scan: response=%v error=%v", response, err)
	}
	for _, limit := range []string{"records", "bytes"} {
		budget := behaviorBudget()
		if limit == "records" {
			budget.MaxRecords = 1
		} else {
			budget.MaxBytes = 1
		}
		if response, err := store.QueryByScan(t.Context(), query, budget); !errors.Is(err, vectorstore.ErrScanBudget) || response != nil || len(index.Groups) != 0 {
			t.Fatalf("partial %s scan: %v %v", limit, response, err)
		}
	}
}

func TestStoreFilteredScanValidatesTheVectorBeforeEnumeration(t *testing.T) {
	for _, sample := range []struct {
		name   string
		values []float64
	}{
		{"wrong dimension", []float64{1}},
		{"float32 overflow", []float64{1e300, 0}},
	} {
		t.Run(sample.name, func(t *testing.T) {
			index := newBehaviorIndex(t)
			store := mustBehaviorStore(t, index)
			query := &vectorstore.Query{Space: behaviorSpace, Values: sample.values}
			if response, err := store.Query(t.Context(), query); err == nil || response != nil {
				t.Fatalf("native path accepted %s: %v, %v", sample.name, response, err)
			}
			query.Options.Filter = filter.IsNull("tenant")
			if response, err := store.QueryByScan(t.Context(), query, behaviorBudget()); err == nil || response != nil || index.Cleared != 0 {
				t.Fatalf("invalid vector reached a scan: %v, %v, cleared=%d", response, err, index.Cleared)
			}
		})
	}
}

func TestStoreMutationsHonorBulkAdmission(t *testing.T) {
	for _, operation := range []struct {
		name   string
		mutate func(context.Context, *elastic.Store, []*document.Document) error
		wire   func(string) string
	}{
		{
			name: "upsert",
			mutate: func(ctx context.Context, store *elastic.Store, docs []*document.Document) error {
				return store.Upsert(ctx, behaviorBatch(docs))
			},
			wire: func(id string) string {
				return fmt.Sprintf("{\"index\":{\"_index\":\"documents\",\"_id\":%q}}\n{\"content\":\"text\",\"embedding\":[1,0],\"metadata_json\":\"null\"}\n", id)
			},
		},
		{
			name: "delete IDs",
			mutate: func(ctx context.Context, store *elastic.Store, docs []*document.Document) error {
				var ids []string
				for _, doc := range docs {
					ids = append(ids, doc.ID)
				}
				return store.DeleteIDs(ctx, ids)
			},
			wire: func(id string) string {
				return fmt.Sprintf("{\"delete\":{\"_index\":\"documents\",\"_id\":%q}}\n", id)
			},
		},
		{
			name: "conditional delete",
			mutate: func(ctx context.Context, store *elastic.Store, _ []*document.Document) error {
				return store.DeleteMatching(ctx, filter.IsNull("unused"), behaviorBudget())
			},
			wire: func(id string) string {
				return fmt.Sprintf("{\"delete\":{\"_index\":\"documents\",\"_id\":%q,\"if_seq_no\":3,\"if_primary_term\":1}}\n", id)
			},
		},
	} {
		t.Run(operation.name, func(t *testing.T) {
			t.Run("exact bytes and framing", func(t *testing.T) {
				docs := []*document.Document{{ID: "a", Text: "text"}, {ID: "b", Text: "text"}}
				limit := len(operation.wire("a"))
				index, store := newBulkBudgetIndex(t, limit, docs)
				if err := operation.mutate(t.Context(), store, docs); err != nil {
					t.Fatal(err)
				}
				if len(index.BulkBodies) != len(docs) {
					t.Fatalf("expected one complete action per request, got %d requests", len(index.BulkBodies))
				}
				for i, body := range index.BulkBodies {
					if string(body) != operation.wire(docs[i].ID) || len(body) > limit {
						t.Fatalf("request lost framing, order, budget or condition: %s", body)
					}
				}
			})
			t.Run("one byte below action", func(t *testing.T) {
				docs := []*document.Document{{ID: "a", Text: "text"}}
				index, store := newBulkBudgetIndex(t, len(operation.wire("a"))-1, docs)
				err := operation.mutate(t.Context(), store, docs)
				if err == nil || len(index.BulkBodies) != 0 || len(index.Rows) != len(docs) || index.Writes != len(docs) {
					t.Fatalf("oversized action reached publication: requests %d, writes %d, error %v", len(index.BulkBodies), index.Writes, err)
				}
				if _, ok := errors.AsType[*vectorstore.WriteError](err); ok {
					t.Fatal("preflight failure claimed an attempted write")
				}
			})
			t.Run("late oversized action", func(t *testing.T) {
				docs := []*document.Document{{ID: "a", Text: "text"}, {ID: "b", Text: "text"}, {ID: strings.Repeat("z", 100), Text: "text"}}
				index, store := newBulkBudgetIndex(t, len(operation.wire("a")), docs)
				if err := operation.mutate(t.Context(), store, docs); err == nil || len(index.BulkBodies) != 0 || len(index.Rows) != len(docs) || index.Writes != len(docs) {
					t.Fatalf("late oversized action published a prefix: requests %d, writes %d, error %v", len(index.BulkBodies), index.Writes, err)
				}
			})
			t.Run("action count bounds acknowledgment growth", func(t *testing.T) {
				var docs []*document.Document
				for i := range 513 {
					docs = append(docs, &document.Document{ID: fmt.Sprintf("%03d", i), Text: "text"})
				}
				index, store := newBulkBudgetIndex(t, 1<<20, docs)
				if err := operation.mutate(t.Context(), store, docs); err != nil {
					t.Fatal(err)
				}
				var counts []int
				linesPerAction := strings.Count(operation.wire("a"), "\n")
				for _, body := range index.BulkBodies {
					counts = append(counts, bytes.Count(body, []byte{'\n'})/linesPerAction)
				}
				if !slices.Equal(counts, []int{512, 1}) {
					t.Fatalf("unbounded native acknowledgment batch: %v", counts)
				}
			})
		})
	}
}

func TestStoreBulkBudgetRetainsWriteEvidenceAcrossRequests(t *testing.T) {
	index := newBehaviorIndex(t)
	const oneAction = "{\"index\":{\"_index\":\"documents\",\"_id\":\"a\"}}\n{\"content\":\"text\",\"embedding\":[1,0],\"metadata_json\":\"null\"}\n"
	store, err := behaviorStore(t, index, len(oneAction))
	if err != nil {
		t.Fatal(err)
	}
	index.BulkFailureAt = 2
	err = store.Upsert(t.Context(), behaviorBatch([]*document.Document{{ID: "a", Text: "text"}, {ID: "b", Text: "text"}, {ID: "c", Text: "text"}}))
	failure, ok := errors.AsType[*vectorstore.WriteError](err)
	if !ok {
		t.Fatalf("missing per-ID evidence: %v", err)
	}
	outcome := failure.Outcome()
	if !slices.Equal(outcome.Acknowledged, []string{"a"}) || !slices.Equal(outcome.Unconfirmed, []string{"b"}) || !slices.Equal(outcome.Unattempted, []string{"c"}) || len(outcome.Rejected) != 0 {
		t.Fatalf("batch boundaries changed write evidence: %+v", outcome)
	}
	if len(index.BulkBodies) != 2 || len(index.Rows) != 1 {
		t.Fatalf("unexpected publication after failure: requests %d, rows %d", len(index.BulkBodies), len(index.Rows))
	}
}

func newBulkBudgetIndex(t *testing.T, limit int, docs []*document.Document) (*elastictest.Index, *elastic.Store) {
	t.Helper()
	index := newBehaviorIndex(t)
	index.PageSize = 512
	if err := mustBehaviorStore(t, index).Upsert(t.Context(), behaviorBatch(docs)); err != nil {
		t.Fatal(err)
	}
	store, err := behaviorStore(t, index, limit)
	if err != nil {
		t.Fatal(err)
	}
	index.BulkBodies = nil
	return index, store
}

func TestStoreReadmitsTheIndexBeforeEveryMutation(t *testing.T) {
	index := newBehaviorIndex(t)
	store := mustBehaviorStore(t, index)
	index.Mapping.Properties[elastic.EmbeddingField] = behaviorField{Dimensions: 3}
	if err := store.Upsert(t.Context(), behaviorBatch([]*document.Document{{ID: "one", Text: "text"}})); !errors.Is(err, elastic.ErrIncompatibleIndex) || len(index.BulkBodies) != 0 {
		t.Fatalf("a changed index received a write: %v, %d bulk requests", err, len(index.BulkBodies))
	}
}

func TestStoreJoinsItsSearchMembersWithEngineSyntax(t *testing.T) {
	index := newBehaviorIndex(t)
	store := mustBehaviorStore(t, index)
	if response, err := store.Query(t.Context(), nearest(vectorstore.SearchOptions{})); err != nil || len(response.Results) != 0 {
		t.Fatalf("Query = %#v, %v", response, err)
	}
	if len(index.SearchBodies) != 1 {
		t.Fatalf("searches = %d", len(index.SearchBodies))
	}
	var body map[string]jsontext.Value
	if err := jsonv2.Unmarshal(index.SearchBodies[0], &body); err != nil {
		t.Fatal(err)
	}
	for member, want := range map[string]string{
		"size": fmt.Sprint(vectorstore.SearchOptions{}.ResultLimit()), "track_total_hits": `true`, "seq_no_primary_term": `true`, "stored_fields": `["_routing"]`, "_source": `true`,
	} {
		if string(body[member]) != want {
			t.Fatalf("%s = %s, want %s in %s", member, body[member], want, index.SearchBodies[0])
		}
	}
	if _, found := body["behavior_knn"]; !found {
		t.Fatalf("engine syntax missing from %s", index.SearchBodies[0])
	}
}

func TestStoreRequiresAComparableSchema(t *testing.T) {
	index := newBehaviorIndex(t)
	store, err := elastic.NewStore(t.Context(), elastic.StoreConfig{Engine: behaviorEngine{uncomparable: true}, Perform: index.Perform, IndexName: elastictest.IndexName, Space: behaviorSpace})
	if store != nil || err == nil || !strings.Contains(err.Error(), "not comparable") {
		t.Fatalf("NewStore = %v, want rejection of a schema it cannot compare", err)
	}
}

func TestStoreConfigRequiresEveryCollaborator(t *testing.T) {
	index := newBehaviorIndex(t)
	valid := elastic.StoreConfig{Engine: behaviorEngine{}, Perform: index.Perform, IndexName: elastictest.IndexName, Space: behaviorSpace}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*elastic.StoreConfig){
		"engine":         func(c *elastic.StoreConfig) { c.Engine = nil },
		"space":          func(c *elastic.StoreConfig) { c.Space = " " },
		"transport":      func(c *elastic.StoreConfig) { c.Perform = nil },
		"response bound": func(c *elastic.StoreConfig) { c.MaxResponseBytes = -1 },
		"bulk bound":     func(c *elastic.StoreConfig) { c.MaxBulkBytes = -1 },
		"index name":     func(c *elastic.StoreConfig) { c.IndexName = "alias,*" },
	} {
		t.Run(name, func(t *testing.T) {
			config := valid
			change(&config)
			if err := config.Validate(); err == nil {
				t.Fatal("invalid config accepted")
			}
			if store, err := elastic.NewStore(t.Context(), config); store != nil || err == nil {
				t.Fatalf("NewStore accepted an invalid config: %v", err)
			}
		})
	}
}

// rewritten answers through perform, letting change edit the decoded body of
// every response to a path with suffix.
func rewritten(t *testing.T, perform func(*http.Request) (*http.Response, error), suffix string, change func(map[string]jsontext.Value)) func(*http.Request) (*http.Response, error) {
	return func(request *http.Request) (*http.Response, error) {
		response, err := perform(request)
		if err != nil || !strings.HasSuffix(request.URL.Path, suffix) {
			return response, err
		}
		var body map[string]jsontext.Value
		if decodeErr := jsonv2.UnmarshalRead(response.Body, &body); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		change(body)
		encoded, err := jsonv2.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		response.Body = io.NopCloser(bytes.NewReader(encoded))
		response.ContentLength = int64(len(encoded))
		return response, nil
	}
}

func TestStoreAdmissionRefusesAMissingOrAmbiguousIndex(t *testing.T) {
	for _, test := range []struct {
		fault string
		want  error
	}{
		{"missing index", elastic.ErrIndexMissing},
		{"alias", elastic.ErrIncompatibleIndex},
		{"settings of another index", elastic.ErrIncompatibleIndex},
		{"result window", elastic.ErrIncompatibleIndex},
	} {
		t.Run(test.fault, func(t *testing.T) {
			index := newBehaviorIndex(t)
			perform := index.Perform
			switch test.fault {
			case "missing index":
				perform = func(request *http.Request) (*http.Response, error) {
					if strings.HasSuffix(request.URL.Path, "/_mapping") {
						return &http.Response{StatusCode: http.StatusNotFound, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":"index_not_found_exception"}`))}, nil
					}
					return index.Perform(request)
				}
			case "alias":
				perform = rewritten(t, index.Perform, "/_mapping", func(body map[string]jsontext.Value) { body["other"] = body[elastictest.IndexName] })
			case "settings of another index":
				perform = rewritten(t, index.Perform, "/_settings", func(body map[string]jsontext.Value) {
					body["other"] = body[elastictest.IndexName]
					delete(body, elastictest.IndexName)
				})
			case "result window":
				index.Settings.Settings["index.max_result_window"] = "0"
			}
			store, err := elastic.NewStore(t.Context(), elastic.StoreConfig{Engine: behaviorEngine{}, Perform: perform, IndexName: elastictest.IndexName, Space: behaviorSpace})
			if store != nil || !errors.Is(err, test.want) {
				t.Fatalf("NewStore = %v, want %v", err, test.want)
			}
		})
	}
}

func TestStorePreflightChecksAdmitWhatTheOperationsAccept(t *testing.T) {
	index := newBehaviorIndex(t)
	index.Settings.Settings["index.max_result_window"] = "2"
	store := mustBehaviorStore(t, index)
	ctx := t.Context()
	valid := []*document.Document{{ID: "one", Text: "text"}}
	if err := store.CheckDocuments(ctx, behaviorSpace, valid); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckQuery(ctx, behaviorSpace, vectorstore.SearchOptions{TopK: 2}); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckDocuments(ctx, "other-v1", valid); !errors.Is(err, vectorstore.ErrIncompatibleSpace) {
		t.Fatalf("foreign space admitted documents: %v", err)
	}
	if err := store.CheckQuery(ctx, "other-v1", vectorstore.SearchOptions{}); !errors.Is(err, vectorstore.ErrIncompatibleSpace) {
		t.Fatalf("foreign space admitted a query: %v", err)
	}
	for name, docs := range map[string][]*document.Document{
		"missing ID": {{Text: "text"}},
		"ID limit":   {{ID: strings.Repeat("x", 513), Text: "text"}},
	} {
		if err := store.CheckDocuments(ctx, behaviorSpace, docs); err == nil {
			t.Fatalf("%s admitted", name)
		}
	}
	for name, test := range map[string]struct {
		options vectorstore.SearchOptions
		want    error
	}{
		"hybrid":       {vectorstore.SearchOptions{Mode: vectorstore.SearchModeHybrid}, nil},
		"filter":       {vectorstore.SearchOptions{TopK: 1, Filter: filter.IsNull("tenant")}, vectorstore.ErrUnsupportedFilter},
		"above window": {vectorstore.SearchOptions{TopK: 3}, vectorstore.ErrInvalidOptions},
	} {
		if err := store.CheckQuery(ctx, behaviorSpace, test.options); err == nil || test.want != nil && !errors.Is(err, test.want) {
			t.Fatalf("%s: CheckQuery = %v, want %v", name, err, test.want)
		}
	}
	index.Mapping.Properties[elastic.EmbeddingField] = behaviorField{Dimensions: 3}
	if err := store.CheckDocuments(ctx, behaviorSpace, valid); !errors.Is(err, elastic.ErrIncompatibleIndex) {
		t.Fatalf("changed index admitted documents: %v", err)
	}
	if err := store.CheckQuery(ctx, behaviorSpace, vectorstore.SearchOptions{TopK: 1}); !errors.Is(err, elastic.ErrIncompatibleIndex) {
		t.Fatalf("changed index admitted a query: %v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := store.CheckDocuments(canceled, behaviorSpace, valid); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled preflight = %v", err)
	}
	if err := store.CheckQuery(canceled, behaviorSpace, vectorstore.SearchOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled preflight = %v", err)
	}
}

func TestStoreScanOperationsRequireTheirPredicateAndBudget(t *testing.T) {
	index := newBehaviorIndex(t)
	store := mustBehaviorStore(t, index)
	ctx := t.Context()
	if response, err := store.QueryByScan(ctx, nearest(vectorstore.SearchOptions{}), behaviorBudget()); !errors.Is(err, vectorstore.ErrMissingFilter) || response != nil {
		t.Fatalf("scan without a predicate = %v, %v", response, err)
	}
	if response, err := store.QueryByScan(ctx, nearest(vectorstore.SearchOptions{Filter: filter.IsNull("tenant")}), vectorstore.ScanBudget{}); !errors.Is(err, vectorstore.ErrInvalidOptions) || response != nil {
		t.Fatalf("scan without a budget = %v, %v", response, err)
	}
	if err := store.DeleteMatching(ctx, nil, behaviorBudget()); !errors.Is(err, vectorstore.ErrMissingFilter) {
		t.Fatalf("deletion without a predicate = %v", err)
	}
	if err := store.DeleteMatching(ctx, filter.IsNull("tenant"), vectorstore.ScanBudget{}); !errors.Is(err, vectorstore.ErrInvalidOptions) {
		t.Fatalf("deletion without a budget = %v", err)
	}
	if err := store.DeleteIDs(ctx, nil); err != nil || len(index.BulkBodies) != 0 {
		t.Fatalf("empty deletion = %v, %d bulk requests", err, len(index.BulkBodies))
	}
	if len(index.SearchBodies) != 0 {
		t.Fatal("a rejected operation reached the index")
	}
}
