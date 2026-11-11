package couchbase

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"

	"github.com/couchbase/gocb/v2"

	"github.com/Tangerg/scope/core/document"
	corevector "github.com/Tangerg/scope/core/vector"
	"github.com/Tangerg/scope/core/vectorstore"
)

const maximumDocumentBytes = 20 << 20

type storedDocument struct {
	Content   string                          `json:"content"`
	Metadata  string                          `json:"metadata"`
	Embedding corevector.Coordinates[float64] `json:"embedding"`
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

func encodeStoredDocument(doc *document.Document, vector []float64, policy vectorPolicy) ([]byte, error) {
	if err := policy.validate(vector); err != nil {
		return nil, err
	}
	encoded, err := doc.MarshalStoredMetadata()
	if err != nil {
		return nil, err
	}
	payload, err := jsonv2.Marshal(storedDocument{Content: doc.Text, Metadata: string(encoded), Embedding: vector})
	if err != nil {
		return nil, err
	}
	// Couchbase applies its value limit after decompression. JSON framing,
	// escaped text, metadata and coordinates all consume that same budget.
	if len(payload) > maximumDocumentBytes {
		return nil, fmt.Errorf("couchbase: %w: encoded document requires %d bytes; native value limit is %d", vectorstore.ErrInvalidDocument, len(payload), maximumDocumentBytes)
	}
	return payload, nil
}

func decodeStoredRow(raw []byte, policy vectorPolicy) (storedCandidate, error) {
	var row storedRow
	if err := jsonv2.Unmarshal(raw, &row, jsonv2.RejectUnknownMembers(true)); err != nil {
		if errors.Is(err, corevector.ErrInvalidCoordinates) {
			err = fmt.Errorf("%w: %w", vectorstore.ErrInvalidRequest, err)
		}
		return storedCandidate{}, fmt.Errorf("couchbase: decode stored record: %w", err)
	}
	if row.CAS == 0 {
		return storedCandidate{}, errors.New("couchbase: stored record has no CAS")
	}
	doc := new(document.Document)
	if err := doc.UnmarshalStoredMetadata([]byte(row.Record.Metadata)); err != nil {
		return storedCandidate{}, fmt.Errorf("couchbase: decode metadata of %s: %w", row.ID, err)
	}
	doc.ID, doc.Text = row.ID, row.Record.Content
	if err := vectorstore.ValidateDocument(doc); err != nil {
		return storedCandidate{}, fmt.Errorf("couchbase: invalid stored document: %w", err)
	}
	if err := policy.validate(row.Record.Embedding); err != nil {
		return storedCandidate{}, fmt.Errorf("couchbase: invalid stored embedding for %q: %w", row.ID, err)
	}
	return storedCandidate{document: doc, embedding: row.Record.Embedding, cas: gocb.Cas(row.CAS)}, nil
}
