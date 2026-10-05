package vectara

import (
	"cmp"
	"context"
	jsonv2 "encoding/json/v2"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
)

type testBatcher struct{}

func (t testBatcher) Batch(_ context.Context, docs []*document.Document) ([][]*document.Document, error) {
	return [][]*document.Document{docs}, nil
}

type nativeFixture struct {
	t            *testing.T
	mu           sync.Mutex
	url          string
	records      map[string]nativeDocument
	writes       int
	deletes      []string
	groups       []int
	scores       map[string]float64
	pages        []string
	listed       int
	sourceChange func(map[string]any)
	policyChange func(map[string]any)
	queryChange  func([]map[string]any) []map[string]any
	beforeQuery  func()
	createChange func(*nativeDocument)
	queryFailure bool
	createStatus int
	deleteStatus int
}

func (n *nativeFixture) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	n.mu.Lock()
	defer n.mu.Unlock()
	writer.Header().Set("Content-Type", "application/json")
	if request.Header.Get("x-api-key") != "isolated" {
		n.t.Error("native credentials lost")
	}
	path := "/v2/corpora/corpus/documents"
	var response any
	status := http.StatusOK
	switch {
	case request.Method == http.MethodGet && request.URL.Path == "/v2/corpora/corpus":
		policy := map[string]any{"key": "corpus", "enabled": true, "custom_dimensions": []any{}}
		if n.policyChange != nil {
			n.policyChange(policy)
		}
		response = policy
	case request.Method == http.MethodGet && request.URL.Path == path:
		n.listed++
		if n.pages != nil {
			if n.listed > len(n.pages) {
				n.t.Error("unexpected extra native page")
				writer.WriteHeader(http.StatusInternalServerError)
				return
			}
			_, _ = writer.Write([]byte(n.pages[n.listed-1]))
			return
		}
		ids := slices.Sorted(maps.Keys(n.records))
		offset := 0
		if value := request.URL.Query().Get("page_key"); value != "" {
			var err error
			offset, err = strconv.Atoi(value)
			if err != nil {
				n.t.Error(err)
				return
			}
		}
		limit, err := strconv.Atoi(request.URL.Query().Get("limit"))
		if err != nil || request.URL.Query().Has("metadata_filter") || limit != listPageSize {
			n.t.Error("native enumeration delegated Core semantics or lost limit")
			return
		}
		docs := make([]map[string]any, 0)
		end := min(len(ids), offset+limit)
		for _, id := range ids[offset:end] {
			docs = append(docs, map[string]any{"id": id})
		}
		cursor := ""
		if end < len(ids) {
			cursor = strconv.Itoa(end)
		}
		response = map[string]any{"documents": docs, "metadata": map[string]any{"page_key": cursor}}
	case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, path+"/"):
		id := strings.TrimPrefix(request.URL.Path, path+"/")
		record, exists := n.records[id]
		if !exists {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		object := map[string]any{"id": record.ID, "metadata": record.Metadata, "parts": record.Parts, "tables": record.Tables, "images": record.Images}
		if n.sourceChange != nil {
			n.sourceChange(object)
		}
		response = object
	case request.Method == http.MethodDelete && strings.HasPrefix(request.URL.Path, path+"/"):
		id := strings.TrimPrefix(request.URL.Path, path+"/")
		n.deletes = append(n.deletes, id)
		if n.deleteStatus != 0 {
			writer.WriteHeader(n.deleteStatus)
			return
		}
		if _, exists := n.records[id]; !exists {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		delete(n.records, id)
		writer.WriteHeader(http.StatusNoContent)
		return
	case request.Method == http.MethodPost && request.URL.Path == path:
		n.writes++
		if request.URL.Query().Get("wait_for") != "searchable" {
			n.t.Error("native creation did not request searchable acknowledgment")
		}
		var record nativeIndexDocument
		if err := jsonv2.UnmarshalRead(request.Body, &record); err != nil {
			n.t.Error(err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		if record.Type != "core" || len(record.Parts) != 1 || len(record.Metadata) != 1 {
			n.t.Error("native indexing lost the current source shape")
		}
		value := nativeDocument{ID: record.ID, Metadata: record.Metadata, Parts: record.Parts}
		n.records[value.ID] = value
		if n.createChange != nil {
			n.createChange(&value)
		}
		response = value
		status = cmp.Or(n.createStatus, http.StatusCreated)
	case request.Method == http.MethodPost && request.URL.Path == "/v2/corpora/corpus/query":
		if n.queryFailure {
			writer.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		var body struct {
			Search struct {
				Limit    int     `json:"limit"`
				Filter   string  `json:"metadata_filter"`
				Lexical  float64 `json:"lexical_interpolation"`
				Reranker struct {
					Type string `json:"type"`
				} `json:"reranker"`
			} `json:"search"`
			Generation struct {
				Enabled bool `json:"enabled"`
			} `json:"generation"`
			Stream  bool `json:"stream_response"`
			History bool `json:"save_history"`
			Rewrite bool `json:"intelligent_query_rewriting"`
		}
		if err := jsonv2.UnmarshalRead(request.Body, &body); err != nil {
			n.t.Error(err)
			return
		}
		if body.Search.Reranker.Type != "none" || body.Search.Lexical != 0 || body.Generation.Enabled || body.Stream || body.History || body.Rewrite {
			n.t.Error("native query left the Core semantic contract")
		}
		var ids []string
		if body.Search.Filter != "" {
			if !regexp.MustCompile(`^doc\.id IN \('(?:[^']|'')*'(?:, '(?:[^']|'')*')*\)$`).MatchString(body.Search.Filter) {
				n.t.Error("query contains a competing metadata DSL")
				return
			}
			literals := regexp.MustCompile(`'(?:[^']|'')*'`).FindAllString(body.Search.Filter, -1)
			for _, literal := range literals {
				ids = append(ids, strings.ReplaceAll(literal[1:len(literal)-1], "''", "'"))
			}
			n.groups = append(n.groups, len(ids))
		}
		if n.beforeQuery != nil {
			n.beforeQuery()
			n.beforeQuery = nil
		}
		items := make([]map[string]any, 0)
		for _, id := range slices.Sorted(maps.Keys(n.records)) {
			if ids != nil && !slices.Contains(ids, id) {
				continue
			}
			record := n.records[id]
			items = append(items, map[string]any{"result_type": "text", "text": record.Parts[0].Text, "document_id": id, "document_metadata": record.Metadata, "score": n.scores[id], "corpus_key": "corpus"})
		}
		slices.SortFunc(items, func(left, right map[string]any) int {
			return cmp.Compare(right["score"].(float64), left["score"].(float64))
		})
		items = items[:min(len(items), body.Search.Limit)]
		if n.queryChange != nil {
			items = n.queryChange(items)
		}
		response = map[string]any{"search_results": items}
	default:
		n.t.Errorf("unexpected native call %s %s", request.Method, request.URL.EscapedPath())
		http.NotFound(writer, request)
		return
	}
	writer.WriteHeader(status)
	if err := jsonv2.MarshalWrite(writer, response); err != nil {
		n.t.Error(err)
	}
}

func newNativeStore(t *testing.T, fixture *nativeFixture) *Store {
	t.Helper()
	fixture.t = t
	if fixture.records == nil {
		fixture.records = make(map[string]nativeDocument)
	}
	server := httptest.NewServer(fixture)
	fixture.url = server.URL
	t.Cleanup(server.Close)
	store, err := NewStore(t.Context(), StoreConfig{Endpoint: server.URL, APIKey: "isolated", CorpusKey: "corpus", DocumentBatcher: testBatcher{}, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(fmt.Errorf("construct native store: %w", err))
	}
	return store
}

func indexedNativeStore(t *testing.T, docs []*document.Document) (*Store, *nativeFixture) {
	t.Helper()
	fixture := &nativeFixture{}
	store := newNativeStore(t, fixture)
	if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: docs}); err != nil {
		t.Fatal(err)
	}
	return store, fixture
}
