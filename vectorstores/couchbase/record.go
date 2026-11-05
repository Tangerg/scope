package couchbase

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"strings"

	"github.com/couchbase/gocb/v2"

	"github.com/Tangerg/scope/core/document"
	corevector "github.com/Tangerg/scope/core/vector"
	"github.com/Tangerg/scope/core/vectorstore"
)

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

func encodeStoredDocument(doc *document.Document, vector []float64) ([]byte, error) {
	encoded, err := doc.MarshalStoredMetadata()
	if err != nil {
		return nil, err
	}
	return jsonv2.Marshal(storedDocument{Content: doc.Text, Metadata: string(encoded), Embedding: vector})
}

func decodeStoredRow(raw []byte) (storedCandidate, error) {
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
	if strings.TrimSpace(doc.ID) == "" {
		return storedCandidate{}, vectorstore.ErrMissingDocumentID
	}
	if err := doc.Validate(); err != nil {
		return storedCandidate{}, fmt.Errorf("couchbase: invalid stored document: %w", err)
	}
	if err := row.Record.Embedding.Validate(); err != nil {
		return storedCandidate{}, fmt.Errorf("%w: couchbase: invalid stored embedding: %w", vectorstore.ErrInvalidRequest, err)
	}
	return storedCandidate{document: doc, embedding: row.Record.Embedding, cas: gocb.Cas(row.CAS)}, nil
}
