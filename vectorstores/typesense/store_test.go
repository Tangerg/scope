package typesense

import (
	"cmp"
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/typesense/typesense-go/v3/typesense/api"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
)

type testBatcher struct{ size int }

func (t testBatcher) Batch(_ context.Context, docs []*document.Document) ([][]*document.Document, error) {
	return slices.Collect(slices.Chunk(docs, cmp.Or(t.size, 32))), nil
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

type countedBody struct {
	io.ReadCloser
	closed *atomic.Int64
}

func (c *countedBody) Close() error { c.closed.Add(1); return c.ReadCloser.Close() }

type countingTransport struct{ responses, closed *atomic.Int64 }

func (c countingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := http.DefaultTransport.RoundTrip(request)
	if err == nil {
		c.responses.Add(1)
		response.Body = &countedBody{ReadCloser: response.Body, closed: c.closed}
	}
	return response, err
}

type protocolFixture struct {
	t                 *testing.T
	mu                sync.Mutex
	server            *httptest.Server
	client            *api.Client
	schema            map[string]any
	records           map[string]json.RawMessage
	responses, closed atomic.Int64
	imports           int
	queries           []searchQuery
	deletes           []string
	searchScript      []json.RawMessage
	exportOverride    *string
	importOverride    *string
	deleteOverride    *string
}

func newProtocolFixture(t *testing.T) *protocolFixture {
	t.Helper()
	fixture := &protocolFixture{t: t, schema: map[string]any{"name": "documents", "fields": []any{map[string]any{"name": "content", "type": "string", "index": true, "store": true}, map[string]any{"name": "metadata", "type": "string", "index": false, "store": true}, map[string]any{"name": "embedding", "type": "float[]", "num_dim": 2, "vec_dist": "cosine", "index": true, "store": true}}}, records: make(map[string]json.RawMessage)}
	fixture.server = httptest.NewServer(http.HandlerFunc(fixture.serveHTTP))
	t.Cleanup(fixture.server.Close)
	var err error
	fixture.client, err = api.NewClient(fixture.server.URL, api.WithHTTPClient(&http.Client{Transport: countingTransport{responses: &fixture.responses, closed: &fixture.closed}}))
	if err != nil {
		t.Fatal(err)
	}
	return fixture
}

func (p *protocolFixture) store(model embedding.Model, size int) *Store {
	p.t.Helper()
	store, err := NewStore(p.t.Context(), StoreConfig{Client: p.client, CollectionName: "documents", EmbeddingModel: model, DocumentBatcher: testBatcher{size: size}})
	if err != nil {
		p.t.Fatal(err)
	}
	return store
}

func (p *protocolFixture) serveHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodGet && r.URL.Path == "/collections/documents" {
		p.respond(w, p.schema)
		return
	}
	if strings.HasSuffix(r.URL.Path, "/export") {
		if p.exportOverride != nil {
			io.WriteString(w, *p.exportOverride)
			return
		}
		for _, key := range slices.Sorted(maps.Keys(p.records)) {
			w.Write(p.records[key])
			io.WriteString(w, "\n")
		}
		return
	}
	if strings.HasSuffix(r.URL.Path, "/import") {
		p.imports++
		if r.URL.Query().Get("action") != "upsert" {
			p.t.Errorf("import action = %q", r.URL.RawQuery)
		}
		decoder := jsontext.NewDecoder(r.Body)
		for {
			value, err := decoder.ReadValue()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				p.t.Error(err)
				w.WriteHeader(400)
				return
			}
			var record storedRecord
			if err = jsonv2.Unmarshal(value, &record); err != nil || record.ID == nil {
				p.t.Errorf("native import = %#v, %v", record, err)
				w.WriteHeader(400)
				return
			}
			p.records[*record.ID] = slices.Clone(value)
			if p.importOverride == nil {
				io.WriteString(w, "{\"success\":true}\n")
			}
		}
		if p.importOverride != nil {
			io.WriteString(w, *p.importOverride)
		}
		return
	}
	if r.Method == http.MethodDelete {
		filterBy := r.URL.Query().Get("filter_by")
		if !strings.HasPrefix(filterBy, "id:=[") || !strings.HasSuffix(filterBy, "]") {
			p.t.Errorf("native explicit deletion = %q", filterBy)
			w.WriteHeader(400)
			return
		}
		count := 0
		for _, key := range strings.Split(strings.TrimSuffix(strings.TrimPrefix(filterBy, "id:=["), "]"), ",") {
			p.deletes = append(p.deletes, key)
			if _, ok := p.records[key]; ok {
				count++
				delete(p.records, key)
			}
		}
		if p.deleteOverride != nil {
			io.WriteString(w, *p.deleteOverride)
		} else {
			p.respond(w, map[string]int{"num_deleted": count})
		}
		return
	}
	if r.URL.Path != "/multi_search" {
		p.t.Errorf("unexpected native operation: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(400)
		return
	}
	var wire struct {
		Searches []searchQuery `json:"searches"`
	}
	if err := jsonv2.UnmarshalRead(r.Body, &wire); err != nil || len(wire.Searches) != 1 {
		p.t.Errorf("multi_search = %#v, %v", wire, err)
		w.WriteHeader(400)
		return
	}
	query := wire.Searches[0]
	p.queries = append(p.queries, query)
	if query.EnableCurations || query.EnableOverrides != nil {
		p.t.Error("curation can originate ranked hits")
	}
	if len(p.searchScript) > 0 {
		value := p.searchScript[0]
		p.searchScript = p.searchScript[1:]
		w.Write(value)
		return
	}
	keys := slices.Sorted(maps.Keys(p.records))
	if query.FilterBy != nil {
		source := *query.FilterBy
		if !strings.HasPrefix(source, "id:=[") || !strings.HasSuffix(source, "]") {
			p.t.Errorf("native user predicate received: %s", source)
			w.WriteHeader(400)
			return
		}
		allowed := strings.Split(strings.TrimSuffix(strings.TrimPrefix(source, "id:=["), "]"), ",")
		keys = slices.DeleteFunc(keys, func(key string) bool { return !slices.Contains(allowed, key) })
	}
	topK := 0
	if query.VectorQuery != nil {
		source := *query.VectorQuery
		offset := strings.Index(source, "k: ")
		if offset >= 0 {
			fmt.Sscanf(source[offset:], "k: %d", &topK)
		}
	}
	keys = keys[:min(len(keys), topK)]
	size := *query.PerPage
	offset := (*query.Page - 1) * size
	end := min(len(keys), offset+size)
	hits := make([]any, 0, end-offset)
	for _, key := range keys[offset:end] {
		hits = append(hits, map[string]any{"document": p.records[key], "vector_distance": 0})
	}
	p.respond(w, map[string]any{"results": []any{map[string]any{"found": len(keys), "hits": hits, "search_cutoff": false}}})
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
func nativeRecord(t *testing.T, doc *document.Document) json.RawMessage {
	t.Helper()
	record, err := encodeDocument(doc)
	if err != nil {
		t.Fatal(err)
	}
	vector := []float32{1, 0}
	record.Embedding = &vector
	return mustJSON(t, record)
}

func TestNativeIDSelectionHasCoreFilterSemantics(t *testing.T) {
	fixture := newProtocolFixture(t)
	store := fixture.store(constantModel(), 32)
	storetest.FilterConformance(t, storetest.FilterConfig{Query: func(ctx context.Context, docs []*document.Document, predicate filter.Predicate) ([]string, error) {
		fixture.mu.Lock()
		clear(fixture.records)
		fixture.mu.Unlock()
		if err := store.Index(ctx, &vectorstore.IndexRequest{Documents: docs}); err != nil {
			return nil, err
		}
		response, err := store.Search(ctx, &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{Filter: predicate, TopK: len(docs)}})
		if err != nil {
			return nil, err
		}
		ids := make([]string, len(response.Results))
		for i, hit := range response.Results {
			ids[i] = hit.Document.ID
		}
		return ids, nil
	}})
	if fixture.responses.Load() != fixture.closed.Load() {
		t.Fatalf("closed %d of %d native responses", fixture.closed.Load(), fixture.responses.Load())
	}
}

func TestCoreMetadataAndIdentityRoundtrip(t *testing.T) {
	fixture := newProtocolFixture(t)
	store := fixture.store(constantModel(), 32)
	for _, id := range []string{"one", " spaced ", "*", "back`tick", "trailing\\", "a/b?x", "中文🙂"} {
		for _, facts := range []metadata.Map{nil, {}, {"large": json.RawMessage(`9007199254740993`), "nested": json.RawMessage(`{"decimal":1.0000000000000000001,"huge":1e1000}`), "content": json.RawMessage(`"user fact"`)}} {
			fixture.mu.Lock()
			clear(fixture.records)
			fixture.mu.Unlock()
			if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{{ID: id, Text: "text", Metadata: facts}}}); err != nil {
				t.Fatal(err)
			}
			response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{Filter: filter.IsNull("unused")}})
			if err != nil {
				t.Fatal(err)
			}
			if len(response.Results) != 1 || response.Results[0].Document.ID != id || !facts.Equal(response.Results[0].Document.Metadata) || (facts == nil) != (response.Results[0].Document.Metadata == nil) {
				t.Fatalf("roundtrip changed original facts: %#v", response)
			}
			if err = store.DeleteIDs(t.Context(), []string{id, id, "unknown"}); err != nil {
				t.Fatal(err)
			}
			if len(fixture.records) != 0 {
				t.Fatal("explicit deletion missed encoded identity")
			}
		}
	}
	if fixture.responses.Load() != fixture.closed.Load() {
		t.Fatal("native response body leaked")
	}
}

func TestIndexPreparesEveryBatchBeforePublication(t *testing.T) {
	for _, fault := range []string{"late embedding", "dimension", "overflow", "underflow", "invalid metadata"} {
		t.Run(fault, func(t *testing.T) {
			fixture := newProtocolFixture(t)
			calls := 0
			cause := errors.New("late model failure")
			model := embedding.ModelFunc(func(_ context.Context, request *embedding.Request) (*embedding.Response, error) {
				calls++
				if calls == 2 && fault == "late embedding" {
					return nil, cause
				}
				vector := []float64{1, 0}
				if calls == 2 {
					switch fault {
					case "dimension":
						vector = []float64{1}
					case "overflow":
						vector = []float64{1e100, 0}
					case "underflow":
						vector = []float64{1e-100, 0}
					}
				}
				return embedding.NewResponse([]*embedding.Output{{Embedding: vector}}, nil)
			})
			store := fixture.store(model, 1)
			docs := []*document.Document{{ID: "one", Text: "first"}, {ID: "two", Text: "second"}}
			if fault == "invalid metadata" {
				docs[1].Metadata = metadata.Map{"bad": json.RawMessage(`broken`)}
			}
			err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: docs})
			if err == nil || fixture.imports != 0 {
				t.Fatalf("Index = %v, published imports = %d", err, fixture.imports)
			}
			if fault == "late embedding" && !errors.Is(err, cause) {
				t.Fatalf("lost model cause: %v", err)
			}
			if fault == "invalid metadata" && calls != 0 {
				t.Fatal("invalid metadata reached model")
			}
		})
	}
}

func TestImportRequiresAllNativeAcknowledgments(t *testing.T) {
	for _, body := range []string{"{\"success\":true}\n{\"success\":true}\n", "{\"success\":true}\n", "{\"success\":true}\n{\"success\":false,\"error\":\"bad\"}\n", "{\"success\":true}\nnull\n", "{}\n{}\n", "{\"success\":true}\n{\"success\":true}\n{\"success\":true}\n"} {
		t.Run(body, func(t *testing.T) {
			fixture := newProtocolFixture(t)
			store := fixture.store(constantModel(), 32)
			fixture.importOverride = &body
			err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{{ID: "one", Text: "first"}, {ID: "two", Text: "second"}}})
			wantSuccess := body == "{\"success\":true}\n{\"success\":true}\n"
			if (err == nil) != wantSuccess {
				t.Fatalf("Index = %v", err)
			}
			if fixture.responses.Load() != fixture.closed.Load() {
				t.Fatal("native import response body leaked")
			}
		})
	}
}
