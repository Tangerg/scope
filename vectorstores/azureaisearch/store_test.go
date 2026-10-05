package azureaisearch

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
)

func TestCoreFilterConformanceOverHTTP(t *testing.T) {
	storetest.FilterConformance(t, storetest.FilterConfig{Query: func(ctx context.Context, docs []*document.Document, predicate filter.Predicate) ([]string, error) {
		fixture := newProtocolFixture(t)
		store := fixture.store()
		if err := store.Index(ctx, &vectorstore.IndexRequest{Documents: docs}); err != nil {
			return nil, err
		}
		response, err := store.Search(ctx, &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{Filter: predicate, TopK: len(docs)}})
		if err != nil {
			return nil, err
		}
		ids := make([]string, len(response.Results))
		for i, row := range response.Results {
			ids[i] = row.Document.ID
		}
		return ids, nil
	}})
}

func TestIndexReplacesOneCoreMetadataDocument(t *testing.T) {
	fixture := newProtocolFixture(t)
	store := fixture.store()
	for _, facts := range []metadata.Map{
		rawMetadata(`{"old":1,"nested":{"large":9007199254740993},"precise":1.0000000000000000001,"null":null,"id":"metadata","@search.action":"delete"}`),
		rawMetadata(`{"new":1e1000}`), {}, nil,
	} {
		if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{{ID: "one", Text: "new text", Metadata: facts}}}); err != nil {
			t.Fatal(err)
		}
		response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query"})
		if err != nil {
			t.Fatal(err)
		}
		if len(response.Results) != 1 || !response.Results[0].Document.Metadata.Equal(facts) || (response.Results[0].Document.Metadata == nil) != (facts == nil) {
			t.Fatalf("roundtrip = %#v, want %#v", response, facts)
		}
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	for _, batch := range fixture.writes {
		if len(batch) != 1 || len(batch[0]) != 5 || string(batch[0]["@search.action"]) != `"upload"` {
			t.Fatalf("wire carried merge or flat metadata: %v", batch)
		}
	}
}

func TestSearchPreflightsEveryRecordBeforeEmbedding(t *testing.T) {
	fixture := newProtocolFixture(t)
	fixture.pageSize = 1
	store := fixture.store()
	fixture.put(&document.Document{ID: "a", Text: "good", Metadata: rawMetadata(`{"n":1}`)}, &document.Document{ID: "z", Text: "bad", Metadata: rawMetadata(`{"n":"wrong"}`)})
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{Filter: filter.LT("n", 2), TopK: 1, MinScore: 1}})
	if err == nil || response != nil || fixture.modelCalls.Load() != 0 {
		t.Fatalf("bad late record bypassed preflight: %v, %v, calls=%d", response, err, fixture.modelCalls.Load())
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if len(fixture.bodies) != 3 {
		t.Fatalf("scan bodies = %d", len(fixture.bodies))
	}
	for _, body := range fixture.bodies {
		if _, exists := body["skip"]; exists {
			t.Fatal("keyset scan used skip")
		}
		if _, exists := body["vectorQueries"]; exists {
			t.Fatal("invalid preflight reached ANN")
		}
	}
	if string(fixture.bodies[2]["filter"]) != `"id gt 'a'"` {
		t.Fatalf("scan did not advance by native key: %v", fixture.bodies)
	}
}

func TestSearchKeepsNativeHybridAndCandidateScope(t *testing.T) {
	fixture := newProtocolFixture(t)
	store := fixture.store()
	fixture.put(&document.Document{ID: "one", Text: "Paris", Metadata: rawMetadata(`{"category":"keep"}`)}, &document.Document{ID: "two", Text: "other", Metadata: rawMetadata(`{"category":"omit"}`)})
	fixture.mu.Lock()
	fixture.query = func(writer http.ResponseWriter, body metadata.Map) {
		if string(body["search"]) != `"capital of France"` || string(body["searchFields"]) != `"content"` || string(body["vectorFilterMode"]) != `"preFilter"` || string(body["filter"]) != `"search.in(id, 'one', ',')"` {
			t.Errorf("hybrid evidence = %v", body)
		}
		row := fixture.rows["one"].Clone()
		_ = row.Set("@search.score", .03)
		_ = jsonv2.MarshalWrite(writer, map[string]any{"value": []metadata.Map{row}})
	}
	fixture.mu.Unlock()
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "capital of France", Options: vectorstore.SearchOptions{Mode: vectorstore.SearchModeHybrid, TopK: 1, Filter: filter.EQ("category", "keep")}})
	if err != nil || len(response.Results) != 1 || response.Results[0].Score != .03 {
		t.Fatalf("hybrid response = %v, %v", response, err)
	}
}

func TestSearchCurrentRecordsAndRawRanking(t *testing.T) {
	for _, test := range []string{"replacement", "changed predicate", "wrong type", "unexpected ID", "duplicate ID", "excess rows", "invalid score", "raw score ranking"} {
		t.Run(test, func(t *testing.T) {
			fixture := newProtocolFixture(t)
			fixture.schema = nativeSchema("dotProduct")
			store := fixture.store()
			fixture.put(&document.Document{ID: "a", Text: "initial", Metadata: rawMetadata(`{"n":1}`)}, &document.Document{ID: "z", Text: "initial", Metadata: rawMetadata(`{"n":1}`)})
			fixture.mu.Lock()
			fixture.query = func(writer http.ResponseWriter, _ metadata.Map) {
				a, z := fixture.rows["a"].Clone(), fixture.rows["z"].Clone()
				rows := []metadata.Map{a}
				switch test {
				case "replacement":
					_ = a.Set("content", "replacement")
				case "changed predicate":
					_ = a.Set("scope_metadata", `{"n":9}`)
				case "wrong type":
					_ = a.Set("scope_metadata", `{"n":"wrong"}`)
				case "unexpected ID":
					_ = a.Set("id", "outside")
				case "duplicate ID":
					rows = append(rows, a)
				case "excess rows":
					rows = append(rows, z, a)
				case "invalid score":
					_ = a.Set("@search.score", -1)
				case "raw score ranking":
					_ = a.Set("@search.score", 2)
					_ = z.Set("@search.score", 3)
					rows = append(rows, z)
				}
				_ = jsonv2.MarshalWrite(writer, map[string]any{"value": rows})
			}
			fixture.mu.Unlock()
			response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 2, Filter: filter.LT("n", 2)}})
			switch test {
			case "replacement":
				if err != nil || response.Results[0].Document.Text != "replacement" {
					t.Fatalf("current record = %v, %v", response, err)
				}
			case "changed predicate":
				if err != nil || len(response.Results) != 0 {
					t.Fatalf("changed predicate = %v, %v", response, err)
				}
			case "raw score ranking":
				if err != nil || len(response.Results) != 2 || response.Results[0].Document.ID != "z" || response.Results[0].Score != 1 || response.Results[1].Score != 1 {
					t.Fatalf("raw order = %v, %v", response, err)
				}
			default:
				if err == nil || response != nil {
					t.Fatalf("invalid response = %v, %v", response, err)
				}
			}
		})
	}
}

func TestIndexPreparesEveryBatchBeforeFirstWrite(t *testing.T) {
	for _, test := range []string{"model error", "wrong width", "narrowing overflow", "invalid ID", "media"} {
		t.Run(test, func(t *testing.T) {
			fixture := newProtocolFixture(t)
			fixture.model = embedding.ModelFunc(func(_ context.Context, request *embedding.Request) (*embedding.Response, error) {
				fixture.modelCalls.Add(1)
				vector := []float64{1, 0}
				if request.Texts[0] == "bad" {
					switch test {
					case "model error":
						return nil, errors.New("model failed")
					case "wrong width":
						vector = []float64{1}
					case "narrowing overflow":
						vector = []float64{math.MaxFloat64, 0}
					}
				}
				return embedding.NewResponse([]*embedding.Output{{Embedding: vector}}, nil)
			})
			config := fixture.config()
			config.DocumentBatcher = writeTestBatcher{single: true}
			store, err := NewStore(t.Context(), config)
			if err != nil {
				t.Fatal(err)
			}
			docs := []*document.Document{{ID: "one", Text: "good"}, {ID: "two", Text: "bad"}}
			if test == "invalid ID" {
				docs[1].ID = "two' injected"
			}
			if test == "media" {
				docs[1].Media = &media.Media{MIME: "text/plain", Source: media.Source{Kind: media.SourceBytes, Bytes: []byte("media")}}
			}
			if err = store.Index(t.Context(), &vectorstore.IndexRequest{Documents: docs}); err == nil {
				t.Fatal("invalid late batch succeeded")
			}
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			if len(fixture.writes) != 0 {
				t.Fatal("preflight failure published an earlier batch")
			}
			if (test == "invalid ID" || test == "media") && fixture.modelCalls.Load() != 0 {
				t.Fatal("invalid source reached model")
			}
		})
	}
}

func TestExplicitDeleteIDsAndServiceWriteLimits(t *testing.T) {
	fixture := newProtocolFixture(t)
	store := fixture.store()
	docs := make([]*document.Document, maximumDocumentsPerBatch+1)
	for i := range docs {
		docs[i] = &document.Document{ID: fmt.Sprint(i), Text: "document"}
	}
	if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: docs}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteIDs(t.Context(), []string{"0", "0", "unknown"}); err != nil {
		t.Fatal(err)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if len(fixture.writes) != 3 || len(fixture.writes[0]) != 1000 || len(fixture.writes[1]) != 1 || len(fixture.writes[2]) != 2 {
		t.Fatalf("write batches = %v", fixture.writes)
	}
	if _, exists := fixture.rows["0"]; exists {
		t.Fatal("explicit deletion failed")
	}
	if len(fixture.rows) != 1000 {
		t.Fatal("explicit deletion changed unrelated documents")
	}
}

func TestWritesRequireCompleteAcknowledgments(t *testing.T) {
	for _, operation := range []string{"index", "delete"} {
		for _, test := range []struct {
			name, body string
			valid      bool
		}{
			{"out of order", `{"value":[{"key":"two","status":true,"statusCode":200},{"key":"one","status":true,"statusCode":201}]}`, true},
			{"partial", `{"value":[{"key":"one","status":true,"statusCode":201},{"key":"two","status":false,"statusCode":503,"errorMessage":"busy"}]}`, false},
			{"missing", `{"value":[{"key":"one","status":true,"statusCode":201}]}`, false},
			{"duplicate", `{"value":[{"key":"one","status":true,"statusCode":201},{"key":"one","status":true,"statusCode":200}]}`, false},
			{"unknown", `{"value":[{"key":"one","status":true,"statusCode":201},{"key":"other","status":true,"statusCode":200}]}`, false},
			{"contradictory status", `{"value":[{"key":"one","status":true,"statusCode":503},{"key":"two","status":true,"statusCode":200}]}`, false},
		} {
			t.Run(operation+"/"+test.name, func(t *testing.T) {
				fixture := newProtocolFixture(t)
				store := fixture.store()
				fixture.mu.Lock()
				fixture.write = func(writer http.ResponseWriter, _ []metadata.Map) {
					body := test.body
					if operation == "delete" {
						body = strings.ReplaceAll(body, "201", "200")
					}
					fmt.Fprint(writer, body)
				}
				fixture.mu.Unlock()
				var err error
				if operation == "index" {
					err = store.Index(t.Context(), &vectorstore.IndexRequest{Documents: []*document.Document{{ID: "one", Text: "one"}, {ID: "two", Text: "two"}}})
				} else {
					err = store.DeleteIDs(t.Context(), []string{"one", "two"})
				}
				if (err == nil) != test.valid {
					t.Fatalf("write error = %v", err)
				}
				if test.name == "partial" && !strings.Contains(err.Error(), "busy") {
					t.Fatalf("provider failure lost: %v", err)
				}
			})
		}
	}
}

func TestSearchContinuationAndCredentialOrigin(t *testing.T) {
	for _, test := range []string{"two pages", "missing parameters", "repeated parameters"} {
		t.Run(test, func(t *testing.T) {
			fixture := newProtocolFixture(t)
			store := fixture.store()
			fixture.put(&document.Document{ID: "one", Text: "one"}, &document.Document{ID: "two", Text: "two"})
			calls := 0
			fixture.mu.Lock()
			fixture.query = func(writer http.ResponseWriter, body metadata.Map) {
				calls++
				if calls == 1 || test == "repeated parameters" {
					next := map[string]any{"skip": 1, "filter": "search.in(id, 'one,two', ',')"}
					response := map[string]any{"value": []metadata.Map{fixture.rows["one"]}, "@odata.nextLink": "https://untrusted.invalid/steal"}
					if test != "missing parameters" {
						response["@search.nextPageParameters"] = next
					}
					_ = jsonv2.MarshalWrite(writer, response)
					return
				}
				if string(body["skip"]) != "1" {
					t.Error("continuation omitted POST parameters")
				}
				_ = jsonv2.MarshalWrite(writer, map[string]any{"value": []metadata.Map{fixture.rows["two"]}})
			}
			fixture.mu.Unlock()
			response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 2}})
			if test == "two pages" {
				if err != nil || len(response.Results) != 2 || calls != 2 {
					t.Fatalf("paged query = %v, %v, calls=%d", response, err, calls)
				}
			} else if err == nil || response != nil {
				t.Fatalf("invalid continuation succeeded: %v, %v", response, err)
			}
		})
	}
}

func TestConstructionRejectsMalformedStoredFacts(t *testing.T) {
	for _, test := range []string{"flat metadata", "null metadata column", "invalid JSON", "wrong vector width", "missing text", "unknown field", "invalid key"} {
		t.Run(test, func(t *testing.T) {
			fixture := newProtocolFixture(t)
			fixture.put(&document.Document{ID: "one", Text: "one"})
			fixture.mu.Lock()
			row := fixture.rows["one"]
			switch test {
			case "flat metadata":
				delete(row, "scope_metadata")
				_ = row.Set("category", "flat")
			case "null metadata column":
				row["scope_metadata"] = json.RawMessage(`null`)
			case "invalid JSON":
				_ = row.Set("scope_metadata", `{"n":`)
			case "wrong vector width":
				_ = row.Set("contentVector", []float32{1})
			case "missing text":
				delete(row, "content")
			case "unknown field":
				_ = row.Set("other", 1)
			case "invalid key":
				_ = row.Set("id", "_one")
			}
			fixture.mu.Unlock()
			store, err := NewStore(t.Context(), fixture.config())
			if err == nil || store != nil || fixture.modelCalls.Load() != 0 {
				t.Fatalf("invalid persisted record accepted: %v, %v", store, err)
			}
		})
	}
}

func TestSchemaIsSoleMetricAndDimensionOwner(t *testing.T) {
	for _, metric := range []string{"cosine", "dotProduct", "euclidean"} {
		t.Run(metric, func(t *testing.T) {
			fixture := newProtocolFixture(t)
			fixture.schema = nativeSchema(metric)
			store := fixture.store()
			if store.metric != nativeMetric(metric) || store.dimensions != 2 || fixture.modelCalls.Load() != 0 {
				t.Fatalf("native schema projection = %v", store)
			}
		})
	}
	for _, change := range []string{
		`"dimensions":2`, `"key":true`, `"filterable":true`, `"sortable":true`, `"vectorSearchProfile":"p"`, `"searchable":true`,
	} {
		t.Run("missing "+change, func(t *testing.T) {
			fixture := newProtocolFixture(t)
			fixture.schema = strings.ReplaceAll(fixture.schema, change, strings.Split(change, ":")[0]+":null")
			store, err := NewStore(t.Context(), fixture.config())
			if store != nil || !errors.Is(err, ErrIncompatibleIndex) {
				t.Fatalf("invalid schema = %v, %v", store, err)
			}
		})
	}
	for _, algorithm := range []string{
		`{"name":"a","kind":"hnsw","exhaustiveKnnParameters":{"metric":"cosine"}}`,
		`{"name":"a","kind":"hnsw","hnswParameters":{"metric":"cosine"},"exhaustiveKnnParameters":{"metric":"cosine"}}`,
		`{"name":"a","hnswParameters":{"metric":"cosine"}}`,
		`{"name":"a","kind":"hnsw","hnswParameters":{}}`,
		`{"name":"a","kind":"hnsw","hnswParameters":{"metric":"future"}}`,
	} {
		t.Run(algorithm, func(t *testing.T) {
			fixture := newProtocolFixture(t)
			fixture.schema = strings.Replace(fixture.schema, `{"name":"a","kind":"hnsw","hnswParameters":{"metric":"cosine"}}`, algorithm, 1)
			store, err := NewStore(t.Context(), fixture.config())
			if store != nil || !errors.Is(err, ErrIncompatibleIndex) {
				t.Fatalf("competing algorithm facts = %v, %v", store, err)
			}
		})
	}
}

func TestNativeScoreValidation(t *testing.T) {
	for _, test := range []struct {
		metric    nativeMetric
		raw, want float64
	}{
		{metricCosine, 1, 1}, {metricCosine, .5, .5}, {metricCosine, 1.0 / 3, 0}, {metricDot, .5, .5}, {metricEuclidean, .25, .25},
	} {
		score, err := test.metric.score(test.raw, vectorstore.SearchModeSemantic)
		if err != nil || math.Abs(score.Float64()-test.want) > 1e-12 {
			t.Fatalf("score = %v, %v", score, err)
		}
	}
	for _, raw := range []float64{-1, math.NaN(), math.Inf(1), 0, .2, 2} {
		if _, err := metricCosine.score(raw, vectorstore.SearchModeSemantic); err == nil {
			t.Fatalf("invalid cosine score %v accepted", raw)
		}
	}
}

func TestScanRejectsRepeatedKeysAndTransportFailures(t *testing.T) {
	for _, test := range []string{"repeat", "missing array", "transport"} {
		t.Run(test, func(t *testing.T) {
			fixture := newProtocolFixture(t)
			store := fixture.store()
			fixture.put(&document.Document{ID: "one", Text: "one"})
			fixture.mu.Lock()
			fixture.scan = func(writer http.ResponseWriter, _ metadata.Map) {
				switch test {
				case "repeat":
					_ = jsonv2.MarshalWrite(writer, map[string]any{"value": []metadata.Map{fixture.rows["one"]}})
				case "missing array":
					fmt.Fprint(writer, `{}`)
				case "transport":
					writer.WriteHeader(http.StatusServiceUnavailable)
					fmt.Fprint(writer, `{"error":"native failure"}`)
				}
			}
			fixture.mu.Unlock()
			response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query"})
			if err == nil || response != nil || fixture.modelCalls.Load() != 0 {
				t.Fatalf("invalid scan bypassed boundary = %v, %v", response, err)
			}
		})
	}
}

func TestCancellationAndInvalidRequestsAvoidPublication(t *testing.T) {
	fixture := newProtocolFixture(t)
	store := fixture.store()
	fixture.put(&document.Document{ID: "one", Text: "one"})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if response, err := store.Search(ctx, &vectorstore.SearchRequest{Query: "query"}); !errors.Is(err, context.Canceled) || response != nil {
		t.Fatalf("cancellation lost = %v, %v", response, err)
	}
	if err := store.DeleteIDs(t.Context(), []string{"one", "unsupported/key"}); err == nil {
		t.Fatal("invalid later key accepted")
	}
	if response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 1001}}); err == nil || response != nil {
		t.Fatal("native top K limit ignored")
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if len(fixture.writes) != 0 || fixture.modelCalls.Load() != 0 {
		t.Fatal("invalid request mutated records or called model")
	}
}

func TestIndexWireBudgetPreflightCoversEveryNativeChunk(t *testing.T) {
	fixture := newProtocolFixture(t)
	store := fixture.store()
	docs := make([]*document.Document, 1001)
	for i := range docs {
		docs[i] = &document.Document{ID: fmt.Sprint(i), Text: "document"}
	}
	docs[1000].Text = strings.Repeat("x", maximumRequestBytes)
	if err := store.Index(t.Context(), &vectorstore.IndexRequest{Documents: docs}); err == nil {
		t.Fatal("oversized late chunk accepted")
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if len(fixture.writes) != 0 {
		t.Fatal("oversized late chunk published an earlier chunk")
	}
}

func TestConfigRejectsInvalidNativeBindings(t *testing.T) {
	fixture := newProtocolFixture(t)
	for _, change := range []func(*StoreConfig){
		func(config *StoreConfig) { config.HTTPClient = nil }, func(config *StoreConfig) { config.MetadataField = config.IDField },
		func(config *StoreConfig) { config.IDField = "id' injected" }, func(config *StoreConfig) { config.IDField = "azureSearchReserved" },
		func(config *StoreConfig) { config.Endpoint = "https://service.invalid/path" }, func(config *StoreConfig) { config.MaxResponseBytes = math.MaxInt64 },
	} {
		config := fixture.config()
		config.IDField = DefaultIDField
		change(&config)
		if err := config.Validate(); err == nil {
			t.Fatal("invalid native binding accepted")
		}
	}
}

func TestSchemaRefusesCompetingStorageAndNativeDefinitions(t *testing.T) {
	for _, test := range []string{"extra storage field", "duplicate field", "normalized key", "hidden field", "unstored vector", "indexed JSON", "duplicate profile", "duplicate algorithm", "missing algorithm", "missing profile"} {
		t.Run(test, func(t *testing.T) {
			fixture := newProtocolFixture(t)
			var schema map[string]any
			if err := jsonv2.Unmarshal([]byte(fixture.schema), &schema); err != nil {
				t.Fatal(err)
			}
			fields := schema["fields"].([]any)
			native := schema["vectorSearch"].(map[string]any)
			switch test {
			case "extra storage field":
				schema["fields"] = append(fields, map[string]any{"name": "old", "type": "Edm.String"})
			case "duplicate field":
				schema["fields"] = append(fields[:3], fields[0])
			case "normalized key":
				fields[0].(map[string]any)["normalizer"] = "lowercase"
			case "hidden field":
				fields[3].(map[string]any)["retrievable"] = false
			case "unstored vector":
				fields[2].(map[string]any)["stored"] = false
			case "indexed JSON":
				fields[3].(map[string]any)["filterable"] = true
			case "duplicate profile":
				profiles := native["profiles"].([]any)
				native["profiles"] = append(profiles, profiles[0])
			case "duplicate algorithm":
				algorithms := native["algorithms"].([]any)
				native["algorithms"] = append(algorithms, algorithms[0])
			case "missing algorithm":
				native["algorithms"] = []any{}
			case "missing profile":
				native["profiles"] = []any{}
			}
			encoded, err := jsonv2.Marshal(schema)
			if err != nil {
				t.Fatal(err)
			}
			fixture.schema = string(encoded)
			store, err := NewStore(t.Context(), fixture.config())
			if store != nil || !errors.Is(err, ErrIncompatibleIndex) {
				t.Fatalf("competing schema = %v, %v", store, err)
			}
		})
	}
	fixture := newProtocolFixture(t)
	fixture.schema = strings.Replace(fixture.schema, `"kind":"hnsw","hnswParameters"`, `"kind":"exhaustiveKnn","exhaustiveKnnParameters"`, 1)
	if _, err := NewStore(t.Context(), fixture.config()); err != nil {
		t.Fatalf("native exhaustive KNN rejected: %v", err)
	}
}

func TestSearchRejectsBadRecordBeyondNativePageLimit(t *testing.T) {
	fixture := newProtocolFixture(t)
	store := fixture.store()
	docs := make([]*document.Document, 1001)
	for i := range docs {
		docs[i] = &document.Document{ID: fmt.Sprintf("id-%04d", i), Text: "document", Metadata: rawMetadata(`{"n":1}`)}
	}
	docs[1000].Metadata = rawMetadata(`{"n":"bad"}`)
	fixture.put(docs...)
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 1, Filter: filter.LT("n", 2)}})
	if err == nil || response != nil || fixture.modelCalls.Load() != 0 {
		t.Fatalf("late bad record hidden: %v, %v", response, err)
	}
}

func TestDeleteIDsRejectsCreationAcknowledgment(t *testing.T) {
	fixture := newProtocolFixture(t)
	store := fixture.store()
	fixture.mu.Lock()
	fixture.write = func(writer http.ResponseWriter, _ []metadata.Map) {
		fmt.Fprint(writer, `{"value":[{"key":"one","status":true,"statusCode":201}]}`)
	}
	fixture.mu.Unlock()
	if err := store.DeleteIDs(t.Context(), []string{"one"}); err == nil {
		t.Fatal("delete accepted a creation acknowledgment")
	}
}

type authenticatedTestTransport struct{ base http.RoundTripper }

func (a authenticatedTestTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	authenticated := request.Clone(request.Context())
	authenticated.Header.Set("Authorization", "Bearer host-token")
	return a.base.RoundTrip(authenticated)
}

func TestHostOwnsAuthenticationAndHTTPClientLifecycle(t *testing.T) {
	fixture := newProtocolFixture(t)
	fixture.expectedAuthorization = "Bearer host-token"
	config := fixture.config()
	config.HTTPClient = &http.Client{Transport: authenticatedTestTransport{base: config.HTTPClient.Transport}}
	store, err := NewStore(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	if _, closer := any(store).(interface{ Close() error }); closer {
		t.Fatal("store acquired a second client lifecycle")
	}
	if _, deleter := any(store).(vectorstore.FilterDeleter); deleter {
		t.Fatal("store advertised deletion without native CAS")
	}
	if err = store.DeleteIDs(t.Context(), []string{"unknown"}); err != nil {
		t.Fatal(err)
	}
}
