package vectorstore_test

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
)

func TestSearchResponseJSONRequiresReportedResults(t *testing.T) {
	for _, data := range []string{
		`null`, `{}`, `{"results":null}`,
		`{"results":[{"document":{"id":"doc","text":"text"}}]}`,
		`{"results":[{"document":{"id":"doc","text":"text"},"score":null}]}`,
	} {
		t.Run(data, func(t *testing.T) {
			kept := &vectorstore.SearchResult{Document: &document.Document{ID: "kept", Text: "text"}, Score: 0.8}
			response := vectorstore.SearchResponse{Results: []*vectorstore.SearchResult{kept}}
			if err := jsonv2.Unmarshal([]byte(data), &response); !errors.Is(err, vectorstore.ErrInvalidResponse) {
				t.Fatalf("decode error = %v, want ErrInvalidResponse", err)
			}
			if response.First() != kept || kept.Document.ID != "kept" || kept.Score != 0.8 {
				t.Fatal("rejected results changed the retained response")
			}
		})
	}
	for _, data := range []string{
		`{"results":[]}`,
		`{"results":[{"document":{"id":"doc","text":"text"},"score":0}]}`,
	} {
		var response vectorstore.SearchResponse
		if err := jsonv2.Unmarshal([]byte(data), &response); err != nil {
			t.Fatal(err)
		}
		encoded, err := jsonv2.Marshal(response)
		if err != nil || string(encoded) != data {
			t.Fatalf("round trip = %s, %v; want %s", encoded, err, data)
		}
	}
}

func TestScoreJSONRequiresANumber(t *testing.T) {
	score := vectorstore.Score(0.8)
	if err := jsonv2.Unmarshal([]byte(`null`), &score); !errors.Is(err, vectorstore.ErrInvalidScore) {
		t.Fatalf("decode error = %v, want ErrInvalidScore", err)
	}
	if score != 0.8 {
		t.Fatalf("rejected score changed receiver to %v", score)
	}
	options := vectorstore.SearchOptions{MinScore: 0.8}
	if err := jsonv2.Unmarshal([]byte(`{"min_score":null}`), &options); !errors.Is(err, vectorstore.ErrInvalidOptions) || !errors.Is(err, vectorstore.ErrInvalidScore) {
		t.Fatalf("null minimum score error = %v, want ErrInvalidOptions and ErrInvalidScore", err)
	}
	if options.MinScore != 0.8 {
		t.Fatal("rejected minimum score changed options")
	}
	if err := jsonv2.Unmarshal([]byte(`{}`), &options); err != nil || options.MinScore != 0 {
		t.Fatalf("omitted minimum score = %v, %v; want default zero", options.MinScore, err)
	}
}
