package web

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"
	"time"
)

func TestWebRejectsUnencodableOwnedText(t *testing.T) {
	if _, err := (&SearchRequest{Query: "\xff"}).Prepare(); err == nil {
		t.Fatal("accepted invalid query UTF-8")
	}
	if _, err := (&FetchRequest{URL: "https://example.test/\xff"}).Prepare(); !errors.Is(err, ErrInvalidURL) {
		t.Fatalf("Prepare() = %v", err)
	}
	if validationErr := (&FetchResponse{Content: "\xff", Format: FormatText}).Validate(); !errors.Is(validationErr, ErrInvalidFetchResponse) {
		t.Fatalf("fetch Validate() = %v", validationErr)
	}
	for _, mutate := range []func(*SearchResponse){
		func(s *SearchResponse) { s.Query = "\xff" },
		func(s *SearchResponse) { s.Results[0].Title = "\xff" },
		func(s *SearchResponse) { s.Results[0].URL = "https://example.test/\xff" },
		func(s *SearchResponse) { s.Results[0].Snippet = "\xff" },
		func(s *SearchResponse) { s.Results[0].FaviconURL = "\xff" },
		func(s *SearchResponse) { s.Results[0].Source = "\xff" },
	} {
		response := &SearchResponse{Query: "query", Results: []*SearchResult{{URL: "https://example.test"}}}
		mutate(response)
		if validationErr := response.Validate(); !errors.Is(validationErr, ErrInvalidSearchResponse) {
			t.Fatalf("Validate(%+v) = %v", response, validationErr)
		}
	}
}

func TestSearchRejectsLossyPublishedTimeBeforeToolCompletion(t *testing.T) {
	for _, value := range []time.Time{
		time.Date(2026, 1, 1, 0, 0, 0, 0, time.FixedZone("seconds", 43)),
		time.Date(2026, 1, 1, 0, 0, 0, 0, time.FixedZone("seconds", -43)),
		time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC),
	} {
		searcher := &fakeSearcher{resp: &SearchResponse{Query: "query", Results: []*SearchResult{{URL: "https://example.test", PublishedTime: value}}}}
		tool, err := NewSearchTool(searcher)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := invokeTestTool(t.Context(), tool, `{"query":"query"}`); !errors.Is(err, ErrInvalidSearchResponse) {
			t.Fatalf("Call() = %v for %v", err, value)
		}
	}
}

func TestSearchPreservesTextAndPublishedInstant(t *testing.T) {
	value := "来源\x00é"
	published := time.Date(2026, 1, 1, 0, 0, 0, 123456789, time.FixedZone("minutes", 8*60*60))
	response := SearchResponse{Query: value, Results: []*SearchResult{{URL: "https://example.test", Title: value, Snippet: value, Source: value, FaviconURL: value, PublishedTime: published}}}
	if validationErr := response.Validate(); validationErr != nil {
		t.Fatal(validationErr)
	}
	encoded, err := jsonv2.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var decoded SearchResponse
	if decodeErr := jsonv2.Unmarshal(encoded, &decoded); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	result := decoded.Results[0]
	if decoded.Query != value || result.Title != value || result.Snippet != value || result.Source != value || result.FaviconURL != value || !result.PublishedTime.Equal(published) {
		t.Fatal("search facts changed during encoding")
	}
	for _, content := range []string{"", value} {
		if validationErr := (&FetchResponse{Content: content, Format: FormatText}).Validate(); validationErr != nil {
			t.Fatal(validationErr)
		}
	}
}
