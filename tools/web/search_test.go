package web

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// fakeSearcher is a test double for the [Searcher] SPI. It records
// the last request it received and returns a canned response. This
// is the only mocking in the package — searcher impls themselves are
// tested against the real upstream via env-keyed integration tests.
type fakeSearcher struct {
	last *SearchRequest
	resp *SearchResponse
	err  error
}

func (f *fakeSearcher) Search(_ context.Context, req *SearchRequest) (*SearchResponse, error) {
	f.last = req
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

func TestSearchNewTool_NilSearcher(t *testing.T) {
	_, err := NewSearchTool(nil)
	if !errors.Is(err, ErrMissingSearcher) {
		t.Errorf("NewSearchTool(nil): err = %v, want ErrMissingSearcher", err)
	}
}

func TestSearchNewTool_TypedNilSearcher(t *testing.T) {
	var searcher *fakeSearcher
	if _, err := NewSearchTool(searcher); !errors.Is(err, ErrMissingSearcher) {
		t.Fatalf("NewSearchTool(typed nil) error = %v, want ErrMissingSearcher", err)
	}
}

func TestSearchTool_Definition(t *testing.T) {
	tool, err := NewSearchTool(&fakeSearcher{})
	if err != nil {
		t.Fatal(err)
	}
	def := tool.Definition()
	if def.Name != "web_search" {
		t.Errorf("Name = %q, want %q", def.Name, "web_search")
	}
	if len(def.InputSchema) == 0 {
		t.Error("InputSchema is empty")
	}
}

func TestSearchTool_Call_HappyPath(t *testing.T) {
	searcher := &fakeSearcher{

		resp: &SearchResponse{
			Query: "kittens",
			Results: []*SearchResult{
				{Title: "Cats", URL: "https://example.com/cats", Snippet: "purr"},
			},
		},
	}
	tool, _ := NewSearchTool(searcher)

	output, err := invokeTestTool(t.Context(), tool, `{"query":"kittens","max_results":3,"recency":"week"}`)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	var resp SearchResponse
	if err := json.Unmarshal(output.Details, &resp); err != nil {
		t.Fatalf("Unmarshal: %v\nbody=%s", err, output.Details)
	}
	if resp.Query != "kittens" {
		t.Errorf("SearchResponse.Query = %q", resp.Query)
	}
	if len(resp.Results) != 1 || resp.Results[0].Title != "Cats" {
		t.Errorf("SearchResponse.Results = %+v", resp.Results)
	}

	if searcher.last == nil {
		t.Fatal("searcher.Search not called")
	}
	if searcher.last.Query != "kittens" {
		t.Errorf("searcher got query %q", searcher.last.Query)
	}
	if searcher.last.MaxResults != 3 {
		t.Errorf("MaxResults forwarded = %d, want 3", searcher.last.MaxResults)
	}
	if searcher.last.Recency != RecencyWeek {
		t.Errorf("Recency forwarded = %q, want week", searcher.last.Recency)
	}
}

func TestSearchTool_Call_EmptyQuery(t *testing.T) {
	tool, _ := NewSearchTool(&fakeSearcher{})
	_, err := invokeTestTool(t.Context(), tool, `{"query":""}`)
	if err == nil {
		t.Fatal("Call empty query: want schema error")
	}
	_, err = invokeTestTool(t.Context(), tool, `{"query":"   "}`)
	if !errors.Is(err, ErrEmptyQuery) {
		t.Errorf("Call blank query: err = %v, want ErrEmptyQuery", err)
	}
}

func TestSearchTool_Call_BadJSON(t *testing.T) {
	tool, _ := NewSearchTool(&fakeSearcher{})
	if _, err := invokeTestTool(t.Context(), tool, `{bad json`); err == nil {
		t.Fatal("want error on bad JSON")
	}
}

func TestSearchTool_Call_EnforcesAdvertisedContract(t *testing.T) {
	searcher := &fakeSearcher{}
	tool, _ := NewSearchTool(searcher)
	for _, arguments := range []string{
		`{"query":"runtime","limit":3}`,
		`{"query":"runtime","max_results":21}`,
		`{"query":"runtime","recency":"recent"}`,
	} {
		searcher.last = nil
		if _, err := invokeTestTool(t.Context(), tool, arguments); err == nil {
			t.Errorf("Call(%s): want contract error", arguments)
		}
		if searcher.last != nil {
			t.Errorf("Call(%s): invalid arguments reached searcher", arguments)
		}
	}
}

func TestSearchTool_Call_DomainsMutuallyExclusive(t *testing.T) {
	tool, _ := NewSearchTool(&fakeSearcher{})
	_, err := invokeTestTool(t.Context(), tool,
		`{"query":"x","allowed_domains":["a.com"],"blocked_domains":["b.com"]}`)
	if !errors.Is(err, ErrDomainsBothSides) {
		t.Errorf("err = %v, want ErrDomainsBothSides", err)
	}
}

func TestSearchTool_Call_SearcherError(t *testing.T) {
	searcher := &fakeSearcher{err: errors.New("upstream boom")}
	tool, _ := NewSearchTool(searcher)
	_, err := invokeTestTool(t.Context(), tool, `{"query":"hello"}`)
	if err == nil {
		t.Fatal("want error when searcher fails")
	}
	if !strings.Contains(err.Error(), "upstream boom") {
		t.Errorf("err = %v, want wrapped 'upstream boom'", err)
	}
}

func TestSearchToolRejectsInvalidProviderResponse(t *testing.T) {
	for _, response := range []*SearchResponse{
		nil,
		{Query: "query", Results: []*SearchResult{nil}},
		{Query: "query", Results: []*SearchResult{{URL: "not-a-url"}}},
	} {
		tool, err := NewSearchTool(&fakeSearcher{resp: response})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := invokeTestTool(t.Context(), tool, `{"query":"query"}`); err == nil {
			t.Fatalf("provider response %#v was accepted", response)
		}
	}
}

func TestSearchRequest_QueryWithSiteOperators(t *testing.T) {
	cases := []struct {
		name    string
		query   string
		allowed []string
		blocked []string
		want    string
	}{
		{"plain", "kittens", nil, nil, "kittens"},
		{"allow one", "kittens", []string{"reddit.com"}, nil, "kittens site:reddit.com"},
		{"allow many", "kittens", []string{"a.com", "b.com"}, nil, "kittens (site:a.com OR site:b.com)"},
		{"allow many with empty entries", "x", []string{"", "a.com", "", "b.com", ""}, nil, "x (site:a.com OR site:b.com)"},
		{"block many", "x", nil, []string{"a.com", "b.com"}, "x -site:a.com -site:b.com"},
		{"block one", "kittens", nil, []string{"pinterest.com"}, "kittens -site:pinterest.com"},
		{"both (caller filters)", "x", []string{"a"}, []string{"b"}, "x site:a -site:b"},
		{"skip empty strings", "x", []string{"", "a", ""}, []string{""}, "x site:a"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := &SearchRequest{Query: tc.query, AllowedDomains: tc.allowed, BlockedDomains: tc.blocked}
			got := request.QueryWithSiteOperators()
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSearchRequestPrepareReturnsOwnedNormalizedCopy(t *testing.T) {
	original := &SearchRequest{
		Query:          "  scope  ",
		AllowedDomains: []string{"example.com"},
	}
	if err := original.Validate(); err != nil {
		t.Fatal(err)
	}
	prepared, err := original.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	prepared.AllowedDomains[0] = "changed.example"

	if prepared.Query != "scope" {
		t.Fatalf("prepared query = %q, want scope", prepared.Query)
	}
	if original.Query != "  scope  " || original.AllowedDomains[0] != "example.com" {
		t.Fatalf("Prepare mutated its input: %#v", original)
	}
}

func TestSearchRequestPrepareNormalizesDomains(t *testing.T) {
	request := &SearchRequest{
		Query:          "scope",
		AllowedDomains: []string{" BÜCHER.example. ", "xn--bcher-kva.example"},
	}
	prepared, err := request.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.AllowedDomains) != 1 || prepared.AllowedDomains[0] != "xn--bcher-kva.example" {
		t.Fatalf("allowed domains = %v", prepared.AllowedDomains)
	}
	for _, domain := range []string{"", "https://example.com", "example.com:443", "*.example.com", "bad..example"} {
		if err := (&SearchRequest{Query: "scope", AllowedDomains: []string{domain}}).Validate(); !errors.Is(err, ErrInvalidDomain) {
			t.Fatalf("domain %q error = %v, want ErrInvalidDomain", domain, err)
		}
	}
}

func TestSearchRecency_Validate(t *testing.T) {
	if err := RecencyWeek.Validate(); err != nil {
		t.Fatalf("RecencyWeek.Validate() error = %v", err)
	}
	if err := Recency("recent").Validate(); !errors.Is(err, ErrInvalidRecency) {
		t.Fatalf("Recency(recent).Validate() error = %v, want ErrInvalidRecency", err)
	}
}

func TestSearchTool_Call_DomainsForwarded(t *testing.T) {
	searcher := &fakeSearcher{resp: &SearchResponse{Query: "q", Results: nil}}
	tool, _ := NewSearchTool(searcher)
	if _, err := invokeTestTool(t.Context(), tool,
		`{"query":"x","allowed_domains":["github.com","stackoverflow.com"]}`); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if got := searcher.last.AllowedDomains; len(got) != 2 || got[0] != "github.com" {
		t.Errorf("AllowedDomains forwarded = %v", got)
	}
}
