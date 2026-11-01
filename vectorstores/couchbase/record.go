package couchbase

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/couchbase/gocb/v2"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
)

type storedDocument struct {
	Content  string `json:"content"`
	Metadata string `json:"metadata"`
	// Nullable wire elements distinguish JSON null from a genuine zero component.
	Embedding []*float64 `json:"embedding"`
}

type storedRow struct {
	ID     string         `json:"id"`
	CAS    uint64         `json:"cas"`
	Record storedDocument `json:"record"`
}

type storedCandidate struct {
	document  *document.Document
	embedding []float64
	cas       gocb.Cas
}

type vectorProjection struct {
	Position  int       `json:"position"`
	Embedding []float64 `json:"embedding"`
}

type distanceRow struct {
	Position *int     `json:"position"`
	Distance *float64 `json:"distance"`
}

type rankedResult struct {
	result   *vectorstore.SearchResult
	distance float64
}

func encodeStoredDocument(doc *document.Document, vector []float64) ([]byte, error) {
	encoded, err := document.MarshalStoredMetadata(doc)
	if err != nil {
		return nil, err
	}
	values := make([]*float64, len(vector))
	for i := range vector {
		values[i] = &vector[i]
	}
	return jsonv2.Marshal(storedDocument{Content: doc.Text, Metadata: string(encoded), Embedding: values})
}

func decodeStoredRow(raw []byte) (storedCandidate, error) {
	var row storedRow
	if err := jsonv2.Unmarshal(raw, &row, jsonv2.RejectUnknownMembers(true)); err != nil {
		return storedCandidate{}, fmt.Errorf("couchbase: decode stored record: %w", err)
	}
	if row.CAS == 0 {
		return storedCandidate{}, errors.New("couchbase: stored record has no CAS")
	}
	doc := new(document.Document)
	if err := document.UnmarshalStoredMetadata([]byte(row.Record.Metadata), doc); err != nil {
		return storedCandidate{}, fmt.Errorf("couchbase: decode metadata of %s: %w", row.ID, err)
	}
	doc.ID, doc.Text = row.ID, row.Record.Content
	if strings.TrimSpace(doc.ID) == "" {
		return storedCandidate{}, vectorstore.ErrMissingDocumentID
	}
	if err := doc.Validate(); err != nil {
		return storedCandidate{}, fmt.Errorf("couchbase: invalid stored document: %w", err)
	}
	vector := make([]float64, len(row.Record.Embedding))
	for i, value := range row.Record.Embedding {
		if value == nil {
			return storedCandidate{}, fmt.Errorf("couchbase: stored embedding[%d] is null", i)
		}
		vector[i] = *value
	}
	if err := validCoordinates(vector); err != nil {
		return storedCandidate{}, fmt.Errorf("couchbase: invalid stored embedding: %w", err)
	}
	return storedCandidate{document: doc, embedding: vector, cas: gocb.Cas(row.CAS)}, nil
}

func validCoordinates(v []float64) error {
	if len(v) == 0 {
		return vectorstore.ErrInvalidRequest
	}
	for _, x := range v {
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return vectorstore.ErrInvalidRequest
		}
	}
	return nil
}
