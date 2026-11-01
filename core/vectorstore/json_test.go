package vectorstore_test

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"slices"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

func TestModelErrorsAreClassifiable(t *testing.T) {
	if err := vectorstore.ValidateDocuments(nil); !errors.Is(err, vectorstore.ErrEmptyDocuments) {
		t.Fatal(err)
	}
	if _, err := vectorstore.NewSearchResult(nil, 1); !errors.Is(err, vectorstore.ErrInvalidResponse) {
		t.Fatalf("NewSearchResult error = %v, want ErrInvalidResponse", err)
	}
	if _, err := vectorstore.NewSearchResponse([]*vectorstore.SearchResult{nil}); !errors.Is(err, vectorstore.ErrInvalidResponse) {
		t.Fatalf("NewSearchResponse error = %v, want ErrInvalidResponse", err)
	}
}

func TestJSONRejectsInvalidModels(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  error
	}{
		{name: "index request", value: vectorstore.Batch{}, want: vectorstore.ErrInvalidRequest},
		{name: "search options", value: vectorstore.SearchOptions{TopK: -1}, want: vectorstore.ErrInvalidOptions},
		{name: "search request", value: vectorstore.Query{}, want: vectorstore.ErrInvalidRequest},
		{name: "search result", value: vectorstore.SearchResult{}, want: vectorstore.ErrInvalidResponse},
		{name: "search response", value: vectorstore.SearchResponse{Results: []*vectorstore.SearchResult{nil}}, want: vectorstore.ErrInvalidResponse},
		{name: "score", value: vectorstore.Score(2), want: vectorstore.ErrInvalidScore},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := jsonv2.Marshal(test.value); !errors.Is(err, test.want) {
				t.Fatalf("json.Marshal error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestJSONRoundTripsVectorstoreModels(t *testing.T) {
	doc := &document.Document{ID: "doc-1", Text: "scope"}
	b := &vectorstore.Batch{Space: "space-a", Records: []vectorstore.Record{{Document: doc, Values: []float64{1, 0}}}}
	var decodedBatch vectorstore.Batch
	roundTripJSON(t, b, &decodedBatch)
	if decodedBatch.Space != b.Space || len(decodedBatch.Records) != 1 || decodedBatch.Records[0].Document.ID != doc.ID || !slices.Equal(decodedBatch.Records[0].Values, []float64{1, 0}) {
		t.Fatalf("decoded batch = %#v", decodedBatch)
	}
	q := &vectorstore.Query{Space: "space-a", Values: []float64{0, 1}, Options: vectorstore.SearchOptions{TopK: 3, MinScore: .5, Filter: filter.EQ("kind", "animal"), Mode: vectorstore.SearchModeSemantic}}
	var decoded vectorstore.Query
	roundTripJSON(t, q, &decoded)
	if decoded.Space != q.Space || !slices.Equal(decoded.Values, q.Values) || decoded.Options.TopK != q.Options.TopK || decoded.Options.MinScore != q.Options.MinScore || !q.Options.Filter.Equal(decoded.Options.Filter) {
		t.Fatalf("decoded query=%#v", decoded)
	}
	response := &vectorstore.SearchResponse{Results: []*vectorstore.SearchResult{{Document: doc, Score: 0.9}}}
	var decodedResponse vectorstore.SearchResponse
	roundTripJSON(t, response, &decodedResponse)
	if len(decodedResponse.Results) != 1 || decodedResponse.Results[0].Document.ID != doc.ID || decodedResponse.Results[0].Score != 0.9 {
		t.Fatalf("decoded search response = %#v", decodedResponse)
	}
}

func TestSearchOptionsJSONPreservesFilterPolicy(t *testing.T) {
	expression, err := filter.Parse(`tenant == 'alpha' AND (year >= 2024 OR tags HAS 'shared')`)
	if err != nil {
		t.Fatal(err)
	}
	options := vectorstore.SearchOptions{TopK: 3, MinScore: 0.5, Filter: expression}
	data, err := jsonv2.Marshal(options)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Filter string `json:"filter"`
	}
	if err := jsonv2.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Filter != expression.String() {
		t.Fatalf("wire filter = %q, want %q", wire.Filter, expression.String())
	}
	var decoded vectorstore.SearchOptions
	if err := jsonv2.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if !expression.Equal(decoded.Filter) || decoded.TopK != 3 || decoded.MinScore != 0.5 {
		t.Fatalf("decoded options = %#v", decoded)
	}

	for _, data := range []string{
		`{"filter":""}`, `{"filter":"tenant =="}`, `{"filter":{}}`, `{"filter":42}`,
	} {
		t.Run(data, func(t *testing.T) {
			decoded := options
			if err := jsonv2.Unmarshal([]byte(data), &decoded); !errors.Is(err, vectorstore.ErrInvalidOptions) {
				t.Fatalf("decode error = %v, want ErrInvalidOptions", err)
			}
			if decoded.Filter != options.Filter || decoded.TopK != options.TopK || decoded.MinScore != options.MinScore {
				t.Fatalf("failed decode changed options: %#v", decoded)
			}
		})
	}
	for _, data := range []string{`{}`, `{"filter":null}`} {
		decoded := options
		if err := jsonv2.Unmarshal([]byte(data), &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded.Filter != nil {
			t.Fatalf("absent filter retained previous policy: %v", decoded.Filter)
		}
	}
}

func TestJSONUnmarshalRejectsInvalidModels(t *testing.T) {
	tests := []struct {
		name   string
		data   string
		target any
		want   error
	}{
		{name: "index request", data: `{"documents":[]}`, target: &vectorstore.Batch{}, want: vectorstore.ErrInvalidRequest},
		{name: "search options", data: `{"top_k":-1}`, target: &vectorstore.SearchOptions{}, want: vectorstore.ErrInvalidOptions},
		{name: "search request", data: `{"query":"","options":{"top_k":1}}`, target: &vectorstore.Query{}, want: vectorstore.ErrInvalidRequest},
		{name: "search result", data: `{"document":null,"score":1}`, target: &vectorstore.SearchResult{}, want: vectorstore.ErrInvalidResponse},
		{name: "search response", data: `{"results":[null]}`, target: &vectorstore.SearchResponse{}, want: vectorstore.ErrInvalidResponse},
		{name: "score", data: `2`, target: new(vectorstore.Score), want: vectorstore.ErrInvalidScore},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := jsonv2.Unmarshal([]byte(test.data), test.target); !errors.Is(err, test.want) {
				t.Fatalf("json.Unmarshal error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestNilJSONReceiversAreRejected(t *testing.T) {
	var indexRequest *vectorstore.Batch
	if err := indexRequest.UnmarshalJSON([]byte(`{}`)); !errors.Is(err, vectorstore.ErrInvalidRequest) {
		t.Fatalf("Batch.UnmarshalJSON error = %v", err)
	}
	var options *vectorstore.SearchOptions
	if err := options.UnmarshalJSON([]byte(`{}`)); !errors.Is(err, vectorstore.ErrInvalidOptions) {
		t.Fatalf("SearchOptions.UnmarshalJSON error = %v", err)
	}
	var request *vectorstore.Query
	if err := request.UnmarshalJSON([]byte(`{}`)); !errors.Is(err, vectorstore.ErrInvalidRequest) {
		t.Fatalf("Query.UnmarshalJSON error = %v", err)
	}
	var result *vectorstore.SearchResult
	if err := result.UnmarshalJSON([]byte(`{}`)); !errors.Is(err, vectorstore.ErrInvalidResponse) {
		t.Fatalf("SearchResult.UnmarshalJSON error = %v", err)
	}
	var response *vectorstore.SearchResponse
	if err := response.UnmarshalJSON([]byte(`{}`)); !errors.Is(err, vectorstore.ErrInvalidResponse) {
		t.Fatalf("SearchResponse.UnmarshalJSON error = %v", err)
	}
	var score *vectorstore.Score
	if err := score.UnmarshalJSON([]byte(`0`)); !errors.Is(err, vectorstore.ErrInvalidScore) {
		t.Fatalf("Score.UnmarshalJSON error = %v", err)
	}
}

func roundTripJSON(t *testing.T, source, target any) {
	t.Helper()
	data, err := jsonv2.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := jsonv2.Unmarshal(data, target); err != nil {
		t.Fatal(err)
	}
}
