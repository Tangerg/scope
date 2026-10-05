package azurecosmos

import (
	jsonv2 "encoding/json/v2"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos"

	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

type filterCase struct {
	source     string
	query      string
	params     []azcosmos.QueryParameter
	wantAbsent bool
}

func coreFilterCases() []filterCase {
	return []filterCase{
		{`name == 'Alice'`, `((c.metadata.name = @p1) ?? false)`, []azcosmos.QueryParameter{{Name: "@p1", Value: "Alice"}}, false},
		{`name != 'Alice'`, `NOT (((c.metadata.name = @p1) ?? false))`, []azcosmos.QueryParameter{{Name: "@p1", Value: "Alice"}}, true},
		{`not (name == 'Alice')`, `NOT (((c.metadata.name = @p1) ?? false))`, []azcosmos.QueryParameter{{Name: "@p1", Value: "Alice"}}, true},
		{`not (name != 'Alice')`, `NOT (NOT (((c.metadata.name = @p1) ?? false)))`, []azcosmos.QueryParameter{{Name: "@p1", Value: "Alice"}}, false},
		{`name < 10`, `((c.metadata.name < @p1) ?? false)`, []azcosmos.QueryParameter{{Name: "@p1", Value: int64(10)}}, false},
		{`name <= 10`, `((c.metadata.name <= @p1) ?? false)`, []azcosmos.QueryParameter{{Name: "@p1", Value: int64(10)}}, false},
		{`name > 10`, `((c.metadata.name > @p1) ?? false)`, []azcosmos.QueryParameter{{Name: "@p1", Value: int64(10)}}, false},
		{`name >= 10`, `((c.metadata.name >= @p1) ?? false)`, []azcosmos.QueryParameter{{Name: "@p1", Value: int64(10)}}, false},
		{`not (name > 10)`, `NOT (((c.metadata.name > @p1) ?? false))`, []azcosmos.QueryParameter{{Name: "@p1", Value: int64(10)}}, true},
		{`name in ('Alice', 'Bob')`, `((c.metadata.name IN (@p1, @p2)) ?? false)`, []azcosmos.QueryParameter{{Name: "@p1", Value: "Alice"}, {Name: "@p2", Value: "Bob"}}, false},
		{`name not in ('Alice', 'Bob')`, `NOT (((c.metadata.name IN (@p1, @p2)) ?? false))`, []azcosmos.QueryParameter{{Name: "@p1", Value: "Alice"}, {Name: "@p2", Value: "Bob"}}, true},
		{`name has 'Alice'`, `((ARRAY_CONTAINS(c.metadata.name, @p1)) ?? false)`, []azcosmos.QueryParameter{{Name: "@p1", Value: "Alice"}}, false},
		{`not (name has 'Alice')`, `NOT (((ARRAY_CONTAINS(c.metadata.name, @p1)) ?? false))`, []azcosmos.QueryParameter{{Name: "@p1", Value: "Alice"}}, true},
		{`name like 'A_%e'`, `((c.metadata.name LIKE @p1 ESCAPE '!') ?? false)`, []azcosmos.QueryParameter{{Name: "@p1", Value: "A_%e"}}, false},
		{`not (name like 'A_%e')`, `NOT (((c.metadata.name LIKE @p1 ESCAPE '!') ?? false))`, []azcosmos.QueryParameter{{Name: "@p1", Value: "A_%e"}}, true},
		{`name is null`, `(NOT IS_DEFINED(c.metadata.name) OR IS_NULL(c.metadata.name))`, nil, true},
		{`name is not null`, `NOT ((NOT IS_DEFINED(c.metadata.name) OR IS_NULL(c.metadata.name)))`, nil, false},
		{`not (name == 'Alice' or name > 10)`, `NOT ((((c.metadata.name = @p1) ?? false) OR ((c.metadata.name > @p2) ?? false)))`, []azcosmos.QueryParameter{{Name: "@p1", Value: "Alice"}, {Name: "@p2", Value: int64(10)}}, true},
		{`name != 'Alice' and name is null`, `(NOT (((c.metadata.name = @p1) ?? false)) AND (NOT IS_DEFINED(c.metadata.name) OR IS_NULL(c.metadata.name)))`, []azcosmos.QueryParameter{{Name: "@p1", Value: "Alice"}}, true},
	}
}

func TestCoreFilterSemanticsReachNativeQueries(t *testing.T) {
	for _, sample := range coreFilterCases() {
		t.Run(sample.source, func(t *testing.T) {
			predicate, err := filter.Parse(sample.source)
			if err != nil {
				t.Fatal(err)
			}
			for _, metadata := range []map[string]any{nil, {"name": nil}} {
				matched, matchErr := filter.Match(predicate, metadata)
				if matchErr != nil || matched != sample.wantAbsent {
					t.Fatalf("Core Match(%#v) = %t, %v, want %t", metadata, matched, matchErr, sample.wantAbsent)
				}
			}
			compiler := newVisitor("c", "metadata")
			if err := predicate.Accept(compiler); err != nil {
				t.Fatal(err)
			}
			query, params := compiler.snapshot()
			if query != sample.query || !slices.Equal(params, sample.params) {
				t.Fatalf("query = %q, %#v; want %q, %#v", query, params, sample.query, sample.params)
			}
		})
	}
}

func TestStoreUsesCanonicalFiltersForSearchAndDeletion(t *testing.T) {
	for _, sample := range coreFilterCases() {
		t.Run(sample.source, func(t *testing.T) {
			var queries, deletes atomic.Int64
			store := newTestStore(t, "", func(writer http.ResponseWriter, request *http.Request) {
				if request.Method == http.MethodDelete {
					deletes.Add(1)
					if request.URL.Path != "/dbs/test/colls/vectors/docs/selected" || queries.Load() != 2 {
						t.Errorf("delete = %s after %d queries", request.URL.Path, queries.Load())
					}
					writer.WriteHeader(http.StatusNoContent)
					return
				}
				var payload struct {
					Query      string `json:"query"`
					Parameters []struct {
						Name  string `json:"name"`
						Value any    `json:"value"`
					} `json:"parameters"`
				}
				if err := jsonv2.UnmarshalRead(request.Body, &payload); err != nil {
					t.Error(err)
					writer.WriteHeader(http.StatusBadRequest)
					return
				}
				queryNumber := queries.Add(1)
				clause := " WHERE " + sample.query
				if queryNumber == 1 {
					if !strings.Contains(payload.Query, clause+" ORDER BY VectorDistance(") {
						t.Errorf("search query = %q, want filter %q", payload.Query, sample.query)
					}
				} else if want := "SELECT c.id AS _id FROM c" + clause; payload.Query != want {
					t.Errorf("delete query = %q, want %q", payload.Query, want)
				}
				filterParams := payload.Parameters
				if queryNumber == 1 {
					if len(filterParams) < 2 {
						t.Errorf("missing search parameters: %#v", filterParams)
						writer.WriteHeader(http.StatusBadRequest)
						return
					}
					filterParams = filterParams[2:]
				}
				if len(filterParams) != len(sample.params) {
					t.Errorf("filter parameters = %#v, want %#v", filterParams, sample.params)
				} else {
					for index, param := range filterParams {
						actual, err := jsonv2.Marshal(param.Value)
						want, wantErr := jsonv2.Marshal(sample.params[index].Value)
						if err != nil || wantErr != nil || param.Name != sample.params[index].Name || string(actual) != string(want) {
							t.Errorf("parameter = %#v, want %#v", param, sample.params[index])
						}
					}
				}
				if !sample.wantAbsent {
					fmt.Fprint(writer, `{"Documents":[],"_count":0}`)
					return
				}
				fmt.Fprint(writer, `{"Documents":[{"_id":"selected","_content":"text","_metadata":{},"_vector_score":1}],"_count":1}`)
			})
			predicate, err := filter.Parse(sample.source)
			if err != nil {
				t.Fatal(err)
			}
			request, err := vectorstore.NewSearchRequest("text")
			if err != nil {
				t.Fatal(err)
			}
			request.Options.Filter = predicate
			response, err := store.Search(t.Context(), request)
			if err != nil || response == nil {
				t.Fatalf("Search = %#v, %v", response, err)
			}
			wantMatches := 0
			if sample.wantAbsent {
				wantMatches = 1
			}
			if len(response.Results) != wantMatches || (wantMatches == 1 && response.Results[0].Document.ID != "selected") {
				t.Fatalf("Search = %#v, want %d matches", response, wantMatches)
			}
			if err := store.DeleteWhere(t.Context(), predicate); err != nil {
				t.Fatal(err)
			}
			if queries.Load() != 2 || deletes.Load() != int64(wantMatches) {
				t.Fatalf("queries = %d, deletes = %d, want 2 queries and %d deletes", queries.Load(), deletes.Load(), wantMatches)
			}
		})
	}
}
