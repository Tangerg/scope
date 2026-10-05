package azurecosmos

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

func TestContainerOwnsNativeVectorPolicy(t *testing.T) {
	for _, function := range []azcosmos.VectorDistanceFunction{azcosmos.VectorDistanceFunctionCosine, azcosmos.VectorDistanceFunctionDotProduct, azcosmos.VectorDistanceFunctionEuclidean} {
		t.Run(string(function), func(t *testing.T) {
			fixture := newProtocolFixture(t, function)
			store := fixture.store(constantModel(), 32)
			fixture.records["a"] = storedDocument(t, &document.Document{ID: "a", Text: "a"})
			fixture.records["b"] = storedDocument(t, &document.Document{ID: "b", Text: "b"})
			fixture.scores["a"] = 0.3
			fixture.scores["b"] = 0.7
			response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query"})
			if err != nil {
				t.Fatal(err)
			}
			want := "b"
			if function == azcosmos.VectorDistanceFunctionEuclidean {
				want = "a"
			}
			if response.Results[0].Document.ID != want {
				t.Fatalf("native metric selected %s, want %s", response.Results[0].Document.ID, want)
			}
			for _, query := range fixture.queries {
				if strings.Contains(query.Query, "distanceFunction") || strings.Contains(query.Query, "dataType") {
					t.Fatalf("query supplied a competing native rule: %s", query.Query)
				}
			}
		})
	}
	for _, fault := range []string{"identity", "partition", "partition kind", "policy", "path", "duplicate", "datatype", "dimensions", "metric", "legacy record"} {
		t.Run(fault, func(t *testing.T) {
			fixture := newProtocolFixture(t, azcosmos.VectorDistanceFunctionCosine)
			policy := fixture.properties["vectorEmbeddingPolicy"].(map[string]any)["vectorEmbeddings"].([]any)[0].(map[string]any)
			switch fault {
			case "identity":
				fixture.properties["id"] = "other"
			case "partition":
				fixture.properties["partitionKey"].(map[string]any)["paths"] = []string{"/tenant"}
			case "partition kind":
				fixture.properties["partitionKey"].(map[string]any)["kind"] = "MultiHash"
			case "policy":
				delete(fixture.properties, "vectorEmbeddingPolicy")
			case "path":
				policy["path"] = "/other"
			case "duplicate":
				fixture.properties["vectorEmbeddingPolicy"].(map[string]any)["vectorEmbeddings"] = []any{policy, policy}
			case "datatype":
				policy["dataType"] = "float16"
			case "dimensions":
				policy["dimensions"] = 0
			case "metric":
				policy["distanceFunction"] = "unknown"
			case "legacy record":
				fixture.records["old"] = json.RawMessage(`{"id":"old","content":"old","partition_key":"library","metadata":{},"embedding":[1,0],"_etag":"old"}`)
			}
			calls := 0
			model := embedding.ModelFunc(func(ctx context.Context, request *embedding.Request) (*embedding.Response, error) {
				calls++
				return constantModel().Call(ctx, request)
			})
			store, err := NewStore(t.Context(), StoreConfig{Container: fixture.container, PartitionKey: "library", EmbeddingModel: model, DocumentBatcher: testBatcher{}})
			if err == nil || store != nil || calls != 0 {
				t.Fatalf("NewStore = %v, %v; model calls = %d", store, err, calls)
			}
			if fault != "legacy record" && !errors.Is(err, ErrIncompatibleContainer) {
				t.Fatalf("native policy error lost identity: %v", err)
			}
		})
	}
}

func TestSearchValidatesFullPartitionBeforeEmbedding(t *testing.T) {
	for _, fault := range []string{"metadata", "dimension", "unknown field", "partition", "etag", "duplicate ID", "whitespace ID", "predicate type", "token"} {
		t.Run(fault, func(t *testing.T) {
			fixture := newProtocolFixture(t, azcosmos.VectorDistanceFunctionCosine)
			calls := 0
			model := embedding.ModelFunc(func(ctx context.Context, request *embedding.Request) (*embedding.Response, error) {
				calls++
				return constantModel().Call(ctx, request)
			})
			store := fixture.store(model, 32)
			first := storedDocument(t, &document.Document{ID: "one", Text: "first"})
			second := storedDocument(t, &document.Document{ID: "two", Text: "second"})
			var fields map[string]json.RawMessage
			if err := jsonv2.Unmarshal(second, &fields); err != nil {
				t.Fatal(err)
			}
			predicate := filter.IsNull("absent")
			switch fault {
			case "metadata":
				fields["metadata"] = json.RawMessage(`"{broken"`)
			case "dimension":
				fields["embedding"] = json.RawMessage(`[1]`)
			case "unknown field":
				fields["legacy"] = json.RawMessage(`true`)
			case "partition":
				fields["partition_key"] = json.RawMessage(`"elsewhere"`)
			case "etag":
				fields["_etag"] = json.RawMessage(`"*"`)
			case "duplicate ID":
				second = first
			case "whitespace ID":
				fields["id"] = json.RawMessage(`" "`)
			case "predicate type":
				fields["metadata"] = mustJSON(t, `{"value":42}`)
				predicate = filter.Like("value", "%")
			}
			if fault != "duplicate ID" {
				second = mustJSON(t, fields)
			}
			fixture.scanScript = []queryPage{{items: []json.RawMessage{first}, token: "next"}, {items: []json.RawMessage{}, token: "last"}, {items: []json.RawMessage{second}}}
			if fault == "token" {
				fixture.scanScript = []queryPage{{items: []json.RawMessage{}, token: "loop"}, {items: []json.RawMessage{}, token: "loop"}}
			}
			response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{Filter: predicate, TopK: 1}})
			if err == nil || response != nil || calls != 0 {
				t.Fatalf("Search = %#v, %v; model calls = %d", response, err, calls)
			}
		})
	}
}

func TestSearchCompletesNativePagesBeforeTruncation(t *testing.T) {
	for _, fault := range []string{"none", "late item", "missing score", "late score", "duplicate", "cycle", "transport", "outside membership", "changed membership"} {
		t.Run(fault, func(t *testing.T) {
			fixture := newProtocolFixture(t, azcosmos.VectorDistanceFunctionDotProduct)
			store := fixture.store(constantModel(), 32)
			first := storedDocument(t, &document.Document{ID: "b", Text: "first", Metadata: metadata.Map{"eligible": json.RawMessage(`true`)}})
			second := storedDocument(t, &document.Document{ID: "a", Text: "second", Metadata: metadata.Map{"eligible": json.RawMessage(`true`)}})
			fixture.records["a"] = second
			fixture.records["b"] = first
			early := mustJSON(t, map[string]any{"item": first, "score": 1000})
			late := mustJSON(t, map[string]any{"item": second, "score": 1001})
			last := queryPage{items: []json.RawMessage{late}}
			switch fault {
			case "late item":
				last.items = []json.RawMessage{json.RawMessage(`{"item":{},"score":0}`)}
			case "missing score":
				last.items = []json.RawMessage{mustJSON(t, map[string]any{"item": second})}
			case "late score":
				last.items = []json.RawMessage{json.RawMessage(`{"item":{},"score":1e1000}`)}
			case "duplicate":
				last.items = []json.RawMessage{early}
			case "cycle":
				last.items = []json.RawMessage{}
				last.token = "next"
			case "transport":
				last.items = []json.RawMessage{}
				last.status = http.StatusForbidden
			case "outside membership":
				last.items = []json.RawMessage{mustJSON(t, map[string]any{"item": storedDocument(t, &document.Document{ID: "outside", Text: "other"}), "score": 1001})}
			case "changed membership":
				last.items = []json.RawMessage{mustJSON(t, map[string]any{"item": storedDocument(t, &document.Document{ID: "a", Text: "other", Metadata: metadata.Map{"eligible": json.RawMessage(`false`)}}), "score": 1001})}
			}
			fixture.rankScript = []queryPage{{items: []json.RawMessage{early}, token: "next"}, {items: []json.RawMessage{}, token: "last"}, last}
			request := &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 1, Filter: filter.EQ("eligible", true)}}
			response, err := store.Search(t.Context(), request)
			if fault == "none" {
				if err != nil || response.Results[0].Document.ID != "a" {
					t.Fatalf("raw native rank lost after saturated score projection: %#v, %v", response, err)
				}
				return
			}
			if err == nil || response != nil {
				t.Fatalf("Search = %#v, %v", response, err)
			}
		})
	}
}

func TestGroupedNativeSelectionRanksGlobally(t *testing.T) {
	fixture := newProtocolFixture(t, azcosmos.VectorDistanceFunctionCosine)
	fixture.pageSize = 1000
	store := fixture.store(constantModel(), 32)
	for i := range 1201 {
		id := fmt.Sprintf("%04d", i)
		fixture.records[id] = storedDocument(t, &document.Document{ID: id, Text: "text"})
	}
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{Filter: filter.IsNull("missing"), TopK: 1201}})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1201 || response.Results[0].Document.ID != "0000" || response.Results[1200].Document.ID != "1200" {
		t.Fatalf("grouped query returned %d unexpected results", len(response.Results))
	}
	var counts []int
	for _, query := range fixture.queries {
		if query.Query == "SELECT VALUE c FROM c" {
			continue
		}
		counts = append(counts, len(query.Parameters)-2)
	}
	want := slices.Repeat([]int{100}, 12)
	want = append(want, 1)
	if !slices.Equal(counts, want) {
		t.Fatalf("native query groups = %v", counts)
	}
}

func TestDeleteWhereUsesObservedETag(t *testing.T) {
	for _, fault := range []string{"none", "changed", "late corrupt", "missing predicate"} {
		t.Run(fault, func(t *testing.T) {
			fixture := newProtocolFixture(t, azcosmos.VectorDistanceFunctionCosine)
			store := fixture.store(constantModel(), 32)
			fixture.records["one"] = storedDocument(t, &document.Document{ID: "one", Text: "text", Metadata: metadata.Map{"eligible": json.RawMessage(`true`)}})
			predicate := filter.EQ("eligible", true)
			if fault == "changed" {
				fixture.deleteHook = func(w http.ResponseWriter, r *http.Request) bool {
					if r.Header.Get("If-Match") != `"original"` {
						t.Errorf("missing observed conditional deletion: %s", r.Header.Get("If-Match"))
					}
					fixture.records["one"] = storedDocument(t, &document.Document{ID: "one", Text: "changed", Metadata: metadata.Map{"eligible": json.RawMessage(`false`)}})
					w.WriteHeader(http.StatusPreconditionFailed)
					fmt.Fprint(w, `{"code":"PreconditionFailed","message":"changed"}`)
					return true
				}
			}
			if fault == "late corrupt" {
				fixture.records["two"] = json.RawMessage(`{"id":"two","metadata":{}}`)
			}
			if fault == "missing predicate" {
				predicate = nil
			}
			err := store.DeleteWhere(t.Context(), predicate)
			if fault == "none" {
				if err != nil || len(fixture.records) != 0 || !slices.Equal(fixture.deletes, []string{"one"}) {
					t.Fatalf("DeleteWhere = %v, deletes %v", err, fixture.deletes)
				}
				return
			}
			if err == nil || len(fixture.deletes) != 0 || fixture.records["one"] == nil {
				t.Fatalf("DeleteWhere = %v, deleted %v", err, fixture.deletes)
			}
			if fault == "changed" {
				var nativeErr *azcore.ResponseError
				if !errors.As(err, &nativeErr) || nativeErr.StatusCode != 412 {
					t.Fatalf("precondition error lost: %v", err)
				}
			}
		})
	}
}

func TestNativeScoresAreValidated(t *testing.T) {
	for _, test := range []struct {
		function azcosmos.VectorDistanceFunction
		raw      float64
		want     float64
	}{{azcosmos.VectorDistanceFunctionCosine, -1, 0}, {azcosmos.VectorDistanceFunctionCosine, 1, 1}, {azcosmos.VectorDistanceFunctionDotProduct, 0, 0.5}, {azcosmos.VectorDistanceFunctionEuclidean, 3, 0.25}} {
		score, err := (containerSchema{function: test.function}).score(test.raw)
		if err != nil || score.Float64() != test.want {
			t.Fatalf("score = %v, %v", score, err)
		}
	}
	for _, test := range []struct {
		function azcosmos.VectorDistanceFunction
		raw      float64
	}{{azcosmos.VectorDistanceFunctionCosine, 2}, {azcosmos.VectorDistanceFunctionEuclidean, -1}, {azcosmos.VectorDistanceFunctionDotProduct, math.Inf(1)}, {azcosmos.VectorDistanceFunctionDotProduct, math.NaN()}, {"unknown", 1}} {
		if _, err := (containerSchema{function: test.function}).score(test.raw); err == nil {
			t.Fatalf("invalid native score accepted: %+v", test)
		}
	}
}

func TestSearchRejectsUnsupportedModeBeforeIO(t *testing.T) {
	fixture := newProtocolFixture(t, azcosmos.VectorDistanceFunctionCosine)
	calls := 0
	store := fixture.store(embedding.ModelFunc(func(ctx context.Context, request *embedding.Request) (*embedding.Response, error) {
		calls++
		return constantModel().Call(ctx, request)
	}), 32)
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{Mode: vectorstore.SearchModeHybrid}})
	if err == nil || response != nil || calls != 0 || len(fixture.queries) != 0 {
		t.Fatalf("Search = %#v, %v; calls=%d queries=%d", response, err, calls, len(fixture.queries))
	}
	cause := errors.New("canceled context")
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(cause)
	response, err = store.Search(ctx, &vectorstore.SearchRequest{Query: "query"})
	if err == nil || response != nil {
		t.Fatalf("canceled Search = %#v, %v", response, err)
	}
}

func TestStoreConfigAndPartitionBudgets(t *testing.T) {
	for _, change := range []func(*StoreConfig){func(c *StoreConfig) { c.Container = nil }, func(c *StoreConfig) { c.PartitionKey = "" }, func(c *StoreConfig) { c.PartitionKey = string([]byte{0xff}) }, func(c *StoreConfig) { c.PartitionKey = strings.Repeat("x", 2049) }, func(c *StoreConfig) { c.EmbeddingModel = nil }, func(c *StoreConfig) { c.DocumentBatcher = nil }} {
		fixture := newProtocolFixture(t, azcosmos.VectorDistanceFunctionCosine)
		config := StoreConfig{Container: fixture.container, PartitionKey: "library", EmbeddingModel: constantModel(), DocumentBatcher: testBatcher{}}
		change(&config)
		if err := config.Validate(); err == nil {
			t.Fatal("invalid config accepted")
		}
		if store, err := NewStore(t.Context(), config); err == nil || store != nil {
			t.Fatalf("invalid constructor = %v, %v", store, err)
		}
	}
	for _, test := range []struct {
		version int
		bytes   int
		valid   bool
	}{{1, 101, true}, {1, 102, false}, {2, 2048, true}, {3, 100, false}} {
		fixture := newProtocolFixture(t, azcosmos.VectorDistanceFunctionCosine)
		fixture.properties["partitionKey"].(map[string]any)["version"] = test.version
		fixture.partition = strings.Repeat("x", test.bytes)
		store, err := NewStore(t.Context(), StoreConfig{Container: fixture.container, PartitionKey: strings.Repeat("x", test.bytes), EmbeddingModel: constantModel(), DocumentBatcher: testBatcher{}})
		if test.valid {
			if err != nil || store == nil {
				t.Fatalf("native partition budget rejected %+v: %v", test, err)
			}
		} else if err == nil || store != nil {
			t.Fatalf("native partition budget accepted %+v", test)
		}
	}
}
