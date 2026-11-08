package couchbase

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
)

func TestEncodedDocumentSizeBoundary(t *testing.T) {
	const nativeLimit = 20 << 20
	const framing = `{"content":"","metadata":"null","embedding":[1,0]}`
	policy := vectorPolicy{dimensions: 2, similarity: SimilarityCosine}
	for _, delta := range []int{-1, 0, 1} {
		t.Run(strconv.Itoa(delta), func(t *testing.T) {
			doc := &document.Document{ID: "document", Text: strings.Repeat("a", nativeLimit-len(framing)+delta)}
			encoded, err := encodeStoredDocument(doc, []float64{1, 0}, policy)
			if delta > 0 {
				if !errors.Is(err, vectorstore.ErrInvalidDocument) {
					t.Fatalf("oversized JSON accepted: %v", err)
				}
				return
			}
			if err != nil || len(encoded) != nativeLimit+delta {
				t.Fatalf("document at native boundary rejected: bytes %d, error %v", len(encoded), err)
			}
		})
	}
}

func TestWholeBatchRejectsEncodedSizeBeforePublication(t *testing.T) {
	for _, name := range []string{"text", "escaped text", "text and metadata"} {
		t.Run(name, func(t *testing.T) {
			doc := &document.Document{ID: "oversized"}
			switch name {
			case "text":
				doc.Text = strings.Repeat("a", 20<<20)
			case "escaped text":
				doc.Text = strings.Repeat(`"`, 10<<20)
			case "text and metadata":
				doc.Text = strings.Repeat("a", 10<<20)
				doc.Metadata = metadata.Map{"payload": json.RawMessage(`"` + strings.Repeat("a", 10<<20) + `"`)}
			}
			store := &Store{space: "s", vectors: vectorPolicy{dimensions: 2, similarity: SimilarityCosine}}
			batch := &vectorstore.Batch{Space: "s", Records: []vectorstore.Record{
				{Document: &document.Document{ID: "valid", Text: "text"}, Values: []float64{1, 0}},
				{Document: doc, Values: []float64{1, 0}},
			}}
			if err := store.Upsert(t.Context(), batch); !errors.Is(err, vectorstore.ErrInvalidDocument) {
				t.Fatalf("oversized later record reached publication: %v", err)
			}
		})
	}
}
