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
	mediaOnly := &document.Document{ID: "doc-1", Media: image}
	if err := vectorstore.ValidateDocuments([]*document.Document{mediaOnly}); err != nil {
		t.Fatal(err)
	}
	if _, err := vectorstore.NewSearchResult(mediaOnly, 0.8); err != nil {
		t.Fatalf("media-only result with prepared coordinates: %v", err)
	}
}

func TestQueryValidate(t *testing.T) {
	cases := []struct {
		name string
		req  vectorstore.Query
		ok   bool
	}{
		{"empty query", vectorstore.Query{Space: "fixture", Options: vectorstore.SearchOptions{TopK: 5}}, false},
		{"default topk", vectorstore.Query{Space: "fixture", Values: []float64{1}}, true},
		{"negative topk", vectorstore.Query{Space: "fixture", Values: []float64{1}, Options: vectorstore.SearchOptions{TopK: -1}}, false},
		{"out-of-range minscore", vectorstore.Query{Space: "fixture", Values: []float64{1}, Options: vectorstore.SearchOptions{TopK: 5, MinScore: 1.5}}, false},
		{"nan minscore", vectorstore.Query{Space: "fixture", Values: []float64{1}, Options: vectorstore.SearchOptions{TopK: 5, MinScore: vectorstore.Score(math.NaN())}}, false},
		{"unknown mode", vectorstore.Query{Space: "fixture", Values: []float64{1}, Options: vectorstore.SearchOptions{Mode: "magic"}}, false},
		{"hybrid threshold", vectorstore.Query{Space: "fixture", Values: []float64{1}, LexicalText: "hi", Options: vectorstore.SearchOptions{Mode: vectorstore.SearchModeHybrid, MinScore: 0.5}}, false},
		{"hybrid", vectorstore.Query{Space: "fixture", Values: []float64{1}, LexicalText: "hi", Options: vectorstore.SearchOptions{Mode: vectorstore.SearchModeHybrid}}, true},
		{"valid", vectorstore.Query{Space: "fixture", Values: []float64{1}, Options: vectorstore.SearchOptions{TopK: 5, MinScore: 0.5}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.req.ValidateFor("fixture")
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
	if got := options.Mode; got != vectorstore.SearchModeSemantic {
		t.Fatalf("zero mode = %q, want semantic", got)
	}
	if options.Mode.String() != "semantic" {
		t.Fatalf("semantic string = %q", options.Mode.String())
	}
	options.Mode = vectorstore.SearchModeHybrid
	if err := options.RequireMode(vectorstore.SearchModeSemantic); !errors.Is(err, vectorstore.ErrUnsupportedSearchMode) {
		t.Fatalf("RequireMode error = %v", err)
	}
	if err := options.RequireMode(vectorstore.SearchModeSemantic, vectorstore.SearchModeHybrid); err != nil {
		t.Fatal(err)
	}
}

func TestRequireResultLimitBoundsTheEffectiveLimit(t *testing.T) {
	if err := (vectorstore.SearchOptions{}).RequireResultLimit(vectorstore.DefaultTopK); err != nil {
		t.Fatalf("default limit at the maximum: %v", err)
	}
	err := (vectorstore.SearchOptions{}).RequireResultLimit(vectorstore.DefaultTopK - 1)
	if !errors.Is(err, vectorstore.ErrInvalidOptions) || err.Error() != "vectorstore: invalid options: top K 5 exceeds the native maximum 4" {
		t.Fatalf("default limit above the maximum: %v", err)
	}
	if err := (vectorstore.SearchOptions{TopK: 11}).RequireResultLimit(10); !errors.Is(err, vectorstore.ErrInvalidOptions) {
		t.Fatalf("explicit limit above the maximum: %v", err)
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
	request := vectorstore.Query{Space: "fixture", Values: []float64{1}, Options: vectorstore.SearchOptions{TopK: 2, MinScore: 0.5}}
	first, _ := document.NewDocument("first", nil)
	second, _ := document.NewDocument("second", nil)
	first.ID = "first"
	second.ID = "second"
	valid := []*vectorstore.SearchResult{{Document: first, Score: 0.9}, {Document: second, Score: 0.5}}
	if err := request.ValidateResponse(&vectorstore.SearchResponse{Results: valid}); err != nil {
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
			if err := request.ValidateResponse(&vectorstore.SearchResponse{Results: test.results}); err == nil {
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
		{"Upserter", reflect.TypeFor[vectorstore.Upserter](), "Upsert"},
		{"Querier", reflect.TypeFor[vectorstore.Querier](), "Query"},
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
