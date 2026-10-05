package azureaisearch

import (
	"context"
	jsonv2 "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/metadata"
)

type writeTestBatcher struct{ single bool }

func (w writeTestBatcher) Batch(_ context.Context, documents []*document.Document) ([][]*document.Document, error) {
	if !w.single {
		return [][]*document.Document{documents}, nil
	}
	batches := make([][]*document.Document, len(documents))
	for i, doc := range documents {
		batches[i] = []*document.Document{doc}
	}
	return batches, nil
}

type protocolFixture struct {
	mu                    sync.Mutex
	t                     *testing.T
	schema                string
	rows                  map[string]metadata.Map
	bodies                []metadata.Map
	writes                [][]metadata.Map
	modelCalls            atomic.Int32
	model                 embedding.Model
	server                *httptest.Server
	pageSize              int
	expectedAuthorization string
	query                 func(http.ResponseWriter, metadata.Map)
	write                 func(http.ResponseWriter, []metadata.Map)
	scan                  func(http.ResponseWriter, metadata.Map)
}

func newProtocolFixture(t *testing.T) *protocolFixture {
	t.Helper()
	fixture := &protocolFixture{t: t, schema: nativeSchema("cosine"), rows: make(map[string]metadata.Map), pageSize: maximumResultsPerPage}
	fixture.model = embedding.ModelFunc(func(_ context.Context, request *embedding.Request) (*embedding.Response, error) {
		fixture.modelCalls.Add(1)
		outputs := make([]*embedding.Output, len(request.Texts))
		for i := range outputs {
			outputs[i] = &embedding.Output{Embedding: []float64{1, 0}}
		}
		return embedding.NewResponse(outputs, nil)
	})
	fixture.server = httptest.NewServer(http.HandlerFunc(fixture.serveHTTP))
	t.Cleanup(fixture.server.Close)
	return fixture
}

func nativeSchema(metric string) string {
	return fmt.Sprintf(`{"fields":[
 {"name":"id","type":"Edm.String","key":true,"filterable":true,"sortable":true,"searchable":false},
 {"name":"content","type":"Edm.String","searchable":true},
 {"name":"contentVector","type":"Collection(Edm.Single)","searchable":true,"filterable":false,"sortable":false,"facetable":false,"dimensions":2,"vectorSearchProfile":"p"},
 {"name":"scope_metadata","type":"Edm.String","searchable":false,"filterable":false,"sortable":false,"facetable":false}],
 "vectorSearch":{"profiles":[{"name":"p","algorithm":"a"}],"algorithms":[{"name":"a","kind":"hnsw","hnswParameters":{"metric":%q}}]}}`, metric)
}

func (p *protocolFixture) config() StoreConfig {
	return StoreConfig{Endpoint: p.server.URL, IndexName: "documents", HTTPClient: p.server.Client(), EmbeddingModel: p.model, DocumentBatcher: writeTestBatcher{}}
}

func (p *protocolFixture) store() *Store {
	p.t.Helper()
	store, err := NewStore(p.t.Context(), p.config())
	if err != nil {
		p.t.Fatal(err)
	}
	return store
}

func (p *protocolFixture) put(docs ...*document.Document) {
	p.t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, doc := range docs {
		encoded, err := doc.Metadata.MarshalJSON()
		if err != nil {
			p.t.Fatal(err)
		}
		row, err := metadata.FromValues(map[string]any{"id": doc.ID, "content": doc.Text, "contentVector": []float32{1, 0}, "scope_metadata": string(encoded), "@search.score": 1})
		if err != nil {
			p.t.Fatal(err)
		}
		p.rows[doc.ID] = row
	}
}

func (p *protocolFixture) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	writer.Header().Set("Content-Type", "application/json")
	if request.URL.Query().Get("api-version") != DefaultAPIVersion {
		p.t.Error("missing native API version")
	}
	if request.Header.Get("Authorization") != p.expectedAuthorization {
		p.t.Error("host authentication was not preserved")
	}
	if request.Header.Get("api-key") != "" {
		p.t.Error("Scope originated authentication")
	}
	if request.Method == http.MethodGet && request.URL.Path == "/indexes/documents" {
		fmt.Fprint(writer, p.schema)
		return
	}
	var body metadata.Map
	if err := jsonv2.UnmarshalRead(request.Body, &body); err != nil {
		p.t.Error(err)
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	p.bodies = append(p.bodies, body)
	if request.URL.Path == "/indexes/documents/docs/index" {
		actions, _, err := body.Decode[[]metadata.Map]("value")
		if err != nil {
			p.t.Error(err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		p.writes = append(p.writes, actions)
		if p.write != nil {
			p.write(writer, actions)
			return
		}
		results := make([]map[string]any, len(actions))
		for i, action := range actions {
			id, _, decodeErr := action.Decode[string]("id")
			if decodeErr != nil {
				p.t.Error(decodeErr)
			}
			operation, _, _ := action.Decode[string]("@search.action")
			switch operation {
			case "upload":
				row := action.Clone()
				delete(row, "@search.action")
				_ = row.Set("@search.score", 1)
				p.rows[id] = row
			case "delete":
				delete(p.rows, id)
			default:
				p.t.Errorf("unexpected native action %q", operation)
			}
			results[i] = map[string]any{"key": id, "status": true, "statusCode": 200}
		}
		_ = jsonv2.MarshalWrite(writer, map[string]any{"value": results})
		return
	}
	if request.URL.Path != "/indexes/documents/docs/search" {
		p.t.Errorf("unexpected path %q", request.URL.Path)
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	order, _, _ := body.Decode[string]("orderby")
	filterText, _, _ := body.Decode[string]("filter")
	var ids []string
	if order != "" {
		if p.scan != nil {
			p.scan(writer, body)
			return
		}
		if order != "id asc" {
			p.t.Errorf("unexpected scan ordering %q", order)
		}
		last := ""
		if filterText != "" {
			if !strings.HasPrefix(filterText, "id gt '") || !strings.HasSuffix(filterText, "'") {
				p.t.Errorf("unexpected scan filter %q", filterText)
			}
			last = strings.TrimSuffix(strings.TrimPrefix(filterText, "id gt '"), "'")
		}
		for id := range p.rows {
			if id > last {
				ids = append(ids, id)
			}
		}
		slices.Sort(ids)
		ids = ids[:min(len(ids), p.pageSize)]
	} else {
		if p.query != nil {
			p.query(writer, body)
			return
		}
		const prefix = "search.in(id, '"
		const suffix = "', ',')"
		if !strings.HasPrefix(filterText, prefix) || !strings.HasSuffix(filterText, suffix) {
			p.t.Errorf("query carried a metadata compiler instead of IDs: %q", filterText)
		}
		selected := strings.Split(strings.TrimSuffix(strings.TrimPrefix(filterText, prefix), suffix), ",")
		for _, id := range selected {
			if _, exists := p.rows[id]; exists {
				ids = append(ids, id)
			}
		}
		top, _, _ := body.Decode[int]("top")
		ids = ids[:min(len(ids), top)]
	}
	rows := make([]metadata.Map, 0, len(ids))
	for _, id := range ids {
		rows = append(rows, p.rows[id])
	}
	_ = jsonv2.MarshalWrite(writer, map[string]any{"value": rows})
}

func rawMetadata(source string) metadata.Map {
	var facts metadata.Map
	if err := facts.UnmarshalJSON([]byte(source)); err != nil {
		panic(err)
	}
	return facts
}
