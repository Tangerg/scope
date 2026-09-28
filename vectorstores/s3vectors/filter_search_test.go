package s3vectors

import (
	"context"
	jsonv2 "encoding/json/v2"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3vectors"
	"github.com/aws/aws-sdk-go-v2/service/s3vectors/types"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
)

type filterHTTPFixture struct {
	mu      sync.Mutex
	records []map[string]any
	deleted []string
}

func (f *filterHTTPFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	var request struct {
		NextToken      string   `json:"nextToken"`
		ReturnMetadata bool     `json:"returnMetadata"`
		ReturnData     bool     `json:"returnData"`
		Keys           []string `json:"keys"`
	}
	if err := jsonv2.UnmarshalRead(r.Body, &request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var response any
	switch r.URL.Path {
	case "/ListVectors":
		if !request.ReturnMetadata {
			http.Error(w, "metadata must be requested", http.StatusBadRequest)
			return
		}
		offset := 0
		if request.NextToken != "" {
			var err error
			offset, err = strconv.Atoi(request.NextToken)
			if err != nil || offset >= len(f.records) {
				http.Error(w, "invalid continuation", http.StatusBadRequest)
				return
			}
		}
		end := min(offset+2, len(f.records))
		rows := make([]map[string]any, 0, end-offset)
		for _, record := range f.records[offset:end] {
			row := map[string]any{"key": record["key"], "metadata": record["metadata"]}
			if request.ReturnData {
				row["data"] = map[string]any{"float32": []float32{1, 0}}
			}
			rows = append(rows, row)
		}
		page := map[string]any{"vectors": rows}
		if end < len(f.records) {
			page["nextToken"] = strconv.Itoa(end)
		}
		response = page
	case "/DeleteVectors":
		f.deleted = append(f.deleted, request.Keys...)
		response = map[string]any{}
	default:
		http.Error(w, "filtered operations must enumerate, not call "+r.URL.Path, http.StatusBadRequest)
		return
	}
	encoded, err := jsonv2.Marshal(response)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_, _ = w.Write(encoded)
}

func newFilterHTTPStore(t *testing.T, docs []*document.Document) (*Store, *filterHTTPFixture) {
	t.Helper()
	fixture := &filterHTTPFixture{}
	for _, doc := range docs {
		values, err := doc.Metadata.Values()
		if err != nil {
			t.Fatal(err)
		}
		if values == nil {
			values = map[string]any{}
		}
		values[contentMetaKey] = doc.Text
		fixture.records = append(fixture.records, map[string]any{"key": doc.ID, "metadata": values})
	}
	server := httptest.NewServer(fixture)
	t.Cleanup(server.Close)
	client := awss3.NewFromConfig(aws.Config{
		Region: "us-east-1",
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "test", SecretAccessKey: "test"}, nil
		}),
	}, func(options *awss3.Options) { options.BaseEndpoint = new(server.URL) })
	return queryStore(t, client), fixture
}

func TestFilteredSearchAndDeleteConformance(t *testing.T) {
	for _, deleting := range []bool{false, true} {
		t.Run(fmt.Sprintf("delete=%v", deleting), func(t *testing.T) {
			storetest.FilterConformance(t, storetest.FilterConfig{Query: func(ctx context.Context, docs []*document.Document, predicate filter.Predicate) ([]string, error) {
				store, fixture := newFilterHTTPStore(t, docs)
				if deleting {
					if err := store.DeleteWhere(ctx, predicate); err != nil {
						return nil, err
					}
					fixture.mu.Lock()
					defer fixture.mu.Unlock()
					return slices.Clone(fixture.deleted), nil
				}
				response, err := store.Search(ctx, &vectorstore.SearchRequest{
					Query: "query", Options: vectorstore.SearchOptions{TopK: len(docs), Filter: predicate},
				})
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

func TestFilteredSearchRanksAfterEveryPageAndBreaksTiesByID(t *testing.T) {
	for _, metric := range []DistanceMetric{DistanceCosine, DistanceEuclidean} {
		t.Run(metric.String(), func(t *testing.T) {
			row := func(id string, value any, vector []float32) types.ListOutputVector {
				result := listed(id, map[string]any{"value": value})
				result.Data = &types.VectorDataMemberFloat32{Value: vector}
				return result
			}
			client := &listedVectors{
				pages: [][]types.ListOutputVector{
					{row("excluded-nearest", []string{"yes"}, []float32{1, 0}), row("first-far", "yes", []float32{-1, 0})},
					{},
					{row("z-last-nearest", "yes", []float32{1, 0}), row("a-last-nearest", "yes", []float32{1, 0})},
				},
				tokens: []string{"middle", "last"},
			}
			store := queryStore(t, client)
			store.distanceMetric = metric
			predicate, _ := filter.Parse(`value == 'yes'`)
			response, err := store.Search(t.Context(), &vectorstore.SearchRequest{
				Query: "query", Options: vectorstore.SearchOptions{Filter: predicate, TopK: 1, MinScore: .9},
			})
			if err != nil {
				t.Fatal(err)
			}
			if client.calls != 3 || len(response.Results) != 1 || response.Results[0].Document.ID != "a-last-nearest" || response.Results[0].Score != 1 {
				t.Fatalf("calls=%d response=%+v; want full enumeration and last-page nearest with stable ID tie", client.calls, response)
			}
		})
	}
}

func TestFilteredSearchRejectsInvalidVectorData(t *testing.T) {
	for name, vector := range map[string][]float32{"missing": nil, "dimension": {1}, "infinite": {float32(math.Inf(1)), 0}} {
		t.Run(name, func(t *testing.T) {
			row := listed("bad", map[string]any{"value": "yes"})
			row.Data = &types.VectorDataMemberFloat32{Value: vector}
			client := &listedVectors{pages: [][]types.ListOutputVector{{row}}}
			predicate, _ := filter.Parse(`value == 'yes'`)
			response, err := queryStore(t, client).Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{Filter: predicate}})
			if err == nil || response != nil {
				t.Fatalf("response=%v error=%v; want explicit invalid-vector error", response, err)
			}
		})
	}
}

func TestFilteredOperationsPropagatePredicateErrors(t *testing.T) {
	for _, deleting := range []bool{false, true} {
		t.Run(fmt.Sprintf("delete=%v", deleting), func(t *testing.T) {
			client := &listedVectors{pages: [][]types.ListOutputVector{{listed("bad", map[string]any{"value": 42})}}}
			store := queryStore(t, client)
			predicate, _ := filter.Parse(`value like '%'`)
			var err error
			if deleting {
				err = store.DeleteWhere(t.Context(), predicate)
			} else {
				_, err = store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query", Options: vectorstore.SearchOptions{Filter: predicate}})
			}
			if err == nil || !strings.Contains(err.Error(), "evaluate filter") || len(client.deleted) != 0 {
				t.Fatalf("error=%v deleted=%v; want predicate error without deletion", err, client.deleted)
			}
		})
	}
}
