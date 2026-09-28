package typesense

import (
	"context"
	jsonv2 "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/typesense/typesense-go/v3/typesense"
	"github.com/typesense/typesense-go/v3/typesense/api"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/embeddingclient"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
)

type filterHTTPFixture struct {
	mu          sync.Mutex
	docs        []*document.Document
	exportBody  string
	searchBody  string
	requests    []api.MultiSearchCollectionParameters
	deleted     []string
	exportCount int
}

func parseFixtureIDs(source string) ([]string, error) {
	if !strings.HasPrefix(source, "id:=[`") || !strings.HasSuffix(source, "`]") {
		return nil, fmt.Errorf("expected exact ID list, got %q", source)
	}
	return strings.Split(strings.TrimSuffix(strings.TrimPrefix(source, "id:=[`"), "`]"), "`,`"), nil
}

func (f *filterHTTPFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/collections/documents/documents/export" {
		f.exportCount++
		if r.URL.Query().Get("include_fields") != "id,metadata" || r.URL.Query().Get("filter_by") != "" {
			http.Error(w, "metadata export must be complete", http.StatusBadRequest)
			return
		}
		if f.exportBody != "" {
			_, _ = w.Write([]byte(f.exportBody))
			return
		}
		for _, doc := range f.docs {
			encoded, err := jsonv2.Marshal(storedDocument{ID: doc.ID, Metadata: doc.Metadata})
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			_, _ = w.Write(append(encoded, '\n'))
		}
		return
	}
	if r.Method == http.MethodDelete && r.URL.Path == "/collections/documents/documents" {
		ids, err := parseFixtureIDs(r.URL.Query().Get("filter_by"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.deleted = append(f.deleted, ids...)
		_, _ = fmt.Fprintf(w, `{"num_deleted":%d}`, len(ids))
		return
	}
	if r.Method != http.MethodPost || r.URL.Path != "/multi_search" {
		http.Error(w, "unexpected request: "+r.Method+" "+r.URL.Path, http.StatusBadRequest)
		return
	}
	var request api.MultiSearchSearchesParameter
	if err := jsonv2.UnmarshalRead(r.Body, &request); err != nil || len(request.Searches) != 1 {
		http.Error(w, "expected one JSON search", http.StatusBadRequest)
		return
	}
	query := request.Searches[0]
	f.requests = append(f.requests, query)
	if query.PerPage == nil || *query.PerPage < 1 || *query.PerPage > 250 || query.Page == nil || *query.Page < 1 || query.VectorQuery == nil {
		http.Error(w, "invalid vector pagination", http.StatusBadRequest)
		return
	}
	if query.FilterCuratedHits == nil || !*query.FilterCuratedHits {
		http.Error(w, "curated hits must obey filters", http.StatusBadRequest)
		return
	}
	if f.searchBody != "" {
		_, _ = w.Write([]byte(f.searchBody))
		return
	}
	var ids []string
	if query.FilterBy != nil {
		var err error
		ids, err = parseFixtureIDs(*query.FilterBy)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	var selected []*document.Document
	for _, doc := range f.docs {
		if query.FilterBy == nil || slices.Contains(ids, doc.ID) {
			selected = append(selected, doc)
		}
	}
	start := min((*query.Page-1)*(*query.PerPage), len(selected))
	end := min(start+*query.PerPage, len(selected))
	hits := make([]searchHit, 0, end-start)
	for _, doc := range selected[start:end] {
		hit := searchHit{Document: &storedDocument{ID: doc.ID, Content: doc.Text, Metadata: doc.Metadata}}
		if query.Q != nil && *query.Q == "*" {
			hit.VectorDistance = new(float32(.25))
		}
		hits = append(hits, hit)
	}
	encoded, err := jsonv2.Marshal(map[string]any{"results": []any{map[string]any{"found": len(selected), "hits": hits}}})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_, _ = w.Write(encoded)
}

func newFilterHTTPStore(t *testing.T, fixture *filterHTTPFixture) *Store {
	t.Helper()
	server := httptest.NewServer(fixture)
	t.Cleanup(server.Close)
	embeddings, err := embeddingclient.New(embedding.ModelFunc(func(_ context.Context, request *embedding.Request) (*embedding.Response, error) {
		outputs := make([]*embedding.Output, len(request.Texts))
		for index := range outputs {
			outputs[index] = &embedding.Output{Embedding: []float64{1, 0}}
		}
		return embedding.NewResponse(outputs, nil)
	}))
	if err != nil {
		t.Fatal(err)
	}
	return &Store{client: typesense.NewClient(typesense.WithServer(server.URL), typesense.WithAPIKey("test")), collectionName: "documents", embeddingClient: embeddings}
}

func filterDocument(t *testing.T, id string, value any) *document.Document {
	t.Helper()
	meta, err := metadata.FromValues(map[string]any{"value": value})
	if err != nil {
		t.Fatal(err)
	}
	return &document.Document{ID: id, Text: "document " + id, Metadata: meta}
}

func TestFilteredSearchAndDeleteConformance(t *testing.T) {
	for _, deleting := range []bool{false, true} {
		t.Run(fmt.Sprintf("delete=%v", deleting), func(t *testing.T) {
			storetest.FilterConformance(t, storetest.FilterConfig{Query: func(ctx context.Context, docs []*document.Document, predicate filter.Predicate) ([]string, error) {
				fixture := &filterHTTPFixture{docs: docs}
				store := newFilterHTTPStore(t, fixture)
				if deleting {
					if err := store.DeleteWhere(ctx, predicate); err != nil {
						return nil, err
					}
					fixture.mu.Lock()
					defer fixture.mu.Unlock()
					return slices.Clone(fixture.deleted), nil
				}
				response, err := store.Search(ctx, &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{Filter: predicate, TopK: len(docs)}})
				if err != nil {
					return nil, err
				}
				var ids []string
				for _, result := range response.Results {
					ids = append(ids, result.Document.ID)
				}
				return ids, nil
			}})
		})
	}
}

func TestSearchPaginatesAndKeepsGlobalHybridRanks(t *testing.T) {
	for _, mode := range []vectorstore.SearchMode{vectorstore.SearchModeSemantic, vectorstore.SearchModeHybrid} {
		t.Run(string(mode), func(t *testing.T) {
			fixture := &filterHTTPFixture{}
			for index := range 503 {
				fixture.docs = append(fixture.docs, filterDocument(t, fmt.Sprintf("id-%04d", index), "yes"))
			}
			predicate, _ := filter.Parse(`value == 'yes'`)
			response, err := newFilterHTTPStore(t, fixture).Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{Mode: mode, Filter: predicate, TopK: 501}})
			if err != nil {
				t.Fatal(err)
			}
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			if len(response.Results) != 501 || len(fixture.requests) != 3 || fixture.exportCount != 1 {
				t.Fatalf("results=%d requests=%d exports=%d", len(response.Results), len(fixture.requests), fixture.exportCount)
			}
			for index, result := range response.Results {
				if result.Document.ID != fmt.Sprintf("id-%04d", index) {
					t.Fatalf("rank %d ID=%s", index, result.Document.ID)
				}
				if mode == vectorstore.SearchModeHybrid && result.Score != vectorstore.Score(1/float64(index+1)) {
					t.Fatalf("rank %d score=%v", index, result.Score)
				}
			}
			for index, request := range fixture.requests {
				if *request.PerPage != 250 || *request.Page != index+1 || !strings.Contains(*request.VectorQuery, "k: 501") {
					t.Fatalf("page %d parameters=%+v", index, request)
				}
			}
		})
	}
}

func TestFilteredSearchSelectsBeforeTopKAndQuotesIDs(t *testing.T) {
	id := "文档, x && (y) [z]:a/b?c%"
	fixture := &filterHTTPFixture{docs: []*document.Document{filterDocument(t, "nearest-excluded", []string{"yes"}), filterDocument(t, id, "yes")}}
	predicate, _ := filter.Parse(`value == 'yes'`)
	response, err := newFilterHTTPStore(t, fixture).Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{Filter: predicate, TopK: 1}})
	if err != nil || len(response.Results) != 1 || response.Results[0].Document.ID != id {
		t.Fatalf("response=%v error=%v; want matching document outside unfiltered TopK", response, err)
	}
}

func TestFilteredOperationsAbortBeforeRequestsOnExportOrIDErrors(t *testing.T) {
	for _, sample := range []struct{ name, body, source string }{
		{"malformed_tail", "{\"id\":\"valid\",\"metadata\":{\"value\":\"yes\"}}\n{", `value == 'yes'`},
		{"predicate_type", `{"id":"valid","metadata":{"value":"yes"}}` + "\n" + `{"id":"bad","metadata":{"value":42}}`, `value like '%'`},
		{"backtick_id", "{\"id\":\"valid\",\"metadata\":{\"value\":\"yes\"}}\n{\"id\":\"bad`id\",\"metadata\":{\"value\":\"yes\"}}", `value == 'yes'`},
		{"edge_space_id", `{"id":" id","metadata":{"value":"yes"}}`, `value == 'yes'`},
		{"wildcard_id", `{"id":"*","metadata":{"value":"yes"}}`, `value == 'yes'`},
		{"trailing_backslash_id", `{"id":"id\\","metadata":{"value":"yes"}}`, `value == 'yes'`},
	} {
		for _, deleting := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/delete=%v", sample.name, deleting), func(t *testing.T) {
				fixture := &filterHTTPFixture{exportBody: sample.body}
				store := newFilterHTTPStore(t, fixture)
				predicate, _ := filter.Parse(sample.source)
				var err error
				if deleting {
					err = store.DeleteWhere(t.Context(), predicate)
				} else {
					_, err = store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{Filter: predicate}})
				}
				fixture.mu.Lock()
				defer fixture.mu.Unlock()
				if err == nil || len(fixture.requests) != 0 || len(fixture.deleted) != 0 {
					t.Fatalf("error=%v searches=%d deleted=%v; want failure before mutation or ranking", err, len(fixture.requests), fixture.deleted)
				}
			})
		}
	}
}

func TestFilteredSearchRejectsChangedMetadataOrUnselectedIDs(t *testing.T) {
	for _, sample := range []struct{ name, body, source string }{
		{"unselected", `{"results":[{"found":1,"hits":[{"document":{"id":"outsider","content":"text","metadata":{"value":"yes"}},"vector_distance":0}]}]}`, `value == 'yes'`},
		{"changed", `{"results":[{"found":1,"hits":[{"document":{"id":"valid","content":"text","metadata":{"value":"no"}},"vector_distance":0}]}]}`, `value == 'yes'`},
		{"predicate_error", `{"results":[{"found":1,"hits":[{"document":{"id":"valid","content":"text","metadata":{"value":42}},"vector_distance":0}]}]}`, `value like '%'`},
		{"cutoff", `{"results":[{"found":1,"hits":[],"search_cutoff":true}]}`, `value == 'yes'`},
		{"incomplete", `{"results":[{"found":2,"hits":[]}]}`, `value == 'yes'`},
	} {
		t.Run(sample.name, func(t *testing.T) {
			fixture := &filterHTTPFixture{docs: []*document.Document{filterDocument(t, "valid", "yes")}, searchBody: sample.body}
			predicate, _ := filter.Parse(sample.source)
			response, err := newFilterHTTPStore(t, fixture).Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{Filter: predicate}})
			if err == nil || response != nil {
				t.Fatalf("response=%v error=%v; want complete search failure", response, err)
			}
		})
	}
}

func TestMetadataExportHasNoScannerLineLimit(t *testing.T) {
	fixture := &filterHTTPFixture{docs: []*document.Document{filterDocument(t, "large", strings.Repeat("a", 128*1024)), filterDocument(t, "match", "yes")}}
	predicate, _ := filter.Parse(`value == 'yes'`)
	response, err := newFilterHTTPStore(t, fixture).Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{Filter: predicate}})
	if err != nil || len(response.Results) != 1 || response.Results[0].Document.ID != "match" {
		t.Fatalf("response=%v error=%v", response, err)
	}
}
