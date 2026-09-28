package elasticsearch

import (
	"context"
	jsonv2 "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/elastic/go-elasticsearch/v8"
	"github.com/elastic/go-elasticsearch/v8/esapi"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/embeddingclient"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

func TestMetadataSelectionPreservesCorePredicates(t *testing.T) {
	for _, sample := range []struct {
		source string
		want   []string
	}{
		{`value == 'a'`, []string{"scalar"}},
		{`value has 'a'`, []string{"array"}},
		{`value in ('a','b')`, []string{"scalar"}},
		{`value is null`, []string{"missing", "null"}},
		{`value is not null`, []string{"array", "empty", "multiline", "scalar", "text"}},
		{`name like 'never OR metadata.guard:%'`, nil},
		{`name like 'Alice'`, nil},
		{`name like 'A%'`, []string{"text"}},
		{`name like 'f_o'`, []string{"multiline"}},
	} {
		t.Run(sample.source, func(t *testing.T) {
			predicate, err := filter.Parse(sample.source)
			if err != nil {
				t.Fatal(err)
			}
			for _, operation := range []string{"delete", "search"} {
				t.Run(operation, func(t *testing.T) {
					var selected []string
					scrollPage := 0
					cleared := false
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.Header().Set("Content-Type", "application/json")
						w.Header().Set("X-Elastic-Product", "Elasticsearch")
						switch {
						case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "_search/scroll"):
							cleared = true
							_, _ = io.WriteString(w, `{"succeeded":true,"num_freed":1}`)
						case strings.HasSuffix(r.URL.Path, "_search/scroll"):
							scrollPage++
							if scrollPage == 1 {
								_, _ = io.WriteString(w, metadataFixturePageTwo)
							} else {
								_, _ = io.WriteString(w, `{"_scroll_id":"snapshot","hits":{"hits":[]}}`)
							}
						case strings.HasSuffix(r.URL.Path, "_search") && r.URL.Query().Get("scroll") != "":
							var body map[string]any
							if err := jsonv2.UnmarshalRead(r.Body, &body); err != nil {
								t.Error(err)
							}
							if body["seq_no_primary_term"] != true {
								t.Error("snapshot did not request concurrency tokens")
							}
							if _, exists := body["query"]; exists {
								t.Error("scan must not use lossy metadata prefilter")
							}
							_, _ = io.WriteString(w, metadataFixturePageOne)
						case strings.HasSuffix(r.URL.Path, "_search"):
							var body map[string]any
							if err := jsonv2.UnmarshalRead(r.Body, &body); err != nil {
								t.Error(err)
							}
							knn := body["knn"].(map[string]any)
							clause := knn["filter"].(map[string]any)
							ids := clause["ids"].(map[string]any)
							raw, _ := jsonv2.Marshal(ids["values"])
							if err := jsonv2.Unmarshal(raw, &selected); err != nil {
								t.Error(err)
							}
							hits := make([]map[string]any, 0, len(selected))
							for _, id := range selected {
								var source map[string]any
								for _, rawPage := range []string{metadataFixturePageOne, metadataFixturePageTwo} {
									var page searchResponse
									if err := jsonv2.Unmarshal([]byte(rawPage), &page); err != nil {
										t.Error(err)
									}
									for _, hit := range page.Hits.Hits {
										if hit.ID == id {
											source = make(map[string]any, len(hit.Source)+1)
											for name, value := range hit.Source {
												source[name] = value
											}
										}
									}
								}
								source["content"] = "document"
								hits = append(hits, map[string]any{"_id": id, "_score": 0.8, "_source": source})
							}
							encoded, _ := jsonv2.Marshal(map[string]any{"hits": map[string]any{"hits": hits}})
							_, _ = w.Write(encoded)
						case strings.HasSuffix(r.URL.Path, "_bulk"):
							body, _ := io.ReadAll(r.Body)
							var items []map[string]any
							for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
								var action struct {
									Delete struct {
										ID      string `json:"_id"`
										Routing string `json:"routing"`
										Seq     *int   `json:"if_seq_no"`
										Term    *int   `json:"if_primary_term"`
									} `json:"delete"`
								}
								if err := jsonv2.Unmarshal([]byte(line), &action); err != nil {
									t.Error(err)
								}
								if action.Delete.Seq == nil || *action.Delete.Seq != 3 || action.Delete.Term == nil || *action.Delete.Term != 1 {
									t.Errorf("deletion lost snapshot token: %s", line)
								}
								if action.Delete.ID == "scalar" && action.Delete.Routing != "tenant-a" {
									t.Error("conditional deletion lost custom routing")
								}
								selected = append(selected, action.Delete.ID)
								items = append(items, map[string]any{"delete": map[string]any{"_id": action.Delete.ID, "status": 200}})
							}
							encoded, _ := jsonv2.Marshal(map[string]any{"errors": false, "items": items})
							_, _ = w.Write(encoded)
						default:
							t.Errorf("unexpected request %s %s", r.Method, r.URL)
							http.Error(w, "unexpected", 500)
						}
					}))
					t.Cleanup(server.Close)
					client, err := elasticsearch.NewClient(elasticsearch.Config{Addresses: []string{server.URL}, Transport: server.Client().Transport})
					if err != nil {
						t.Fatal(err)
					}
					model, err := embeddingclient.New(embedding.ModelFunc(func(context.Context, *embedding.Request) (*embedding.Response, error) {
						return &embedding.Response{Outputs: []*embedding.Output{{Embedding: []float64{1, 0}}}}, nil
					}))
					if err != nil {
						t.Fatal(err)
					}
					store := &Store{client: client, indexName: "documents", metadataField: "metadata", contentField: "content", embeddingField: "embedding", embeddingClient: model, numCandidatesMul: 1.5, similarity: SimilarityCosine}
					if operation == "delete" {
						err = store.DeleteWhere(t.Context(), predicate)
					} else {
						_, err = store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 10, Filter: predicate}})
					}
					if err != nil {
						t.Fatal(err)
					}
					slices.Sort(selected)
					if !slices.Equal(selected, sample.want) {
						t.Fatalf("selected %v; want %v", selected, sample.want)
					}
					if !cleared || scrollPage != 2 {
						t.Fatalf("incomplete scroll lifecycle: cleared=%v pages=%d", cleared, scrollPage)
					}
				})
			}
		})
	}
}

// Both pages are raw server results. The fixture performs no predicate evaluation.
const metadataFixturePageOne = `{"_scroll_id":"snapshot","hits":{"hits":[
 {"_id":"scalar","_routing":"tenant-a","_seq_no":3,"_primary_term":1,"_source":{"metadata":{"value":"a","name":"a","guard":"present"}}},
 {"_id":"array","_seq_no":3,"_primary_term":1,"_source":{"metadata":{"value":["a"]}}},
 {"_id":"empty","_seq_no":3,"_primary_term":1,"_source":{"metadata":{"value":[]}}}
]}}`
const metadataFixturePageTwo = `{"_scroll_id":"snapshot","hits":{"hits":[
 {"_id":"null","_seq_no":3,"_primary_term":1,"_source":{"metadata":{"value":null}}},
 {"_id":"missing","_seq_no":3,"_primary_term":1,"_source":{"metadata":{}}},
 {"_id":"text","_seq_no":3,"_primary_term":1,"_source":{"metadata":{"value":"Alice Smith","name":"Alice Smith"}}},
 {"_id":"multiline","_seq_no":3,"_primary_term":1,"_source":{"metadata":{"value":"f\no","name":"f\no"}}}
]}}`

func TestConditionalDeletionRejectsIncompleteOrConflictingAcknowledgment(t *testing.T) {
	for _, body := range []string{
		`{"errors":false,"items":[]}`,
		`{"errors":false,"items":[{"delete":{"_id":"different","status":200}}]}`,
		`{"errors":false,"items":[{"delete":{"_id":"scalar","status":409}}]}`,
		`{"errors":true,"items":[{"delete":{"_id":"scalar","status":409,"error":{"reason":"version changed"}}}]}`,
	} {
		t.Run(fmt.Sprint(body), func(t *testing.T) {
			if err := parseBulkResponse(&esapi.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, bulkOperationDelete, []string{"scalar"}); err == nil {
				t.Fatal("incomplete/conflicting acknowledgment accepted")
			}
		})
	}
}

func TestSearchRejectsChangedOrUnselectedRecords(t *testing.T) {
	for _, record := range []string{
		`{"_id":"scalar","_score":0.8,"_source":{"content":"document","metadata":{"value":"other"}}}`,
		`{"_id":"unexpected","_score":0.8,"_source":{"content":"document","metadata":{"value":"a"}}}`,
	} {
		t.Run(record, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Elastic-Product", "Elasticsearch")
				switch {
				case r.Method == http.MethodDelete:
					_, _ = io.WriteString(w, `{"succeeded":true,"num_freed":1}`)
				case strings.HasSuffix(r.URL.Path, "_search/scroll"):
					_, _ = io.WriteString(w, `{"_scroll_id":"snapshot","hits":{"hits":[]}}`)
				case r.URL.Query().Get("scroll") != "":
					_, _ = io.WriteString(w, metadataFixturePageOne)
				default:
					_, _ = io.WriteString(w, `{"hits":{"hits":[`+record+`]}}`)
				}
			}))
			t.Cleanup(server.Close)
			client, err := elasticsearch.NewClient(elasticsearch.Config{Addresses: []string{server.URL}, Transport: server.Client().Transport})
			if err != nil {
				t.Fatal(err)
			}
			model, err := embeddingclient.New(embedding.ModelFunc(func(context.Context, *embedding.Request) (*embedding.Response, error) {
				return &embedding.Response{Outputs: []*embedding.Output{{Embedding: []float64{1, 0}}}}, nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			store := &Store{client: client, indexName: "documents", metadataField: "metadata", contentField: "content", embeddingField: "embedding", embeddingClient: model, numCandidatesMul: 1.5, similarity: SimilarityCosine}
			response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 1, Filter: filter.EQ("value", "a")}})
			if err == nil || response != nil {
				t.Fatalf("query accepted changed/unselected result: response=%v error=%v", response, err)
			}
		})
	}
}
