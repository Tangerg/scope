package azurecosmos

import (
	"cmp"
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
)

const testSpace vectorstore.SpaceID = "test-encoder-v1"

func testPartition() string { return testSpace.StorageKey() + ":library" }
func testBatch(docs []*document.Document) *vectorstore.Batch {
	b := &vectorstore.Batch{Space: testSpace}
	for _, d := range docs {
		b.Records = append(b.Records, vectorstore.Record{Document: d, Values: []float64{1, 0}})
	}
	return b
}
func testScanRequest(p filter.Predicate) vectorstore.ScanRequest {
	return vectorstore.ScanRequest{Filter: p, Budget: testScanBudget()}
}

func testScanBudget() vectorstore.ScanBudget {
	return vectorstore.ScanBudget{MaxRecords: 10000, MaxBytes: 64 << 20, Timeout: 5 * time.Second}
}
func scanIDs(ctx context.Context, s *Store, p filter.Predicate) ([]string, error) {
	docs, err := s.Scan(ctx, testScanRequest(p))
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(docs))
	for i, d := range docs {
		ids[i] = d.ID
	}
	return ids, nil
}

type queryWire struct {
	Query      string                    `json:"query"`
	Parameters []azcosmos.QueryParameter `json:"parameters"`
}
type queryPage struct {
	items  []json.RawMessage
	token  string
	status int
}

type protocolFixture struct {
	t          *testing.T
	mu         sync.Mutex
	server     *httptest.Server
	container  *azcosmos.ContainerClient
	properties map[string]any
	records    map[string]json.RawMessage
	scores     map[string]float64
	pageSize   int
	partition  string
	writes     int
	deletes    []string
	queries    []queryWire
	scanScript []queryPage
	rankScript []queryPage
	deleteHook func(http.ResponseWriter, *http.Request) bool
	writeHook  func(http.ResponseWriter, string) bool
	nextETag   int
}

func newProtocolFixture(t *testing.T, function azcosmos.VectorDistanceFunction) *protocolFixture {
	t.Helper()
	fixture := &protocolFixture{t: t, properties: map[string]any{"id": "vectors", "partitionKey": map[string]any{"kind": "Hash", "paths": []string{"/partition_key"}}, "vectorEmbeddingPolicy": map[string]any{"vectorEmbeddings": []any{map[string]any{"path": "/embedding", "dataType": "float32", "distanceFunction": string(function), "dimensions": 2}}}}, records: make(map[string]json.RawMessage), scores: make(map[string]float64), pageSize: 2, partition: testPartition()}
	fixture.server = httptest.NewServer(http.HandlerFunc(fixture.serveHTTP))
	t.Cleanup(fixture.server.Close)
	credential, err := azcosmos.NewKeyCredential("dGVzdA==")
	if err != nil {
		t.Fatal(err)
	}
	client, err := azcosmos.NewClientWithKey(fixture.server.URL, credential, &azcosmos.ClientOptions{ClientOptions: azcore.ClientOptions{Retry: policy.RetryOptions{MaxRetries: -1}}})
	if err != nil {
		t.Fatal(err)
	}
	fixture.container, err = client.NewContainer("test", "vectors")
	if err != nil {
		t.Fatal(err)
	}
	return fixture
}

func (p *protocolFixture) store() *Store {
	p.t.Helper()
	store, err := NewStore(p.t.Context(), StoreConfig{Container: p.container, PartitionKey: "library", Space: testSpace})
	if err != nil {
		p.t.Fatal(err)
	}
	p.mu.Lock()
	p.queries = nil
	p.mu.Unlock()
	return store
}

func (p *protocolFixture) serveHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodGet {
		if strings.Contains(r.URL.Path, "/colls/") {
			p.respond(w, p.properties)
			return
		}
		p.respond(w, map[string]any{"id": "test", "readableLocations": []any{}, "writableLocations": []any{}})
		return
	}
	if got := r.Header.Get("x-ms-documentdb-partitionkey"); got != string(mustJSON(p.t, []string{p.partition})) {
		p.t.Errorf("partition header = %q", got)
		w.WriteHeader(400)
		return
	}
	if r.Method == http.MethodDelete {
		if p.deleteHook != nil && p.deleteHook(w, r) {
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/dbs/test/colls/vectors/docs/")
		var record itemRecord
		if _, ok := p.records[id]; !ok {
			w.WriteHeader(404)
			p.respond(w, map[string]string{"code": "NotFound"})
			return
		}
		if err := jsonv2.Unmarshal(p.records[id], &record); err != nil {
			p.t.Error(err)
			w.WriteHeader(404)
			return
		}
		if r.Header.Get("If-Match") != "" && (record.ETag == nil || r.Header.Get("If-Match") != *record.ETag) {
			w.WriteHeader(http.StatusPreconditionFailed)
			p.respond(w, map[string]string{"code": "PreconditionFailed", "message": "changed item"})
			return
		}
		p.deletes = append(p.deletes, id)
		delete(p.records, id)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Header.Get("Content-Type") != "application/query+json" {
		var record map[string]json.RawMessage
		if err := jsonv2.UnmarshalRead(r.Body, &record); err != nil {
			p.t.Error(err)
			w.WriteHeader(400)
			return
		}
		var id string
		if err := jsonv2.Unmarshal(record["id"], &id); err != nil {
			p.t.Error(err)
			w.WriteHeader(400)
			return
		}
		if p.writeHook != nil && p.writeHook(w, id) {
			return
		}
		p.nextETag++
		record["_etag"] = mustJSON(p.t, fmt.Sprintf(`"etag-%d"`, p.nextETag))
		p.records[id] = mustJSON(p.t, record)
		p.writes++
		w.Header().Set("etag", fmt.Sprintf(`"etag-%d"`, p.nextETag))
		w.WriteHeader(201)
		p.respond(w, record)
		return
	}
	var query queryWire
	if err := jsonv2.UnmarshalRead(r.Body, &query); err != nil {
		p.t.Error(err)
		w.WriteHeader(400)
		return
	}
	p.queries = append(p.queries, query)
	scan := query.Query == "SELECT VALUE c FROM c"
	script := &p.rankScript
	if scan {
		script = &p.scanScript
	}
	if len(*script) > 0 {
		page := (*script)[0]
		*script = (*script)[1:]
		if page.token != "" {
			w.Header().Set("x-ms-continuation", page.token)
		}
		if page.status != 0 {
			w.WriteHeader(page.status)
		}
		p.respond(w, map[string]any{"Documents": page.items, "_count": len(page.items)})
		return
	}
	ids := slices.Sorted(maps.Keys(p.records))
	if !scan {
		allowed := make(map[string]bool)
		topK := 0
		for _, parameter := range query.Parameters {
			if strings.HasPrefix(parameter.Name, "@id") {
				id, ok := parameter.Value.(string)
				if !ok {
					p.t.Error("native ID parameter is not a string")
				}
				allowed[id] = true
			}
			if parameter.Name == "@topK" {
				switch value := parameter.Value.(type) {
				case float64:
					topK = int(value)
				case int:
					topK = value
				}
			}
		}
		if strings.Contains(query.Query, " WHERE ") && !strings.Contains(query.Query, " WHERE c.id IN (") {
			p.t.Errorf("native query attempted user filtering: %s", query.Query)
		}
		if len(allowed) > 0 {
			ids = slices.DeleteFunc(ids, func(id string) bool { return !allowed[id] })
		}
		var function string
		embeddings := p.properties["vectorEmbeddingPolicy"].(map[string]any)["vectorEmbeddings"].([]any)
		function = embeddings[0].(map[string]any)["distanceFunction"].(string)
		slices.SortFunc(ids, func(left, right string) int {
			order := cmp.Compare(p.nativeScore(left, function), p.nativeScore(right, function))
			if function != "euclidean" {
				order = -order
			}
			if order != 0 {
				return order
			}
			return cmp.Compare(left, right)
		})
		if topK < len(ids) {
			ids = ids[:topK]
		}
	}
	offset := 0
	if token := r.Header.Get("x-ms-continuation"); token != "" {
		var err error
		offset, err = strconv.Atoi(token)
		if err != nil {
			p.t.Error(err)
			w.WriteHeader(400)
			return
		}
	}
	end := min(len(ids), offset+p.pageSize)
	if end < len(ids) {
		w.Header().Set("x-ms-continuation", strconv.Itoa(end))
	}
	items := make([]json.RawMessage, 0, end-offset)
	for _, id := range ids[offset:end] {
		if scan {
			items = append(items, p.records[id])
			continue
		}
		function := p.properties["vectorEmbeddingPolicy"].(map[string]any)["vectorEmbeddings"].([]any)[0].(map[string]any)["distanceFunction"].(string)
		items = append(items, mustJSON(p.t, map[string]any{"item": p.records[id], "score": p.nativeScore(id, function)}))
	}
	p.respond(w, map[string]any{"Documents": items, "_count": len(items)})
}

func (p *protocolFixture) nativeScore(id, function string) float64 {
	if score, ok := p.scores[id]; ok {
		return score
	}
	if function == "euclidean" {
		return 0
	}
	return 1
}
func (p *protocolFixture) respond(w http.ResponseWriter, value any) {
	if err := jsonv2.MarshalWrite(w, value); err != nil {
		p.t.Error(err)
	}
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := jsonv2.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func storedDocument(t *testing.T, doc *document.Document) json.RawMessage {
	t.Helper()
	record, err := encodeDocument(doc, testPartition())
	if err != nil {
		t.Fatal(err)
	}
	vector := []float32{1, 0}
	etag := `"original"`
	record.Embedding = &vector
	record.ETag = &etag
	return mustJSON(t, record)
}

func TestNativeIDSelectionHasCoreFilterSemantics(t *testing.T) {
	fixture := newProtocolFixture(t, azcosmos.VectorDistanceFunctionCosine)
	store := fixture.store()
	storetest.FilterConformance(t, storetest.FilterConfig{Query: func(ctx context.Context, docs []*document.Document, predicate filter.Predicate) ([]string, error) {
		fixture.mu.Lock()
		clear(fixture.records)
		fixture.mu.Unlock()
		if err := store.Upsert(ctx, testBatch(docs)); err != nil {
			return nil, err
		}
		return scanIDs(ctx, store, predicate)
	}})
}

func TestMetadataRoundtripAndReplacement(t *testing.T) {
	fixture := newProtocolFixture(t, azcosmos.VectorDistanceFunctionCosine)
	store := fixture.store()
	for _, facts := range []metadata.Map{nil, {}, {"large": json.RawMessage(`9007199254740993`), "nested": json.RawMessage(`{"small":1.0000000000000000001,"huge":1e1000}`), "embedding": json.RawMessage(`"user fact"`)}} {
		if err := store.Upsert(t.Context(), testBatch([]*document.Document{{ID: "one", Text: "text", Metadata: facts}})); err != nil {
			t.Fatal(err)
		}
		response, err := store.Query(t.Context(), &vectorstore.Query{Space: testSpace, Values: []float64{1, 0}})
		if err != nil {
			t.Fatal(err)
		}
		if len(response.Results) != 1 || !facts.Equal(response.Results[0].Document.Metadata) || (facts == nil) != (response.Results[0].Document.Metadata == nil) {
			t.Fatalf("metadata changed: %#v", response)
		}
	}
	if err := store.Upsert(t.Context(), testBatch([]*document.Document{{ID: "one", Text: "replacement"}})); err != nil {
		t.Fatal(err)
	}
	response, err := store.Query(t.Context(), &vectorstore.Query{Space: testSpace, Values: []float64{1, 0}})
	if err != nil {
		t.Fatal(err)
	}
	if response.Results[0].Document.Text != "replacement" || response.Results[0].Document.Metadata != nil {
		t.Fatalf("replacement retained obsolete facts: %#v", response)
	}
}

func TestIndexPreparesEveryBatchBeforePublication(t *testing.T) {
	for _, failure := range []string{"dimension", "overflow", "underflow", "late budget", "late ID"} {
		t.Run(failure, func(t *testing.T) {
			fixture := newProtocolFixture(t, azcosmos.VectorDistanceFunctionCosine)
			store := fixture.store()
			docs := []*document.Document{{ID: "one", Text: "first"}, {ID: "two", Text: "second"}}
			if failure == "late budget" {
				docs[1].Text = strings.Repeat("x", maxItemBytes)
			}
			if failure == "late ID" {
				docs[1].ID = "invalid/path"
			}
			batch := testBatch(docs)
			switch failure {
			case "dimension":
				batch.Records[1].Values = []float64{1}
			case "overflow":
				batch.Records[1].Values = []float64{1e100, 0}
			case "underflow":
				batch.Records[1].Values = []float64{1e-100, 0}
			}
			err := store.Upsert(t.Context(), batch)
			if err == nil || fixture.writes != 0 {
				t.Fatalf("Index = %v, published %d writes", err, fixture.writes)
			}

		})
	}
}
