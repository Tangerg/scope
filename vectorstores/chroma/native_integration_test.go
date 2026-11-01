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
	"strings"

	"testing"
	"time"

	v2 "github.com/amikos-tech/chroma-go/pkg/api/v2"
	ce "github.com/amikos-tech/chroma-go/pkg/embeddings"

	"github.com/Tangerg/scope/core/document"

	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
)

type nativeCreateRequest struct {
	Metadata      map[string]string   `json:"metadata"`
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
	beforeQuery func(context.Context) error
}

func newNativeFixture(t *testing.T, space v2.Space) *nativeFixture {
	t.Helper()
	baseURL := os.Getenv("SCOPE_CHROMA_URL")
	if baseURL == "" {
		t.Fatal("SCOPE_CHROMA_URL is required with -tags=integration; use an isolated Chroma 1.x server")
	}
	name := "scope_native_" + strings.ToLower(rand.Text())
	payload, err := jsonv2.Marshal(nativeCreateRequest{Name: name, Metadata: map[string]string{SpaceMetadataKey: string(testSpace)}, Configuration: nativeConfiguration{HNSW: nativeHNSW{Space: space}}})
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
	// Chroma fixes and reports the dimension only after the first vector.
	// Provision it in this isolated collection, then capture the native schema
	// through a fresh SDK handle. No adapter call is allowed to guess the width.
	if err = collection.Upsert(t.Context(), v2.WithIDs("dimension_seed"), v2.WithTexts("seed"), v2.WithEmbeddings(ce.NewEmbeddingFromFloat32([]float32{1, 0})), v2.WithMetadatas(v2.NewDocumentMetadata(v2.NewStringAttribute(metadataField, "null")))); err != nil {
		t.Fatal(err)
	}
	if err = collection.Delete(t.Context(), v2.WithIDs("dimension_seed")); err != nil {
		t.Fatal(err)
	}
	bound, err := client.GetCollection(t.Context(), name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := bound.Close(); err != nil {
			t.Error(err)
		}
	})
	return &nativeFixture{Collection: bound}
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
	store, err := NewStore(t.Context(), StoreConfig{Collection: n, Space: testSpace})
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

func nativeVectors(text string) []float64 {
	switch text {
	case "opposite":
		return []float64{-1, 0}
	case "far":
		return []float64{0, 1}
	case "big":
		return []float64{100, 0}
	case "big query":
		return []float64{1000, 0}
	default:
		return []float64{1, 0}
	}
}
func nativeBatch(docs []*document.Document) *vectorstore.Batch {
	records := make([]vectorstore.Record, len(docs))
	for i, d := range docs {
		records[i] = vectorstore.Record{Document: d, Values: nativeVectors(d.Text)}
	}
	return &vectorstore.Batch{Space: testSpace, Records: records}
}
func nativeUpsert(t *testing.T, store *Store, docs ...*document.Document) {
	t.Helper()
	if err := store.Upsert(t.Context(), nativeBatch(docs)); err != nil {
		t.Fatal(err)
	}
}
func nativePredicate(t *testing.T, source string) filter.Predicate {
	t.Helper()
	p, err := filter.Parse(source)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func nativeBudget() vectorstore.ScanBudget {
	return vectorstore.ScanBudget{MaxRecords: 1000, MaxBytes: 16 << 20, Timeout: time.Minute}
}

func TestNativeFilterConformance(t *testing.T) {
	f := newNativeFixture(t, v2.SpaceCosine)
	s := f.store(t)
	storetest.FilterConformance(t, storetest.FilterConfig{Query: func(ctx context.Context, docs []*document.Document, predicate filter.Predicate) ([]string, error) {
		f.clear(t)
		if len(docs) != 0 {
			if err := s.Upsert(ctx, nativeBatch(docs)); err != nil {
				return nil, err
			}
		}
		found, err := s.Scan(ctx, ScanConfig{Budget: nativeBudget(), Filter: predicate})
		if err != nil {
			return nil, err
		}
		ids := make([]string, len(found))
		for i, d := range found {
			ids[i] = d.ID
		}
		return ids, nil
	}})
}
func TestNativeSchemaOwnsMetricAndCoreJSON(t *testing.T) {
	for _, metric := range []v2.Space{v2.SpaceCosine, v2.SpaceL2, v2.SpaceIP} {
		t.Run(string(metric), func(t *testing.T) {
			f := newNativeFixture(t, metric)
			s := f.store(t)
			docs := []*document.Document{{ID: "nil", Text: "query"}, {ID: "empty", Text: "query", Metadata: metadata.Map{}}, {ID: "original/路径'", Text: "opposite", Metadata: metadata.Map{"ok": json.RawMessage(`true`), "exact": json.RawMessage(`9007199254740993`), "precise": json.RawMessage(`1.00000000000000001`), "nested": json.RawMessage(`{"values":[null,1e1000]}`), "null": json.RawMessage(`null`)}}, {ID: "closer", Text: "query", Metadata: metadata.Map{"ok": json.RawMessage(`false`)}}}
			nativeUpsert(t, s, docs...)
			got, err := s.Query(t.Context(), query(vectorstore.SearchOptions{TopK: len(docs)}))
			if err != nil || len(got.Results) != len(docs) {
				t.Fatal(got, err)
			}
			byID := make(map[string]*vectorstore.SearchResult)
			for _, r := range got.Results {
				byID[r.Document.ID] = r
			}
			for _, d := range docs {
				r := byID[d.ID]
				if r == nil || !r.Document.Metadata.Equal(d.Metadata) || (r.Document.Metadata == nil) != (d.Metadata == nil) {
					t.Fatalf("record did not round-trip: %+v", d)
				}
			}
			want := 0.0
			if metric == v2.SpaceL2 {
				want = 0.2
			}
			if metric == v2.SpaceIP {
				want = 1 / (1 + math.Exp(1))
			}
			if score := byID["original/路径'"].Score.Float64(); math.Abs(score-want) > 1e-7 {
				t.Fatalf("native %s score=%v want %v", metric, score, want)
			}
			filtered, err := s.Scan(t.Context(), ScanConfig{Budget: nativeBudget(), Filter: nativePredicate(t, "ok == true")})
			if err != nil || len(filtered) != 1 || filtered[0].ID != "original/路径'" {
				t.Fatal(filtered, err)
			}
			if _, err = NewStore(t.Context(), StoreConfig{Collection: f, Space: "wrong-space"}); !errors.Is(err, vectorstore.ErrIncompatibleSpace) {
				t.Fatalf("shared dimension accepted foreign space: %v", err)
			}
		})
	}
}
func TestNativeCompletePaginationAndPreflight(t *testing.T) {
	f := newNativeFixture(t, v2.SpaceCosine)
	s := f.store(t)
	docs := make([]*document.Document, 600)
	for i := range docs {
		docs[i] = &document.Document{ID: fmt.Sprintf("doc-%03d", i), Text: "query", Metadata: metadata.Map{"rank": json.RawMessage(`1`)}}
	}
	nativeUpsert(t, s, docs...)
	cfg := ScanConfig{Budget: nativeBudget(), Filter: nativePredicate(t, "rank == 1")}
	found, err := s.Scan(t.Context(), cfg)
	if err != nil || len(found) != 600 || found[599].ID != "doc-599" {
		t.Fatal(len(found), err)
	}
	cfg.Budget.MaxRecords = 599
	if found, err = s.Scan(t.Context(), cfg); found != nil || !errors.Is(err, vectorstore.ErrScanBudget) {
		t.Fatal(found, err)
	}
	f.replace(t, "doc-599", `{"rank":"wrong type"}`, "query", []float32{1, 0})
	cfg.Budget = nativeBudget()
	cfg.Filter = nativePredicate(t, "rank > 0")
	if found, err = s.Scan(t.Context(), cfg); err == nil || found != nil {
		t.Fatal("malformed last page silently omitted")
	}
}
func TestNativeIPRankingAndWholeWriteValidation(t *testing.T) {
	f := newNativeFixture(t, v2.SpaceIP)
	s := f.store(t)
	nativeUpsert(t, s, &document.Document{ID: "big", Text: "big"}, &document.Document{ID: "small", Text: "query"})
	q := query(vectorstore.SearchOptions{TopK: 2})
	q.Values = nativeVectors("big query")
	found, err := s.Query(t.Context(), q)
	if err != nil || len(found.Results) != 2 || found.Results[0].Document.ID != "big" {
		t.Fatal(found, err)
	}
	bad := nativeBatch([]*document.Document{{ID: "unwritten", Text: "query"}, {ID: "wrong", Text: "query"}})
	bad.Records[1].Values = []float64{math.MaxFloat64, 0}
	if err = s.Upsert(t.Context(), bad); err == nil {
		t.Fatal("narrowing overflow accepted")
	}
	for _, id := range f.ids(t) {
		if id == "unwritten" || id == "wrong" {
			t.Fatal("partial validation published data")
		}
	}
}
func TestNativeStrictRecordsAndCancellation(t *testing.T) {
	f := newNativeFixture(t, v2.SpaceCosine)
	s := f.store(t)
	for _, raw := range []string{`[]`, `{"a":1,"a":2}`} {
		f.replace(t, "bad", raw, "query", []float32{1, 0})
		if _, err := NewStore(t.Context(), StoreConfig{Collection: f, Space: testSpace}); err != nil {
			t.Fatalf("constructor audited source: %v", err)
		}
		if got, err := s.Query(t.Context(), query(vectorstore.SearchOptions{TopK: 1})); err == nil || got != nil {
			t.Fatal(got, err)
		}
		if got, err := s.Scan(t.Context(), ScanConfig{Budget: nativeBudget()}); err == nil || got != nil {
			t.Fatal(got, err)
		}
	}
	f.clear(t)
	if err := f.Upsert(t.Context(), v2.WithIDs("old"), v2.WithTexts("query"), v2.WithEmbeddings(ce.NewEmbeddingFromFloat32([]float32{1, 0})), v2.WithMetadatas(v2.NewDocumentMetadata(v2.NewStringAttribute("old", "flat")))); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Query(t.Context(), query(vectorstore.SearchOptions{})); got != nil || err == nil {
		t.Fatal("flat metadata accepted")
	}
	f.clear(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if got, err := s.Query(ctx, query(vectorstore.SearchOptions{})); got != nil || !errors.Is(err, context.Canceled) {
		t.Fatal(got, err)
	}
	if err := s.DeleteIDs(ctx, []string{"missing"}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	nativeUpsert(t, s, &document.Document{ID: "original/路径", Text: "query"})
	if err := s.DeleteIDs(t.Context(), []string{"original/路径", "original/路径", "missing"}); err != nil || len(f.ids(t)) != 0 {
		t.Fatal(err)
	}
}
