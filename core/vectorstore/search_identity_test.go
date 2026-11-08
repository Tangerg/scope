package vectorstore_test

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
)

func TestSearchResponseRejectsRepeatedDocumentIdentity(t *testing.T) {
	response := &vectorstore.SearchResponse{Results: []*vectorstore.SearchResult{
		{Document: &document.Document{ID: "same", Text: "first"}, Score: 0.9},
		{Document: &document.Document{ID: "same", Text: "second"}, Score: 0.8},
	}}
	query := &vectorstore.Query{Space: "space", Values: []float64{1, 0}}
	for _, validate := range []func() error{response.Validate, func() error { return query.ValidateResponse(response) }} {
		if err := validate(); !errors.Is(err, vectorstore.ErrInvalidResponse) {
			t.Fatalf("duplicate search identity admitted: %v", err)
		}
	}
	if _, err := jsonv2.Marshal(response); !errors.Is(err, vectorstore.ErrInvalidResponse) {
		t.Fatalf("duplicate search identity encoded: %v", err)
	}
	response.Results[1].Document.ID = "different"
	if err := query.ValidateResponse(response); err != nil {
		t.Fatalf("distinct identities rejected: %v", err)
	}
}
