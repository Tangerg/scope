//go:build integration

package chroma

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	v2 "github.com/amikos-tech/chroma-go/pkg/api/v2"
	ce "github.com/amikos-tech/chroma-go/pkg/embeddings"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
)

type nativeCreateRequest struct {
	Name          string              `json:"name"`
	Configuration nativeConfiguration `json:"configuration"`
}
type nativeConfiguration struct {
	HNSW              nativeHNSW `json:"hnsw"`
	EmbeddingFunction *string    `json:"embedding_function"`
}
type nativeHNSW struct {
	Space v2.Space `json:"space"`
}

type nativeFixture struct {
	v2.Collection
	model       embedding.Model
	calls       atomic.Int64
	beforeModel func(context.Context) error
	beforeQuery func(context.Context) error
}

func newNativeFixture(t *testing.T, space v2.Space) *nativeFixture {
	t.Helper()
	baseURL := os.Getenv("SCOPE_CHROMA_URL")
	if baseURL == "" {
		t.Fatal("SCOPE_CHROMA_URL is required with -tags=integration; use an isolated Chroma 1.x server")
	}
	name := "scope_native_" + strings.ToLower(rand.Text())
	payload, err := jsonv2.Marshal(nativeCreateRequest{Name: name, Configuration: nativeConfiguration{HNSW: nativeHNSW{Space: space}}})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, strings.TrimRight(baseURL, "/")+"/api/v2/tenants/default_tenant/databases/default_database/collections", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	httpClient := &http.Client{Timeout: 10 * time.Second}
	response, err := httpClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	closeErr := response.Body.Close()
	if err = errors.Join(readErr, closeErr); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusCreated {
		t.Fatalf("create collection status=%d: %s", response.StatusCode, body)
	}
	client, err := v2.NewHTTPClient(v2.WithBaseURL(baseURL), v2.WithTimeout(10*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cleanupErr := client.Close(); cleanupErr != nil {
			t.Error(cleanupErr)
		}
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if deleteErr := client.DeleteCollection(ctx, name); deleteErr != nil {
			t.Error(deleteErr)
		}
	})
	collection, err := client.GetCollection(t.Context(), name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cleanupErr := collection.Close(); cleanupErr != nil {
			t.Error(cleanupErr)
		}
	})
	f := &nativeFixture{Collection: collection}
	f.model = embedding.ModelFunc(func(ctx context.Context, request *embedding.Request) (*embedding.Response, error) {
		f.calls.Add(1)
		if f.beforeModel != nil {
			if err := f.beforeModel(ctx); err != nil {
				return nil, err
			}
		}
		outputs := make([]*embedding.Output, len(request.Texts))
		for i, text := range request.Texts {
			vector := []float64{1, 0}
			switch text {
			case "opposite":
				vector = []float64{-1, 0}
			case "far":
				vector = []float64{0, 1}
			case "big":
				vector = []float64{100, 0}
			case "big query":
				vector = []float64{1000, 0}
			case "overflow":
				vector = []float64{math.MaxFloat64, 0}
			case "different width":
				vector = []float64{1, 0, 0}
			}
			outputs[i] = &embedding.Output{Embedding: vector}
		}
		return embedding.NewResponse(outputs, nil)
	})
	return f
}
func (n *nativeFixture) Query(ctx context.Context, options ...v2.QueryOption) (v2.QueryResult, error) {
	if n.beforeQuery != nil {
		if err := n.beforeQuery(ctx); err != nil {
			return nil, err
		}
	}
	return n.Collection.Query(ctx, options...)
}
func (n *nativeFixture) store(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(t.Context(), StoreConfig{Collection: n, EmbeddingModel: n.model, DocumentBatcher: nativeBatcher{}})
	if err != nil {
		t.Fatal(err)
	}
	return store
}
func (n *nativeFixture) ids(t *testing.T) []string {
	t.Helper()
	result, err := n.Get(t.Context(), v2.WithLimit(5000))
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, len(result.GetIDs()))
	for i, id := range result.GetIDs() {
		ids[i] = string(id)
	}
	return ids
}
func (n *nativeFixture) clear(t *testing.T) {
	t.Helper()
	ids := n.ids(t)
	if len(ids) == 0 {
		return
	}
	nativeIDs := make([]v2.DocumentID, len(ids))
	for i, id := range ids {
		nativeIDs[i] = v2.DocumentID(id)
	}
	if err := n.Delete(t.Context(), v2.WithIDs(nativeIDs...)); err != nil {
		t.Fatal(err)
	}
}
func (n *nativeFixture) replace(t *testing.T, id, raw, text string, vector []float32) {
	t.Helper()
	if err := n.Upsert(t.Context(), v2.WithIDs(v2.DocumentID(id)), v2.WithTexts(text), v2.WithEmbeddings(ce.NewEmbeddingFromFloat32(vector)), v2.WithMetadatas(v2.NewDocumentMetadata(v2.NewStringAttribute(metadataField, raw)))); err != nil {
		t.Fatal(err)
	}
}

type nativeBatcher struct{}

func (nativeBatcher) Batch(_ context.Context, docs []*document.Document) ([][]*document.Document, error) {
	return slices.Collect(slices.Chunk(docs, 32)), nil
}
func nativeIndex(t *testing.T, store *Store, docs ...*document.Document) {
	t.Helper()
	if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: docs}); err != nil {
		t.Fatal(err)
	}
}
func nativePredicate(t *testing.T, source string) filter.Predicate {
	t.Helper()
	predicate, err := filter.Parse(source)
	if err != nil {
		t.Fatal(err)
	}
	return predicate
}

func TestNativeFilterConformance(t *testing.T) {
	f := newNativeFixture(t, v2.SpaceCosine)
	store := f.store(t)
	storetest.FilterConformance(t, storetest.FilterConfig{Query: func(ctx context.Context, docs []*document.Document, predicate filter.Predicate) ([]string, error) {
		f.clear(t)
		if err := store.Index(ctx, &vectorstore.IndexRequest{Documents: docs}); err != nil {
			return nil, err
		}
		response, err := store.Search(ctx, &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{Filter: predicate, TopK: len(docs)}})
		if err != nil {
			return nil, err
		}
		ids := make([]string, len(response.Results))
		for i, result := range response.Results {
			ids[i] = result.Document.ID
		}
		return ids, nil
	}})
}
func TestNativeSchemaOwnsMetricAndCoreJSON(t *testing.T) {
	for _, space := range []v2.Space{v2.SpaceCosine, v2.SpaceL2, v2.SpaceIP} {
		t.Run(string(space), func(t *testing.T) {
			f := newNativeFixture(t, space)
			store := f.store(t)
			docs := []*document.Document{{ID: "nil", Text: "query"}, {ID: "empty", Text: "query", Metadata: metadata.Map{}}, {ID: "original/路径'", Text: "opposite", Metadata: metadata.Map{"ok": json.RawMessage(`true`), "exact": json.RawMessage(`9007199254740993`), "precise": json.RawMessage(`1.00000000000000001`), "nested": json.RawMessage(`{"values":[null,1e1000]}`), "null": json.RawMessage(`null`)}}, {ID: "closer", Text: "query", Metadata: metadata.Map{"ok": json.RawMessage(`false`)}}}
			nativeIndex(t, store, docs...)
			response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: len(docs)}})
			if err != nil {
				t.Fatal(err)
			}
			if len(response.Results) != len(docs) {
				t.Fatal(response)
			}
			for _, result := range response.Results {
				source := docs[slices.IndexFunc(docs, func(doc *document.Document) bool { return doc.ID == result.Document.ID })]
				if !source.Metadata.Equal(result.Document.Metadata) || (source.Metadata == nil) != (result.Document.Metadata == nil) {
					t.Fatalf("metadata=%s want %s", result.Document.Metadata, source.Metadata)
				}
			}
			if response.First().Document.ID != "closer" {
				t.Fatal("native distance tie lost ID order")
			}
			response, err = store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 1, Filter: nativePredicate(t, "ok == true")}})
			if err != nil || response.First() == nil || response.First().Document.ID != "original/路径'" {
				t.Fatalf("filtered response=%v err=%v", response, err)
			}
			want := 0.0
			if space == v2.SpaceL2 {
				want = .2
			}
			if space == v2.SpaceIP {
				want = 1 / (1 + math.Exp(1))
			}
			if math.Abs(response.First().Score.Float64()-want) > 1e-7 {
				t.Fatalf("native %s score=%v want %v", space, response.First().Score, want)
			}

		})
	}
}
func TestNativeCompletePaginationAndPreflight(t *testing.T) {
	f := newNativeFixture(t, v2.SpaceCosine)
	store := f.store(t)
	docs := make([]*document.Document, 600)
	for i := range docs {
		docs[i] = &document.Document{ID: fmt.Sprintf("doc-%03d", i), Text: "query", Metadata: metadata.Map{"rank": json.RawMessage(`1`)}}
	}
	nativeIndex(t, store, docs...)
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: len(docs)}})
	if err != nil || len(response.Results) == 0 || len(response.Results) > len(docs) {
		t.Fatalf("paged response=%v err=%v", response, err)
	}
	f.replace(t, "doc-599", `{"rank":"wrong"}`, "query", []float32{1, 0})
	calls := f.calls.Load()
	predicate := nativePredicate(t, "rank < 2")
	response, err = store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{Filter: predicate, TopK: 1, MinScore: 1}})
	if response != nil || err == nil || f.calls.Load() != calls {
		t.Fatalf("preflight response=%v err=%v calls=%d want %d", response, err, f.calls.Load(), calls)
	}
	if len(f.ids(t)) != len(docs) {
		t.Fatal("preflight altered stored records")
	}

}
func TestNativeSearchValidatesCurrentQueryRecords(t *testing.T) {
	f := newNativeFixture(t, v2.SpaceCosine)
	store := f.store(t)
	nativeIndex(t, store, &document.Document{ID: "same", Text: "query", Metadata: metadata.Map{"rank": json.RawMessage(`1`)}})
	f.beforeModel = func(context.Context) error { f.replace(t, "same", `{"rank":1}`, "far", []float32{0, 1}); return nil }
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{Filter: nativePredicate(t, "rank == 1")}})
	if err != nil || response.First() == nil || response.First().Document.Text != "far" || response.First().Score != .5 {
		t.Fatalf("current response=%v err=%v", response, err)
	}
	f.beforeModel = func(context.Context) error {
		f.replace(t, "same", `{"rank":"wrong"}`, "query", []float32{1, 0})
		return nil
	}
	response, err = store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{Filter: nativePredicate(t, "rank < 2"), MinScore: 1}})
	if response != nil || err == nil {
		t.Fatalf("malformed current response=%v err=%v", response, err)
	}
	f.beforeModel = nil
	f.replace(t, "same", `{"rank":1}`, "query", []float32{1, 0})
	f.beforeQuery = func(ctx context.Context) error {
		f.beforeQuery = nil
		return f.Delete(ctx, v2.WithIDs("same"))
	}
	response, err = store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query"})
	if response != nil || err == nil {
		t.Fatalf("disappeared response=%v err=%v", response, err)
	}
}
func TestNativeIPRankingAndWholeIndexValidation(t *testing.T) {
	f := newNativeFixture(t, v2.SpaceIP)
	store := f.store(t)
	nativeIndex(t, store, &document.Document{ID: "a-lower", Text: "query"}, &document.Document{ID: "z-higher", Text: "big"})
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "big query", Options: vectorstore.SearchOptions{TopK: 1}})
	if err != nil || response.First() == nil || response.First().Document.ID != "z-higher" || response.First().Score != 1 {
		t.Fatalf("saturated response=%v err=%v", response, err)
	}
	f.clear(t)
	docs := make([]*document.Document, 33)
	for i := range docs {
		docs[i] = &document.Document{ID: fmt.Sprintf("doc-%03d", i), Text: "query"}
	}
	for _, bad := range []string{"overflow", "different width"} {
		docs[32].Text = bad
		if err = store.Index(t.Context(), &vectorstore.IndexRequest{Documents: docs}); err == nil {
			t.Fatal("accepted invalid later batch", bad)
		}
		if len(f.ids(t)) != 0 {
			t.Fatal("later validation published earlier batch")
		}
	}
}
func TestNativeStrictRecordsAndCancellation(t *testing.T) {
	f := newNativeFixture(t, v2.SpaceL2)
	store := f.store(t)
	for _, raw := range []string{"", `[]`, `{"a":1,"a":2}`} {
		f.clear(t)
		f.replace(t, "bad", raw, "query", []float32{1, 0})
		calls := f.calls.Load()
		response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{Filter: nativePredicate(t, "a == 'hidden'"), MinScore: 1}})
		if response != nil || err == nil || f.calls.Load() != calls {
			t.Fatalf("invalid response=%v err=%v", response, err)
		}
		if current, err := NewStore(t.Context(), StoreConfig{Collection: f, EmbeddingModel: f.model, DocumentBatcher: nativeBatcher{}}); current != nil || err == nil {
			t.Fatal("constructor accepted malformed stored records")
		}
	}
	f.clear(t)
	if err := f.Upsert(t.Context(), v2.WithIDs("old"), v2.WithTexts("query"), v2.WithEmbeddings(ce.NewEmbeddingFromFloat32([]float32{1, 0})), v2.WithMetadatas(v2.NewDocumentMetadata(v2.NewStringAttribute("old", "field")))); err != nil {
		t.Fatal(err)
	}
	if current, err := NewStore(t.Context(), StoreConfig{Collection: f, EmbeddingModel: f.model, DocumentBatcher: nativeBatcher{}}); current != nil || err == nil {
		t.Fatal("constructor accepted flat metadata schema")
	}
	f.clear(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	response, err := store.Search(ctx, &vectorstore.SearchRequest{Query: "query"})
	if response != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled response=%v err=%v", response, err)
	}
	if err = store.DeleteIDs(ctx, []string{"unknown"}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	nativeIndex(t, store, &document.Document{ID: "original/路径", Text: "query"})
	if err = store.DeleteIDs(t.Context(), []string{"original/路径", "original/路径", "unknown"}); err != nil || len(f.ids(t)) != 0 {
		t.Fatal(err)
	}
}
