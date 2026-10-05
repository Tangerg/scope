package typesense

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"

	nativesense "github.com/typesense/typesense-go/v3/typesense"
	"github.com/typesense/typesense-go/v3/typesense/api"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

func TestConstructorBindsActualNativeSchema(t *testing.T) {
	for _, fault := range []string{"identity", "extra field", "duplicate", "metadata object", "content not indexed", "not stored", "dimension", "inner product", "auto embedding"} {
		t.Run(fault, func(t *testing.T) {
			fixture := newProtocolFixture(t)
			fields := fixture.schema["fields"].([]any)
			switch fault {
			case "identity":
				fixture.schema["name"] = "other"
			case "extra field":
				fixture.schema["fields"] = append(fields, map[string]any{"name": "another", "type": "string"})
			case "duplicate":
				fixture.schema["fields"] = append(fields, fields[0])
			case "metadata object":
				fields[1].(map[string]any)["type"] = "object"
			case "content not indexed":
				fields[0].(map[string]any)["index"] = false
			case "not stored":
				fields[1].(map[string]any)["store"] = false
			case "dimension":
				fields[2].(map[string]any)["num_dim"] = 0
			case "inner product":
				fields[2].(map[string]any)["vec_dist"] = "ip"
			case "auto embedding":
				fields[2].(map[string]any)["embed"] = map[string]any{"from": []string{"content"}, "model_config": map[string]string{"model_name": "native"}}
			}
			store, err := NewStore(t.Context(), StoreConfig{Client: fixture.client, CollectionName: "documents", EmbeddingModel: constantModel(), DocumentBatcher: testBatcher{}})
			if store != nil || !errors.Is(err, ErrIncompatibleCollection) {
				t.Fatalf("constructor=%#v, error=%v", store, err)
			}
			if fixture.responses.Load() != fixture.closed.Load() {
				t.Fatal("schema response leaked")
			}
		})
	}
}

func TestFullExportRejectsCorruptCurrentRecordsBeforeEmbedding(t *testing.T) {
	for _, fault := range []string{"legacy metadata", "missing", "unknown", "dimension", "zero vector", "invalid key", "key alias", "whitespace ID", "corrupt metadata", "empty content", "duplicate"} {
		t.Run(fault, func(t *testing.T) {
			fixture := newProtocolFixture(t)
			calls := 0
			model := embedding.ModelFunc(func(ctx context.Context, request *embedding.Request) (*embedding.Response, error) {
				calls++
				return constantModel().Call(ctx, request)
			})
			store := fixture.store(model, 32)
			valid := nativeRecord(t, &document.Document{ID: "one", Text: "text"})
			var broken map[string]any
			if err := jsonv2.Unmarshal(valid, &broken); err != nil {
				t.Fatal(err)
			}
			switch fault {
			case "legacy metadata":
				broken["metadata"] = map[string]any{}
			case "missing":
				delete(broken, "metadata")
			case "unknown":
				broken["another"] = true
			case "dimension":
				broken["embedding"] = []float32{1}
			case "zero vector":
				broken["embedding"] = []float32{0, 0}
			case "invalid key":
				broken["id"] = "*"
			case "key alias":
				broken["id"] = "b25l\n"
			case "whitespace ID":
				key, err := encodeKey(" ")
				if err != nil {
					t.Fatal(err)
				}
				broken["id"] = key
			case "corrupt metadata":
				broken["metadata"] = "broken"
			case "empty content":
				broken["content"] = ""
			}
			raw := string(valid) + "\n" + string(mustJSON(t, broken)) + "\n"
			if fault == "duplicate" {
				raw = string(valid) + "\n" + string(valid) + "\n"
			}
			fixture.exportOverride = &raw
			response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{Filter: filter.IsNull("unused")}})
			if response != nil || err == nil || calls != 0 || len(fixture.queries) != 0 {
				t.Fatalf("response=%#v, error=%v, model=%d, native=%d", response, err, calls, len(fixture.queries))
			}
			if fixture.responses.Load() != fixture.closed.Load() {
				t.Fatal("export response leaked")
			}
		})
	}
}

func searchReply(t *testing.T, found int, hits []any) json.RawMessage {
	t.Helper()
	return mustJSON(t, map[string]any{"results": []any{map[string]any{"found": found, "hits": hits, "search_cutoff": false}}})
}

func TestSemanticPaginationValidatesEveryPage(t *testing.T) {
	for _, fault := range []string{"none", "duplicate", "late corrupt", "count changed", "cutoff", "short page", "missing distance"} {
		t.Run(fault, func(t *testing.T) {
			fixture := newProtocolFixture(t)
			store := fixture.store(constantModel(), 32)
			docs := make([]*document.Document, 300)
			for i := range docs {
				docs[i] = &document.Document{ID: fmt.Sprintf("doc_%03d", i), Text: "text"}
			}
			if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: docs}); err != nil {
				t.Fatal(err)
			}
			first, second := make([]any, 250), make([]any, 50)
			for i, doc := range docs {
				hit := map[string]any{"document": nativeRecord(t, doc), "vector_distance": float64(i) / 300}
				if i < 250 {
					first[i] = hit
				} else {
					second[i-250] = hit
				}
			}
			count := 300
			switch fault {
			case "duplicate":
				second[49] = first[0]
			case "late corrupt":
				second[49] = map[string]any{"document": json.RawMessage(`{}`), "vector_distance": 1}
			case "count changed":
				count = 299
			case "short page":
				first = first[:249]
			case "missing distance":
				second[49] = map[string]any{"document": nativeRecord(t, docs[299])}
			}
			fixture.searchScript = []json.RawMessage{searchReply(t, 300, first), searchReply(t, count, second)}
			if fault == "cutoff" {
				fixture.searchScript[1] = mustJSON(t, map[string]any{"results": []any{map[string]any{"found": 300, "hits": second, "search_cutoff": true}}})
			}
			response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 300, MinScore: 0.1}})
			if fault != "none" {
				if err == nil || response != nil {
					t.Fatalf("partial success: %#v, %v", response, err)
				}
				return
			}
			if err != nil || len(response.Results) != 300 || len(fixture.queries) != 2 {
				t.Fatalf("pagination: %#v, %v", response, err)
			}
			for i, result := range response.Results {
				if result.Document.ID != docs[i].ID {
					t.Fatalf("rank %d changed", i)
				}
			}
		})
	}
}

func TestHybridUsesNativeFusionAndRejectsPaginationBeforeIO(t *testing.T) {
	fixture := newProtocolFixture(t)
	alpha := float32(0.8)
	store, err := NewStore(t.Context(), StoreConfig{Client: fixture.client, CollectionName: "documents", EmbeddingModel: constantModel(), DocumentBatcher: testBatcher{}, HybridAlpha: &alpha})
	if err != nil {
		t.Fatal(err)
	}
	alpha = 0.1
	doc := &document.Document{ID: "one", Text: "lexical"}
	if err = store.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{doc}}); err != nil {
		t.Fatal(err)
	}
	fixture.searchScript = []json.RawMessage{searchReply(t, 1, []any{map[string]any{"document": nativeRecord(t, doc)}})}
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "lexical", Options: vectorstore.SearchOptions{Mode: vectorstore.SearchModeHybrid}})
	if err != nil || len(response.Results) != 1 || response.Results[0].Score != 1 {
		t.Fatalf("lexical-only native fusion: %#v, %v", response, err)
	}
	if !strings.Contains(*fixture.queries[0].VectorQuery, "alpha: 0.8") || *fixture.queries[0].Q != "lexical" || *fixture.queries[0].QueryBy != "content" {
		t.Fatal("hybrid native query changed configured weight or lexical evidence")
	}
	before := fixture.responses.Load()
	response, err = store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{Mode: vectorstore.SearchModeHybrid, TopK: 251}})
	if !errors.Is(err, vectorstore.ErrInvalidOptions) || response != nil || fixture.responses.Load() != before {
		t.Fatalf("unsupported fusion pagination: %#v, %v", response, err)
	}
	for _, value := range []float32{float32(math.NaN()), float32(math.Inf(1)), -1, 2} {
		if err = (StoreConfig{Client: fixture.client, EmbeddingModel: constantModel(), DocumentBatcher: testBatcher{}, HybridAlpha: &value}).Validate(); err == nil {
			t.Fatal("invalid alpha accepted")
		}
	}
}

func TestNativeSearchRequiresCompleteAcknowledgmentAndMembership(t *testing.T) {
	for _, fault := range []string{"missing count", "missing hits", "missing cutoff", "native error", "outside membership", "changed predicate", "extra hit", "invalid distance"} {
		t.Run(fault, func(t *testing.T) {
			fixture := newProtocolFixture(t)
			store := fixture.store(constantModel(), 32)
			doc := &document.Document{ID: "one", Text: "text"}
			if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{doc}}); err != nil {
				t.Fatal(err)
			}
			hit := map[string]any{"document": nativeRecord(t, doc), "vector_distance": 0}
			output := map[string]any{"found": 1, "hits": []any{hit}, "search_cutoff": false}
			switch fault {
			case "missing count":
				delete(output, "found")
			case "missing hits":
				delete(output, "hits")
			case "missing cutoff":
				delete(output, "search_cutoff")
			case "native error":
				output["error"] = "failed"
				output["code"] = 500
			case "outside membership":
				hit["document"] = nativeRecord(t, &document.Document{ID: "other", Text: "text"})
			case "changed predicate":
				hit["document"] = nativeRecord(t, &document.Document{ID: "one", Text: "text", Metadata: metadata.Map{"unused": json.RawMessage(`true`)}})
			case "extra hit":
				output["hits"] = []any{hit, map[string]any{"document": nativeRecord(t, &document.Document{ID: "other", Text: "text"}), "vector_distance": 0}}
			case "invalid distance":
				hit["vector_distance"] = 3
			}
			fixture.searchScript = []json.RawMessage{mustJSON(t, map[string]any{"results": []any{output}})}
			response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 1, Filter: filter.IsNull("unused")}})
			if err == nil || response != nil {
				t.Fatalf("invalid native search succeeded: %#v, %v", response, err)
			}
		})
	}
}

type faultyBody struct {
	reader            io.Reader
	readErr, closeErr error
	closed            int
}

func (f *faultyBody) Read(p []byte) (int, error) {
	if f.readErr != nil {
		return 0, f.readErr
	}
	return f.reader.Read(p)
}
func (f *faultyBody) Close() error { f.closed++; return f.closeErr }

func TestResponseBoundaryOwnsBudgetClosureAndErrors(t *testing.T) {
	cause := errors.New("native failure")
	for _, fault := range []string{"none", "call", "read", "close", "oversize", "http"} {
		t.Run(fault, func(t *testing.T) {
			body := &faultyBody{reader: strings.NewReader("body")}
			response := &http.Response{StatusCode: 200, Body: body}
			var callErr error
			switch fault {
			case "call":
				callErr = cause
			case "read":
				body.readErr = cause
			case "close":
				body.closeErr = cause
			case "oversize":
				body.reader = strings.NewReader("too big")
			case "http":
				response.StatusCode = 503
			}
			raw, err := (&Store{maxResponseBytes: 4}).readResponse(response, callErr)
			if body.closed != 1 {
				t.Fatal("body ownership violated")
			}
			if fault == "none" {
				if err != nil || string(raw) != "body" {
					t.Fatalf("read=%q, %v", raw, err)
				}
				return
			}
			if err == nil || raw != nil {
				t.Fatalf("unexpected success=%q, %v", raw, err)
			}
			if fault == "call" || fault == "read" || fault == "close" {
				if !errors.Is(err, cause) {
					t.Fatalf("lost cause: %v", err)
				}
			}
			if fault == "http" {
				var nativeErr *nativesense.HTTPError
				if !errors.As(err, &nativeErr) || nativeErr.Status != 503 {
					t.Fatalf("lost native status: %v", err)
				}
			}
		})
	}
	var nilBody *faultyBody
	for _, response := range []*http.Response{nil, {StatusCode: 200}, {StatusCode: 200, Body: nilBody}} {
		if raw, err := (&Store{maxResponseBytes: 4}).readResponse(response, nil); err == nil || raw != nil {
			t.Fatal("invalid native response accepted")
		}
	}
}

func TestExplicitDeletionPrevalidatesIDsAndAcknowledgments(t *testing.T) {
	fixture := newProtocolFixture(t)
	store := fixture.store(constantModel(), 32)
	if err := store.DeleteIDs(t.Context(), []string{"one", ""}); err == nil || len(fixture.deletes) != 0 {
		t.Fatal("invalid trailing ID published deletion")
	}
	for _, body := range []string{`{}`, `{"num_deleted":-1}`, `{"num_deleted":2}`, `null`} {
		fixture.deleteOverride = &body
		if err := store.DeleteIDs(t.Context(), []string{"unknown"}); err == nil {
			t.Fatalf("invalid deletion acknowledgment=%s", body)
		}
	}
	var nilClient *api.Client
	if err := (StoreConfig{Client: nilClient, EmbeddingModel: constantModel(), DocumentBatcher: testBatcher{}}).Validate(); err == nil {
		t.Fatal("typed nil native client accepted")
	}
}
