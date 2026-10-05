package couchbase

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"

	"github.com/couchbase/gocb/v2"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/metadata"
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

func encodeStoredDocument(text string, facts metadata.Map, vector []float64) ([]byte, error) {
	encoded, err := facts.MarshalJSON()
	if err != nil {
		return nil, err
	}
	values := make([]*float64, len(vector))
	for i := range vector {
		values[i] = &vector[i]
	}
	return jsonv2.Marshal(storedDocument{Content: text, Metadata: string(encoded), Embedding: values})
}

func decodeStoredRow(raw []byte) (storedCandidate, error) {
	var row storedRow
	if err := jsonv2.Unmarshal(raw, &row, jsonv2.RejectUnknownMembers(true)); err != nil {
		return storedCandidate{}, fmt.Errorf("couchbase: decode stored record: %w", err)
	}
	if row.CAS == 0 {
		return storedCandidate{}, errors.New("couchbase: stored record has no CAS")
	}
	var facts metadata.Map
	if err := facts.UnmarshalJSON([]byte(row.Record.Metadata)); err != nil {
		return storedCandidate{}, fmt.Errorf("couchbase: decode metadata of %s: %w", row.ID, err)
	}
	doc := &document.Document{ID: row.ID, Text: row.Record.Content, Metadata: facts}
	if err := (&vectorstore.IndexRequest{Documents: []*document.Document{doc}}).Validate(); err != nil {
		return storedCandidate{}, fmt.Errorf("couchbase: invalid stored document: %w", err)
	}
	vector := make([]float64, len(row.Record.Embedding))
	for i, value := range row.Record.Embedding {
		if value == nil {
			return storedCandidate{}, fmt.Errorf("couchbase: stored embedding[%d] is null", i)
		}
		vector[i] = *value
	}
	if err := (&embedding.Output{Embedding: vector}).Validate(); err != nil {
		return storedCandidate{}, fmt.Errorf("couchbase: invalid stored embedding: %w", err)
	}
	return storedCandidate{document: doc, embedding: vector, cas: gocb.Cas(row.CAS)}, nil
}
