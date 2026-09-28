package opensearch

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

func TestFilteredSearchChecksReturnedMetadataAndBatch(t *testing.T) {
	for _, sample := range []struct {
		name      string
		predicate string
		metadata  string
		id        string
		score     float64
		wantErr   string
	}{
		{"unchanged", `tenant == 'allowed'`, `{"tenant":"allowed"}`, "document", 0.8, ""},
		{"tenant changed after scan", `tenant == 'allowed'`, `{"tenant":"private"}`, "document", 0.8, "no longer matches"},
		{"changed hit below minimum score", `tenant == 'allowed'`, `{"tenant":"private"}`, "document", 0.1, "no longer matches"},
		{"metadata type changed", `age > 0`, `{"age":"private"}`, "document", 0.8, "evaluate returned metadata"},
		{"metadata became malformed", `tenant == 'allowed'`, `"not an object"`, "document", 0.8, "decode metadata"},
		{"ID outside selected batch", `tenant == 'allowed'`, `{"tenant":"allowed"}`, "foreign", 0.8, "outside the selected ID batch"},
	} {
		t.Run(sample.name, func(t *testing.T) {
			knnCalls, scans := 0, 0
			store := newSearchContractStore(t, EngineLucene, SpaceTypeCosine, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "_search/scroll"):
					fmt.Fprint(w, `{"succeeded":true,"num_freed":1}`)
				case strings.Contains(r.URL.Path, "_search/scroll"):
					fmt.Fprint(w, `{"_scroll_id":"snapshot","hits":{"hits":[]}}`)
				case r.URL.Query().Get("scroll") != "":
					scans++
					fmt.Fprint(w, `{"_scroll_id":"snapshot","hits":{"hits":[{"_id":"document","_source":{"metadata":{"tenant":"allowed","age":1}}}]}}`)
				case r.URL.Path == "/documents/_search":
					knnCalls++
					fmt.Fprintf(w, `{"hits":{"hits":[{"_id":%q,"_score":%g,"_source":{"content":"updated document","metadata":%s}}]}}`, sample.id, sample.score, sample.metadata)
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL)
				}
			}))
			predicate, err := filter.Parse(sample.predicate)
			if err != nil {
				t.Fatal(err)
			}
			response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{Filter: predicate, MinScore: 0.5}})
			if sample.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), sample.wantErr) || response != nil {
					t.Fatalf("Search() = %v, %v; want %q", response, err, sample.wantErr)
				}
			} else if err != nil || response == nil || len(response.Results) != 1 {
				t.Fatalf("Search() = %v, %v", response, err)
			}
			if knnCalls != 1 || scans != 1 {
				t.Fatalf("scans=%d knn=%d", scans, knnCalls)
			}
		})
	}
}

func TestFilteredSearchMergesByNativeScore(t *testing.T) {
	knnCalls, scrollCalls := 0, 0
	store := newSearchContractStore(t, EngineFaiss, SpaceTypeIP, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "_search/scroll"):
			fmt.Fprint(w, `{"succeeded":true,"num_freed":1}`)
		case strings.Contains(r.URL.Path, "_search/scroll"):
			scrollCalls++
			if scrollCalls == 1 {
				fmt.Fprint(w, `{"_scroll_id":"snapshot","hits":{"hits":[{"_id":"z-best","_source":{"metadata":{"allowed":true}}}]}}`)
			} else {
				fmt.Fprint(w, `{"_scroll_id":"snapshot","hits":{"hits":[]}}`)
			}
		case r.URL.Query().Get("scroll") != "":
			hits := make([]map[string]any, filterBatchSize)
			for index := range hits {
				hits[index] = map[string]any{"_id": fmt.Sprintf("a-%04d", index), "_source": map[string]any{"metadata": map[string]any{"allowed": true}}}
			}
			writeContractJSON(t, w, map[string]any{"_scroll_id": "snapshot", "hits": map[string]any{"hits": hits}})
		case r.URL.Path == "/documents/_search":
			knnCalls++
			var body struct {
				Query struct {
					KNN map[string]nearestNeighbor `json:"knn"`
				} `json:"query"`
			}
			if err := jsonv2.UnmarshalRead(r.Body, &body); err != nil {
				t.Error(err)
			}
			neighbor := body.Query.KNN["embedding"]
			if neighbor.Filter == nil || neighbor.K != 1 {
				t.Fatalf("native KNN lost bounded ID selection: %#v", neighbor)
			}
			id, rawScore := "a-0000", 41
			if knnCalls == 2 {
				id, rawScore = "z-best", 51
				if len(neighbor.Filter.IDs.Values) != 1 || neighbor.Filter.IDs.Values[0] != id {
					t.Errorf("second batch = %v", neighbor.Filter.IDs.Values)
				}
			} else if len(neighbor.Filter.IDs.Values) != filterBatchSize {
				t.Errorf("first batch has %d IDs", len(neighbor.Filter.IDs.Values))
			}
			fmt.Fprintf(w, `{"hits":{"hits":[{"_id":%q,"_score":%d,"_source":{"content":"document","metadata":{"allowed":true}}}]}}`, id, rawScore)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
	}))
	predicate, err := filter.Parse(`allowed == true`)
	if err != nil {
		t.Fatal(err)
	}
	response, err := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{TopK: 1, Filter: predicate}})
	if err != nil {
		t.Fatal(err)
	}
	if knnCalls != 2 || len(response.Results) != 1 || response.Results[0].Document.ID != "z-best" || response.Results[0].Score != 1 {
		t.Fatalf("native rank was lost: calls=%d response=%+v", knnCalls, response)
	}
}

func TestNMSLibOnlyRejectsFilteredSearch(t *testing.T) {
	searchCalls := 0
	store := newSearchContractStore(t, EngineNMSLib, SpaceTypeL2, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		searchCalls++
		switch {
		case r.URL.Query().Get("scroll") != "":
			fmt.Fprint(w, `{"hits":{"hits":[]}}`)
		case r.URL.Path == "/documents/_search":
			var body map[string]json.RawMessage
			if err := jsonv2.UnmarshalRead(r.Body, &body); err != nil {
				t.Error(err)
			}
			if strings.Contains(string(body["query"]), `"filter"`) {
				t.Error("NMSLib received unsupported knn.filter")
			}
			fmt.Fprint(w, `{"hits":{"hits":[{"_id":"document","_score":0.8,"_source":{"content":"document"}}]}}`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
	}))
	predicate, err := filter.Parse(`tenant == 'allowed'`)
	if err != nil {
		t.Fatal(err)
	}
	request := &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{Filter: predicate}}
	if _, callErr := store.Search(t.Context(), request); !errors.Is(callErr, errors.ErrUnsupported) || searchCalls != 0 {
		t.Fatalf("filtered Search() = %v, requests=%d", callErr, searchCalls)
	}
	request.Options.Filter = nil
	if _, callErr := store.Search(t.Context(), request); callErr != nil {
		t.Fatal(callErr)
	}
	if deleteErr := store.DeleteWhere(t.Context(), predicate); deleteErr != nil {
		t.Fatal(deleteErr)
	}
	if searchCalls != 2 {
		t.Fatalf("unfiltered search and DeleteWhere made %d calls", searchCalls)
	}
}
