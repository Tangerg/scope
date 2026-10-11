package vectorstore_test

import (
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/vectorstore"
)

func TestSearchResultRequiresCompleteIndexedDocument(t *testing.T) {
	image, err := media.NewURI("image/png", "https://example.test/a.png")
	if err != nil {
		t.Fatal(err)
	}
	// A media-only document is individually valid but is not a complete indexed
	// document: a store rejects it at write, so a search result must reject it too.
	mediaOnly := &document.Document{ID: "doc-1", Media: image}
	if docErr := mediaOnly.Validate(); docErr != nil {
		t.Fatalf("media-only document is individually valid: %v", docErr)
	}
	if _, resErr := vectorstore.NewSearchResult(mediaOnly, 0.8); !errors.Is(resErr, vectorstore.ErrInvalidResponse) {
		t.Fatalf("NewSearchResult accepted a document without text: %v", resErr)
	}
	// The same rule rejects it at index admission, confirming one shared owner.
	if _, reqErr := vectorstore.NewIndexRequest([]*document.Document{mediaOnly}); !errors.Is(reqErr, vectorstore.ErrInvalidRequest) {
		t.Fatalf("NewIndexRequest accepted a document without text: %v", reqErr)
	}
}

func TestSearchRequestValidate(t *testing.T) {
	cases := []struct {
		name string
		req  vectorstore.SearchRequest
		ok   bool
	}{
		{"empty query", vectorstore.SearchRequest{Options: vectorstore.SearchOptions{TopK: 5}}, false},
		{"default topk", vectorstore.SearchRequest{Query: "hi"}, true},
		{"negative topk", vectorstore.SearchRequest{Query: "hi", Options: vectorstore.SearchOptions{TopK: -1}}, false},
		{"out-of-range minscore", vectorstore.SearchRequest{Query: "hi", Options: vectorstore.SearchOptions{TopK: 5, MinScore: 1.5}}, false},
		{"nan minscore", vectorstore.SearchRequest{Query: "hi", Options: vectorstore.SearchOptions{TopK: 5, MinScore: vectorstore.Score(math.NaN())}}, false},
		{"unknown mode", vectorstore.SearchRequest{Query: "hi", Options: vectorstore.SearchOptions{Mode: "magic"}}, false},
		{"hybrid threshold", vectorstore.SearchRequest{Query: "hi", Options: vectorstore.SearchOptions{Mode: vectorstore.SearchModeHybrid, MinScore: 0.5}}, false},
		{"hybrid", vectorstore.SearchRequest{Query: "hi", Options: vectorstore.SearchOptions{Mode: vectorstore.SearchModeHybrid}}, true},
		{"valid", vectorstore.SearchRequest{Query: "hi", Options: vectorstore.SearchOptions{TopK: 5, MinScore: 0.5}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.req.Validate()
			if tc.ok && err != nil {
				t.Fatalf("expected ok, got %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestSearchModeIsExplicitAndCapabilityChecked(t *testing.T) {
	var options vectorstore.SearchOptions
	if got := options.EffectiveMode(); got != vectorstore.SearchModeSemantic {
		t.Fatalf("zero mode = %q, want semantic", got)
	}
	if options.EffectiveMode().String() != "semantic" {
		t.Fatalf("semantic string = %q", options.EffectiveMode().String())
	}
	options.Mode = vectorstore.SearchModeHybrid
	if err := options.RequireMode(vectorstore.SearchModeSemantic); !errors.Is(err, vectorstore.ErrUnsupportedSearchMode) {
		t.Fatalf("RequireMode error = %v", err)
	}
	if err := options.RequireMode(vectorstore.SearchModeSemantic, vectorstore.SearchModeHybrid); err != nil {
		t.Fatal(err)
	}
}

func TestSearchOptionsZeroValueUsesDefaultLimit(t *testing.T) {
	var options vectorstore.SearchOptions
	if err := options.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := options.ResultLimit(); got != vectorstore.DefaultTopK {
		t.Fatalf("ResultLimit = %d, want %d", got, vectorstore.DefaultTopK)
	}
	options.TopK = 3
	if got := options.ResultLimit(); got != 3 {
		t.Fatalf("explicit ResultLimit = %d, want 3", got)
	}
}

func TestSearchResponseValidate(t *testing.T) {
	request := vectorstore.SearchRequest{Query: "scope", Options: vectorstore.SearchOptions{TopK: 2, MinScore: 0.5}}
	first, _ := document.NewDocument("first", nil)
	second, _ := document.NewDocument("second", nil)
	first.ID = "first"
	second.ID = "second"
	valid := []*vectorstore.SearchResult{{Document: first, Score: 0.9}, {Document: second, Score: 0.5}}
	if err := (&vectorstore.SearchResponse{Results: valid}).ValidateFor(&request); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		results []*vectorstore.SearchResult
	}{
		{name: "too many", results: append(valid, &vectorstore.SearchResult{Document: second, Score: 0.5})},
		{name: "nil result", results: []*vectorstore.SearchResult{nil}},
		{name: "nil document", results: []*vectorstore.SearchResult{{Score: 0.9}}},
		{name: "missing document ID", results: []*vectorstore.SearchResult{{Document: &document.Document{Text: "text"}, Score: 0.9}}},
		{name: "out of range", results: []*vectorstore.SearchResult{{Document: first, Score: 1.1}}},
		{name: "below threshold", results: []*vectorstore.SearchResult{{Document: first, Score: 0.4}}},
		{name: "not sorted", results: []*vectorstore.SearchResult{{Document: first, Score: 0.5}, {Document: second, Score: 0.9}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := (&vectorstore.SearchResponse{Results: test.results}).ValidateFor(&request); err == nil {
				t.Fatal("SearchResponse.Validate accepted invalid output")
			}
		})
	}
}

func TestCapabilityInterfacesStayMinimal(t *testing.T) {
	tests := []struct {
		name   string
		typeOf reflect.Type
		method string
	}{
		{"Indexer", reflect.TypeFor[vectorstore.Indexer](), "Index"},
		{"Searcher", reflect.TypeFor[vectorstore.Searcher](), "Search"},
		{"IDDeleter", reflect.TypeFor[vectorstore.IDDeleter](), "DeleteIDs"},
		{"FilterDeleter", reflect.TypeFor[vectorstore.FilterDeleter](), "DeleteWhere"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.typeOf.NumMethod() != 1 || tc.typeOf.Method(0).Name != tc.method {
				t.Fatalf("methods = %v, want only %s", tc.typeOf, tc.method)
			}
		})
	}
}
