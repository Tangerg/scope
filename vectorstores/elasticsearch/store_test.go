package elasticsearch

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
	"time"

	"github.com/Tangerg/scope/vectorstores/protocol/elastic"

	"github.com/elastic/elastic-transport-go/v8/elastictransport"
	elasticsearchsdk "github.com/elastic/go-elasticsearch/v9"

	"github.com/Tangerg/scope/core/document"

	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
)

const fixtureSpace vectorstore.SpaceID = "paired-v1"

func fixtureVector(_ string) []float64 { return []float64{1, 0} }
func fixtureBatch(docs []*document.Document) *vectorstore.Batch {
	b := &vectorstore.Batch{Space: fixtureSpace}
	for _, doc := range docs {
		b.Records = append(b.Records, vectorstore.Record{Document: doc, Values: fixtureVector(doc.Text)})
	}
	return b
}
func fixtureBudget() vectorstore.ScanBudget {
	return vectorstore.ScanBudget{MaxRecords: 10000, MaxBytes: 16 << 20, Timeout: time.Minute}
}
func fixtureQuery(ctx context.Context, s *Store, q *vectorstore.Query) (*vectorstore.SearchResponse, error) {
	if q.Options.Filter != nil {
		return s.QueryByScan(ctx, q, fixtureBudget())
	}
	return s.Query(ctx, q)
}

type protocolFixture struct {
	t             *testing.T
	client        *elasticsearchsdk.Client
	mapping       nativeMapping
	settings      nativeSettings
	rows          map[string]storedDocument
	snapshot      []elastic.Hit
	position      int
	pageSize      int
	writes        int
	cleared       int
	groups        []int
	bulkBodies    [][]byte
	bulkFailureAt int
	scores        map[string]float64
	reply         any
	beforeSearch  func()
	beforeDelete  func()
}

func newProtocolFixture(t *testing.T) *protocolFixture {
	t.Helper()
	fixture := &protocolFixture{t: t, pageSize: 2, rows: make(map[string]storedDocument), scores: make(map[string]float64), mapping: nativeMapping{Dynamic: "strict", Properties: map[string]mappedField{contentField: {Type: "text"}, metadataField: {Type: "keyword", Index: new(false), DocValues: new(false)}, embeddingField: {Type: "dense_vector", Dimensions: 2, Similarity: "cosine"}}}, settings: nativeSettings{Settings: map[string]string{}, Defaults: map[string]string{"index.mapping.source.mode": "STORED", "index.default_pipeline": "_none", "index.final_pipeline": "_none", "index.max_result_window": "10000"}}}
	fixture.mapping.Meta.Space = string(fixtureSpace)
	server := httptest.NewServer(fixture)
	t.Cleanup(server.Close)
	client, err := elasticsearchsdk.New(elasticsearchsdk.WithAddresses(server.URL), elasticsearchsdk.WithTransportOptions(elastictransport.WithTransport(server.Client().Transport), elastictransport.WithDisableRetry()))
	if err != nil {
		t.Fatal(err)
	}
	fixture.client = client
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
		defer cancel()
		if err := client.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return fixture
}

func (p *protocolFixture) store() *Store {
	p.t.Helper()
	store, err := NewStore(p.t.Context(), StoreConfig{Client: p.client, IndexName: "documents", Space: fixtureSpace})
	if err != nil {
		p.t.Fatal(err)
	}
	return store
}

func (p *protocolFixture) hit(id string) elastic.Hit {
	source, err := jsonv2.Marshal(p.rows[id])
	if err != nil {
		p.t.Error(err)
	}
	score := p.scores[id]
	if score == 0 {
		score = 0.8
	}
	return elastic.Hit{Index: "documents", ID: id, SeqNo: new(int64(3)), PrimaryTerm: new(int64(1)), Score: &score, Source: source}
}

func (p *protocolFixture) page(hits []elastic.Hit, total int, cursor string) elastic.SearchResponse {
	if hits == nil {
		hits = []elastic.Hit{}
	}
	return elastic.SearchResponse{ScrollID: cursor, TimedOut: new(false), Shards: &elastic.SearchShards{Total: new(1), Successful: new(1), Failed: new(0)}, Hits: &elastic.SearchHits{Hits: &hits, Total: &elastic.SearchTotal{Value: new(int64(total)), Relation: "eq"}}}
}

func (p *protocolFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Elastic-Product", "Elasticsearch")
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
		p.bulkBodies = append(p.bulkBodies, body)
		if len(p.bulkBodies) == p.bulkFailureAt {
			w.WriteHeader(http.StatusServiceUnavailable)
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
		if request.KNN == nil {
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
			var hits []elastic.Hit
			var ids []string
			if request.KNN.Filter != nil {
				ids = request.KNN.Filter.IDs.Values
			}
			p.groups = append(p.groups, len(ids))
			for id := range p.rows {
				if ids == nil || slices.Contains(ids, id) {
					hits = append(hits, p.hit(id))
				}
			}
			slices.SortFunc(hits, func(left, right elastic.Hit) int {
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
			store := fixture.store()
			storetest.FilterConformance(t, storetest.FilterConfig{Query: func(ctx context.Context, docs []*document.Document, predicate filter.Predicate) ([]string, error) {
				clear(fixture.rows)
				if err := store.Upsert(ctx, fixtureBatch(docs)); err != nil {
					return nil, err
				}
				if operation == "delete" {
					if err := store.DeleteMatching(ctx, predicate, fixtureBudget()); err != nil {
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
				response, err := fixtureQuery(ctx, store, &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("query"), Options: vectorstore.SearchOptions{TopK: len(docs), Filter: predicate}})
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
	store := fixture.store()
	for _, facts := range []metadata.Map{nil, {}, {"content": json.RawMessage(`"value"`), "embedding": json.RawMessage(`{"nested":[1,null]}`), "metadata_json": json.RawMessage(`9007199254740993`), "decimal": json.RawMessage(`1.0000000000000000001`), "huge": json.RawMessage(`1e1000`)}} {
		clear(fixture.rows)
		id := " spaced / * ? 中文🙂 "
		if err := store.Upsert(t.Context(), fixtureBatch([]*document.Document{{ID: id, Text: "text", Metadata: facts}})); err != nil {
			t.Fatal(err)
		}
		response, err := fixtureQuery(t.Context(), store, &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("query")})
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
	for _, fault := range []string{"dimension", "overflow", "magnitude", "cosine underflow", "dot magnitude", "metadata", "ID limit"} {
		t.Run(fault, func(t *testing.T) {
			fixture := newProtocolFixture(t)
			if fault == "dot magnitude" {
				field := fixture.mapping.Properties[embeddingField]
				field.Similarity = "dot_product"
				fixture.mapping.Properties[embeddingField] = field
			}
			vector := []float64{1, 0}
			switch fault {
			case "dimension":
				vector = []float64{1}
			case "overflow":
				vector = []float64{1e100, 0}
			case "magnitude":
				vector = []float64{1e20, 0}
			case "cosine underflow":
				vector = []float64{1e-30, 0}
			case "dot magnitude":
				vector = []float64{2, 0}
			}
			store := fixture.store()
			docs := []*document.Document{{ID: "one", Text: "text"}, {ID: "two", Text: "text"}}
			if fault == "metadata" {
				docs[1].Metadata = metadata.Map{"bad": json.RawMessage(`broken`)}
			}
			if fault == "ID limit" {
				docs[1].ID = strings.Repeat("x", 513)
			}
			batch := fixtureBatch(docs)
			batch.Records[1].Values = vector
			err := store.Upsert(t.Context(), batch)
			if err == nil || fixture.writes != 0 {
				t.Fatalf("failure=%v, native writes=%d", err, fixture.writes)
			}
		})
	}
}

func TestNativePolicyHasNoLocalCompetitor(t *testing.T) {
	for _, metric := range []string{"cosine", "l2_norm", "dot_product", "max_inner_product"} {
		t.Run(metric, func(t *testing.T) {
			fixture := newProtocolFixture(t)
			field := fixture.mapping.Properties[embeddingField]
			field.Similarity = metric
			fixture.mapping.Properties[embeddingField] = field
			store := fixture.store()
			if store.schema.similarity != nativeSimilarity(metric) || store.schema.dimensions != 2 {
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
				field.ElementType = "byte"
				fixture.mapping.Properties[embeddingField] = field
			case "no dimension":
				field := fixture.mapping.Properties[embeddingField]
				field.Dimensions = 0
				fixture.mapping.Properties[embeddingField] = field
			case "pipeline":
				fixture.settings.Settings["index.final_pipeline"] = "rewrite"
			case "source mode":
				fixture.settings.Settings["index.mapping.source.mode"] = "synthetic"
			}
			store, err := NewStore(t.Context(), StoreConfig{Client: fixture.client, IndexName: "documents", Space: fixtureSpace})
			if store != nil || !errors.Is(err, ErrIncompatibleIndex) {
				t.Fatalf("incompatible policy accepted: %#v, %v", store, err)
			}
		})
	}
}

func TestConditionalDeletionReportsNativeConflict(t *testing.T) {
	fixture := newProtocolFixture(t)
	store := fixture.store()
	if err := store.Upsert(t.Context(), fixtureBatch([]*document.Document{{ID: "one", Text: "text"}})); err != nil {
		t.Fatal(err)
	}
	fixture.beforeDelete = func() {
		record := fixture.rows["one"]
		record.MetadataJSON = `{"value":"new"}`
		fixture.rows["one"] = record
	}
	if err := store.DeleteMatching(t.Context(), filter.IsNull("unused"), fixtureBudget()); err == nil || len(fixture.rows) != 1 {
		t.Fatalf("stale deletion: %v", err)
	}
	if err := store.DeleteMatching(t.Context(), filter.IsNull("unused"), fixtureBudget()); err != nil || len(fixture.rows) != 0 {
		t.Fatalf("current deletion: %v", err)
	}
}

func TestNativeInnerProductScoreAndRawRanking(t *testing.T) {
	schema := nativeSchema{similarity: "max_inner_product"}
	for _, sample := range []struct{ raw, want float64 }{{0.5, 0.2689414213699951}, {1, 0.5}, {2, 0.7310585786300049}} {
		score, err := schema.score(sample.raw)
		if err != nil || math.Abs(float64(score)-sample.want) > 1e-15 {
			t.Fatalf("score(%g)=%g, %v", sample.raw, score, err)
		}
	}
	fixture := newProtocolFixture(t)
	field := fixture.mapping.Properties[embeddingField]
	field.Similarity = "max_inner_product"
	fixture.mapping.Properties[embeddingField] = field
	store := fixture.store()
	docs := make([]*document.Document, 513)
	for i := range docs {
		docs[i] = &document.Document{ID: fmt.Sprintf("%04d", i), Text: "text"}
	}
	if err := store.Upsert(t.Context(), fixtureBatch(docs)); err != nil {
		t.Fatal(err)
	}
	for i, doc := range docs {
		fixture.scores[doc.ID] = 1e30 + float64(i)*1e29
	}
	response, err := fixtureQuery(t.Context(), store, &vectorstore.Query{Space: fixtureSpace, Values: fixtureVector("query"), Options: vectorstore.SearchOptions{TopK: 1, Filter: filter.IsNull("unused")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 || response.Results[0].Document.ID != "0512" || !slices.Equal(fixture.groups, []int{512, 1}) {
		t.Fatalf("raw native rank changed: %#v, groups=%v", response, fixture.groups)
	}
}

func fixtureProtocol(t *testing.T, client APIClient, limit int64) *elastic.Client {
	t.Helper()
	protocol, err := elastic.NewClient(elastic.ClientConfig{Perform: client.Perform, MaxResponseBytes: limit})
	if err != nil {
		t.Fatal(err)
	}
	return protocol
}
