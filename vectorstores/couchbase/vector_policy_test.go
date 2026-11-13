package couchbase

import (
	"cmp"
	jsonv2 "encoding/json/v2"
	"errors"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
)

func TestNativeVectorDomainAtEveryBoundary(t *testing.T) {
	for _, metric := range []Similarity{SimilarityCosine, SimilarityL2Norm, SimilarityDotProduct} {
		t.Run(metric.String(), func(t *testing.T) {
			for _, test := range []struct {
				name       string
				values     []float64
				invalid    bool
				cosineOnly bool
				// queryErr classifies the request when it differs from
				// ErrInvalidRequest.
				queryErr error
			}{
				{name: "float32 boundary", values: []float64{math.MaxFloat32, -math.MaxFloat32}},
				{name: "float64 precision", values: []float64{math.Nextafter(1, 2), 0}},
				{name: "small positive norm", values: []float64{1e-160, 0}},
				{name: "positive overflow", values: []float64{math.Nextafter(math.MaxFloat32, math.Inf(1)), 0}, invalid: true},
				{name: "negative overflow", values: []float64{math.Nextafter(-math.MaxFloat32, math.Inf(-1)), 0}, invalid: true},
				{name: "zero", values: []float64{0, 0}, cosineOnly: true},
				{name: "squared norm underflow", values: []float64{1e-200, 0}, cosineOnly: true},
				{name: "dimension", values: []float64{1}, invalid: true, queryErr: vectorstore.ErrIncompatibleSpace},
			} {
				t.Run(test.name, func(t *testing.T) {
					policy := vectorPolicy{dimensions: 2, similarity: metric}
					invalid := test.invalid || test.cosineOnly && metric == SimilarityCosine
					doc := &document.Document{ID: "invalid", Text: "text"}
					encoded, encodeErr := encodeStoredDocument(doc, test.values, policy)
					row, err := jsonv2.Marshal(storedRow{ID: doc.ID, CAS: 1, Record: storedDocument{Content: doc.Text, Metadata: "null", Embedding: test.values}})
					if err != nil {
						t.Fatal(err)
					}
					candidate, decodeErr := decodeStoredRow(row, policy)
					if !invalid {
						if encodeErr != nil || decodeErr != nil {
							t.Fatalf("valid native vector rejected: encode %v, decode %v", encodeErr, decodeErr)
						}
						var wire storedDocument
						if err := jsonv2.Unmarshal(encoded, &wire); err != nil {
							t.Fatal(err)
						}
						if !slices.Equal(wire.Embedding, test.values) || !slices.Equal(candidate.embedding, test.values) {
							t.Fatal("native admission changed coordinate values")
						}
						return
					}
					if !errors.Is(encodeErr, vectorstore.ErrInvalidRequest) || !errors.Is(decodeErr, vectorstore.ErrInvalidRequest) {
						t.Fatalf("invalid native vector admitted: encode %v, decode %v", encodeErr, decodeErr)
					}
					store := &Store{space: "s", vectors: policy}
					batch := &vectorstore.Batch{Space: "s", Records: []vectorstore.Record{
						{Document: &document.Document{ID: "valid", Text: "text"}, Values: []float64{1, 0}},
						{Document: doc, Values: test.values},
					}}
					if err := store.Upsert(t.Context(), batch); !errors.Is(err, vectorstore.ErrInvalidRequest) {
						t.Fatalf("invalid later record reached publication: %v", err)
					}
					query := &vectorstore.Query{Space: "s", Values: test.values}
					budget := vectorstore.ScanBudget{MaxRecords: 10, MaxBytes: 1 << 20, Timeout: time.Second}
					if result, err := store.QueryByScan(t.Context(), query, budget); result != nil || !errors.Is(err, cmp.Or(test.queryErr, vectorstore.ErrInvalidRequest)) {
						t.Fatalf("invalid query reached collection I/O: %v", err)
					}
				})
			}
		})
	}
}
