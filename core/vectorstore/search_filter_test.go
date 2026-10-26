package vectorstore_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

func TestSearchResponseValidateForEnforcesMetadataFilter(t *testing.T) {
	for _, test := range []struct {
		name      string
		predicate filter.Predicate
		values    map[string]any
		wantError bool
	}{
		{name: "unfiltered", values: map[string]any{"tenant": "other"}},
		{name: "matching tenant", predicate: filter.EQ("tenant", "scope"), values: map[string]any{"tenant": "scope"}},
		{name: "different tenant", predicate: filter.EQ("tenant", "scope"), values: map[string]any{"tenant": "other"}, wantError: true},
		{name: "missing tenant", predicate: filter.EQ("tenant", "scope"), wantError: true},
		{name: "explicit absent selection", predicate: filter.IsNull("tenant")},
		{name: "matching large integer", predicate: filter.EQ("sequence", int64(9007199254740993)), values: map[string]any{"sequence": int64(9007199254740993)}},
		{name: "distinct large integer", predicate: filter.EQ("sequence", int64(9007199254740993)), values: map[string]any{"sequence": int64(9007199254740992)}, wantError: true},
		{name: "matching array index", predicate: filter.EQ(filter.Index("teams", 0), "scope"), values: map[string]any{"teams": []string{"scope"}}},
		{name: "object key is not array index", predicate: filter.EQ(filter.Index("teams", 0), "scope"), values: map[string]any{"teams": map[string]any{"0": "scope"}}, wantError: true},
		{name: "ordering type mismatch", predicate: filter.GT("sequence", 0), values: map[string]any{"sequence": "invalid"}, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			facts, err := metadata.FromValues(test.values)
			if err != nil {
				t.Fatal(err)
			}
			response := &vectorstore.SearchResponse{Results: []*vectorstore.SearchResult{
				{Document: &document.Document{ID: "doc", Text: "content", Metadata: facts}, Score: 1},
			}}
			request := &vectorstore.SearchRequest{Query: "content", Options: vectorstore.SearchOptions{Filter: test.predicate}}
			err = response.ValidateFor(request)
			if !test.wantError {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if !errors.Is(err, vectorstore.ErrInvalidResponse) || !strings.Contains(err.Error(), "results[0]") {
				t.Fatalf("ValidateFor() = %v, want ErrInvalidResponse identifying results[0]", err)
			}
		})
	}
}
