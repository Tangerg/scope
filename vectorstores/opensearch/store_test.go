package opensearch

import (
	"bytes"
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	opensearchsdk "github.com/opensearch-project/opensearch-go/v4"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
)

type testBatcher struct{ size int }

func (t testBatcher) Batch(_ context.Context, docs []*document.Document) ([][]*document.Document, error) {
	return slices.Collect(slices.Chunk(docs, max(t.size, 1))), nil
}

func constantModel() embedding.Model {
	return embedding.ModelFunc(func(_ context.Context, request *embedding.Request) (*embedding.Response, error) {
		outputs := make([]*embedding.Output, len(request.Texts))
		for i := range outputs {
			outputs[i] = &embedding.Output{Embedding: []float64{1, 0}}
		}
		return embedding.NewResponse(outputs, nil)
	})
}

type protocolFixture struct {
	t            *testing.T
	client       *opensearchsdk.Client
	mapping      nativeMapping
	settings     nativeSettings
	rows         map[string]storedDocument
	snapshot     []searchHit
	position     int
	pageSize     int
	writes       int
	cleared      int
	groups       []int
	scores       map[string]float64
	reply        any
	scanReply    any
	beforeSearch func()
	beforeDelete func()
}

func newProtocolFixture(t *testing.T) *protocolFixture {
	t.Helper()
	fixture := &protocolFixture{t: t, pageSize: 2, rows: make(map[string]storedDocument), scores: make(map[string]float64), mapping: nativeMapping{Dynamic: "strict", Properties: map[string]mappedField{contentField: {Type: "text"}, metadataField: {Type: "keyword", Index: new(false), DocValues: new(false)}, embeddingField: {Type: "knn_vector", Dimensions: 2, Method: &nativeMethod{Name: "hnsw", Engine: "lucene", SpaceType: "cosinesimil"}}}}, settings: nativeSettings{Settings: map[string]string{}, Defaults: map[string]string{"index.knn": "true", "index.derived_source.enabled": "false", "index.knn.derived_source.enabled": "false", "index.search.default_pipeline": "_none", "index.default_pipeline": "_none", "index.final_pipeline": "_none", "index.max_result_window": "10000"}}}
	server := httptest.NewServer(fixture)
	t.Cleanup(server.Close)
	client, err := opensearchsdk.NewClient(opensearchsdk.Config{Addresses: []string{server.URL}, Transport: server.Client().Transport, DisableRetry: true})
	if err != nil {
		t.Fatal(err)
	}
	fixture.client = client
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Error(err)
		}
	})

	return fixture
}

func (p *protocolFixture) store(model embedding.Model, size int) *Store {
	p.t.Helper()
	store, err := NewStore(p.t.Context(), StoreConfig{Client: p.client, IndexName: "documents", EmbeddingModel: model, DocumentBatcher: testBatcher{size: size}})
	if err != nil {
		p.t.Fatal(err)
	}
	return store
}

func (p *protocolFixture) hit(id string) searchHit {
	source, err := jsonv2.Marshal(p.rows[id])
	if err != nil {
		p.t.Error(err)
	}
	score := p.scores[id]
	if score == 0 {
		score = 0.8
	}
	return searchHit{Index: "documents", ID: id, SeqNo: new(int64(3)), PrimaryTerm: new(int64(1)), Score: &score, Source: source}
}

func (p *protocolFixture) page(hits []searchHit, total int, cursor string) searchResponse {
	if hits == nil {
		hits = []searchHit{}
	}
	return searchResponse{ScrollID: cursor, TimedOut: new(false), Shards: &searchShards{Total: new(1), Successful: new(1), Failed: new(0)}, Hits: &searchHits{Hits: &hits, Total: &searchTotal{Value: new(int64(total)), Relation: "eq"}}}
}

func (p *protocolFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	var response any
	switch {
	case strings.HasSuffix(r.URL.Path, "/_mapping"):
		response = map[string]mappedIndex{"documents": {Mappings: p.mapping}}
	case strings.HasSuffix(r.URL.Path, "/_settings"):
		response = map[string]nativeSettings{"documents": p.settings}
	case r.Method == http.MethodDelete && r.URL.Path == "/_search/scroll":
		p.cleared++
		response = map[string]any{"succeeded": true, "num_freed": 1}
	case r.URL.Path == "/_bulk":
		body, err := io.ReadAll(r.Body)
		if err != nil {
			p.t.Error(err)
			w.WriteHeader(500)
			return
		}
		lines := bytes.Split(bytes.TrimSuffix(body, []byte{'\n'}), []byte{'\n'})
		result := bulkResponse{Errors: new(false), Items: []map[bulkOperation]bulkItemResult{}}
		for i := 0; i < len(lines); i++ {
			var action bulkAction
			if err := jsonv2.Unmarshal(lines[i], &action); err != nil {
				p.t.Error(err)
				w.WriteHeader(500)
				return
			}
			operation, target, status := bulkOperationDelete, action.Delete, 200
			if action.Index != nil {
				operation, target, status = bulkOperationIndex, action.Index, 201
				i++
				var record storedDocument
				if i >= len(lines) {
					p.t.Error("missing native document line")
					w.WriteHeader(500)
					return
				}
				if err := jsonv2.Unmarshal(lines[i], &record, jsonv2.RejectUnknownMembers(true)); err != nil {
					p.t.Error(err)
					w.WriteHeader(500)
					return
				}
				p.rows[target.ID] = record
				p.writes++
			} else {
				if p.beforeDelete != nil {
					p.beforeDelete()
					p.beforeDelete = nil
					status = 409
					*result.Errors = true
				}
				if status != 409 {
					if _, exists := p.rows[target.ID]; !exists {
						status = 404
					}
					delete(p.rows, target.ID)
				}
			}
			result.Items = append(result.Items, map[bulkOperation]bulkItemResult{operation: {Index: target.Index, ID: target.ID, Status: &status}})
		}
		response = result
	case r.URL.Path == "/_search/scroll":
		end := min(p.position+p.pageSize, len(p.snapshot))
		response = p.page(p.snapshot[p.position:end], len(p.snapshot), "cursor")
		p.position = end
	case strings.HasSuffix(r.URL.Path, "/_search"):
		var request searchRequest
		if err := jsonv2.UnmarshalRead(r.Body, &request); err != nil {
			p.t.Error(err)
			w.WriteHeader(500)
			return
		}
		if request.Query == nil {
			if p.scanReply != nil {
				response = p.scanReply
				break
			}
			p.snapshot = nil
			for _, id := range slices.Sorted(maps.Keys(p.rows)) {
				p.snapshot = append(p.snapshot, p.hit(id))
			}
			end := min(p.pageSize, len(p.snapshot))
			response = p.page(p.snapshot[:end], len(p.snapshot), "cursor")
			p.position = end
		} else {
			if p.beforeSearch != nil {
				p.beforeSearch()
				p.beforeSearch = nil
			}
			if p.reply != nil {
				response = p.reply
				break
			}
			var hits []searchHit
			var ids []string
			if request.Query.KNN[embeddingField].Filter != nil {
				ids = request.Query.KNN[embeddingField].Filter.IDs.Values
			}
			p.groups = append(p.groups, len(ids))
			for id := range p.rows {
				if ids == nil || slices.Contains(ids, id) {
					hits = append(hits, p.hit(id))
				}
			}
			slices.SortFunc(hits, func(left, right searchHit) int {
				if *left.Score > *right.Score {
					return -1
				}
				if *left.Score < *right.Score {
					return 1
				}
				return strings.Compare(left.ID, right.ID)
			})
			hits = hits[:min(len(hits), request.Size)]
			response = p.page(hits, len(hits), "")
		}
	default:
		p.t.Errorf("unexpected native request %s %s", r.Method, r.URL)
		w.WriteHeader(500)
		return
	}
	if err := jsonv2.MarshalWrite(w, response); err != nil {
		p.t.Error(err)
	}
}

func TestCoreFilterConformanceThroughNativeMembership(t *testing.T) {
	for _, operation := range []string{"search", "delete"} {
		t.Run(operation, func(t *testing.T) {
			fixture := newProtocolFixture(t)
			store := fixture.store(constantModel(), 32)
			storetest.FilterConformance(t, storetest.FilterConfig{Query: func(ctx context.Context, docs []*document.Document, predicate filter.Predicate) ([]string, error) {
				clear(fixture.rows)
				if err := store.Index(ctx, &vectorstore.IndexRequest{Documents: docs}); err != nil {
					return nil, err
				}
				if operation == "delete" {
					if err := store.DeleteWhere(ctx, predicate); err != nil {
						return nil, err
					}
					var deleted []string
					for _, doc := range docs {
						if _, present := fixture.rows[doc.ID]; !present {
							deleted = append(deleted, doc.ID)
						}
					}
					return deleted, nil
				}
				response, err := store.Search(ctx, &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: len(docs), Filter: predicate}})
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

func TestMetadataRoundTripHasOneOpaqueRepresentation(t *testing.T) {
	fixture := newProtocolFixture(t)
	store := fixture.store(constantModel(), 32)
	for _, facts := range []metadata.Map{nil, {}, {"content": json.RawMessage(`"value"`), "embedding": json.RawMessage(`{"nested":[1,null]}`), "metadata_json": json.RawMessage(`9007199254740993`), "decimal": json.RawMessage(`1.0000000000000000001`), "huge": json.RawMessage(`1e1000`)}} {
		clear(fixture.rows)
		id := " spaced / * ? 中文🙂 "
		if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{{ID: id, Text: "text", Metadata: facts}}}); err != nil {
			t.Fatal(err)
		}
		response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query"})
		if err != nil {
			t.Fatal(err)
		}
		if len(response.Results) != 1 || response.Results[0].Document.ID != id || !facts.Equal(response.Results[0].Document.Metadata) || (facts == nil) != (response.Results[0].Document.Metadata == nil) {
			t.Fatalf("facts changed: %#v", response)
		}
		if err := store.DeleteIDs(t.Context(), []string{id, id, "unknown"}); err != nil || len(fixture.rows) != 0 {
			t.Fatalf("explicit deletion: %v", err)
		}
	}
}

func TestWholePreparationRejectsLateFailuresWithoutPublication(t *testing.T) {
	for _, fault := range []string{"late model", "dimension", "overflow", "zero vector", "FP16", "metadata", "ID limit"} {
		t.Run(fault, func(t *testing.T) {
			fixture := newProtocolFixture(t)

			if fault == "FP16" {
				field := fixture.mapping.Properties[embeddingField]
				field.Method.Engine = "faiss"
				field.Method.SpaceType = "l2"
				field.Method.Parameters.Encoder = &nativeEncoder{Name: "sq"}
				fixture.mapping.Properties[embeddingField] = field
			}
			calls := 0
			cause := errors.New("late model failure")
			model := embedding.ModelFunc(func(context.Context, *embedding.Request) (*embedding.Response, error) {
				calls++
				if calls == 2 && fault == "late model" {
					return nil, cause
				}
				vector := []float64{1, 0}
				if calls == 2 {
					switch fault {
					case "dimension":
						vector = []float64{1}
					case "overflow":
						vector = []float64{1e100, 0}
					case "FP16":
						vector = []float64{65505, 0}
					case "zero vector":
						vector = []float64{1e-50, 0}
					}
				}
				return embedding.NewResponse([]*embedding.Output{{Embedding: vector}}, nil)
			})
			store := fixture.store(model, 1)
			docs := []*document.Document{{ID: "one", Text: "text"}, {ID: "two", Text: "text"}}
			if fault == "metadata" {
				docs[1].Metadata = metadata.Map{"bad": json.RawMessage(`broken`)}
			}
			if fault == "ID limit" {
				docs[1].ID = strings.Repeat("x", 513)
			}
			err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: docs})
			if err == nil || fixture.writes != 0 {
				t.Fatalf("failure=%v, native writes=%d", err, fixture.writes)
			}
			if fault == "late model" && !errors.Is(err, cause) {
				t.Fatal("lost model cause")
			}
			if (fault == "metadata" || fault == "ID limit") && calls != 0 {
				t.Fatal("invalid document reached the model")
			}
		})
	}
}

func TestNativePolicyHasNoLocalCompetitor(t *testing.T) {
	for _, metric := range []string{"cosinesimil", "l2", "innerproduct", "l1", "linf"} {
		t.Run(metric, func(t *testing.T) {
			fixture := newProtocolFixture(t)
			field := fixture.mapping.Properties[embeddingField]
			field.Method.SpaceType = metric
			if metric == "l1" || metric == "linf" {
				field.Method.Engine = "nmslib"
			}
			fixture.mapping.Properties[embeddingField] = field
			store := fixture.store(constantModel(), 1)
			if store.schema.similarity != metric || store.schema.dimensions != 2 {
				t.Fatalf("schema=%#v", store.schema)
			}
		})
	}
	for _, fault := range []string{"dynamic", "extra property", "routing", "source disabled", "source pruned", "metadata indexed", "metadata object", "byte vectors", "no dimension", "pipeline", "source mode"} {
		t.Run(fault, func(t *testing.T) {
			fixture := newProtocolFixture(t)
			switch fault {
			case "dynamic":
				fixture.mapping.Dynamic = "true"
			case "extra property":
				fixture.mapping.Properties["shadow"] = mappedField{Type: "keyword"}
			case "routing":
				fixture.mapping.Routing.Required = true
			case "source disabled":
				fixture.mapping.Source.Enabled = new(false)
			case "source pruned":
				fixture.mapping.Source.Excludes = []string{embeddingField}
			case "metadata indexed":
				field := fixture.mapping.Properties[metadataField]
				field.Index = new(true)
				fixture.mapping.Properties[metadataField] = field
			case "metadata object":
				fixture.mapping.Properties[metadataField] = mappedField{Type: "object"}
			case "byte vectors":
				field := fixture.mapping.Properties[embeddingField]
				field.DataType = "byte"
				fixture.mapping.Properties[embeddingField] = field
			case "no dimension":
				field := fixture.mapping.Properties[embeddingField]
				field.Dimensions = 0
				fixture.mapping.Properties[embeddingField] = field
			case "pipeline":
				fixture.settings.Settings["index.final_pipeline"] = "rewrite"
			case "source mode":
				fixture.settings.Settings["index.derived_source.enabled"] = "true"
			}
			store, err := NewStore(t.Context(), StoreConfig{Client: fixture.client, IndexName: "documents", EmbeddingModel: constantModel(), DocumentBatcher: testBatcher{size: 1}})
			if store != nil || !errors.Is(err, ErrIncompatibleIndex) {
				t.Fatalf("incompatible policy accepted: %#v, %v", store, err)
			}
		})
	}
}

func TestConditionalDeletionReportsNativeConflict(t *testing.T) {
	fixture := newProtocolFixture(t)
	store := fixture.store(constantModel(), 1)
	if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{{ID: "one", Text: "text"}}}); err != nil {
		t.Fatal(err)
	}
	fixture.beforeDelete = func() {
		record := fixture.rows["one"]
		record.MetadataJSON = `{"value":"new"}`
		fixture.rows["one"] = record
	}
	if err := store.DeleteWhere(t.Context(), filter.IsNull("unused")); err == nil || len(fixture.rows) != 1 {
		t.Fatalf("stale deletion: %v", err)
	}
	if err := store.DeleteWhere(t.Context(), filter.IsNull("unused")); err != nil || len(fixture.rows) != 0 {
		t.Fatalf("current deletion: %v", err)
	}
}

func TestNativeInnerProductScoreAndRawRanking(t *testing.T) {
	schema := nativeSchema{similarity: "innerproduct"}
	for _, sample := range []struct{ raw, want float64 }{{0.5, 0.2689414213699951}, {1, 0.5}, {2, 0.7310585786300049}} {
		score, err := schema.score(sample.raw)
		if err != nil || math.Abs(float64(score)-sample.want) > 1e-15 {
			t.Fatalf("score(%g)=%g, %v", sample.raw, score, err)
		}
	}
	fixture := newProtocolFixture(t)
	field := fixture.mapping.Properties[embeddingField]
	field.Method.SpaceType = "innerproduct"
	fixture.mapping.Properties[embeddingField] = field
	store := fixture.store(constantModel(), 32)
	docs := make([]*document.Document, 513)
	for i := range docs {
		docs[i] = &document.Document{ID: fmt.Sprintf("%04d", i), Text: "text"}
	}
	if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: docs}); err != nil {
		t.Fatal(err)
	}
	for i, doc := range docs {
		fixture.scores[doc.ID] = 1e30 + float64(i)*1e29
	}
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 1, Filter: filter.IsNull("unused")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 || response.Results[0].Document.ID != "0512" || !slices.Equal(fixture.groups, []int{512, 1}) {
		t.Fatalf("raw native rank changed: %#v, groups=%v", response, fixture.groups)
	}
}
