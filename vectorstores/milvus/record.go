package milvus

import (
	"errors"
	"fmt"

	"github.com/milvus-io/milvus/client/v3/column"
	"github.com/milvus-io/milvus/client/v3/entity"
	"github.com/milvus-io/milvus/client/v3/milvusclient"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

type nativeRecord struct {
	doc      *document.Document
	metadata string
}

type rankedDocument struct {
	result *vectorstore.SearchResult
	raw    float64
}

func encodeDocuments(docs []*document.Document, policy nativePolicy) ([]nativeRecord, error) {
	records := make([]nativeRecord, len(docs))
	for position, doc := range docs {
		if len(doc.ID) > policy.idBytes {
			return nil, ErrDocumentIDTooLong
		}
		if len(doc.Text) > policy.contentBytes {
			return nil, ErrDocumentContentTooLong
		}
		facts, err := doc.MarshalStoredMetadata()
		if err != nil {
			return nil, err
		}
		if len(facts) > policy.metadataBytes {
			return nil, errors.New("milvus: metadata exceeds the native VARCHAR capacity")
		}
		records[position] = nativeRecord{doc: doc, metadata: string(facts)}
	}
	return records, nil
}

func encodeColumns(records []nativeRecord, vectors [][]float64, policy nativePolicy) ([]column.Column, error) {
	if len(vectors) != len(records) || len(records) == 0 {
		return nil, errors.New("milvus: embedding count is inconsistent")
	}
	ids, content, facts := make([]string, len(records)), make([]string, len(records)), make([]string, len(records))
	dense := make([][]float32, len(records))
	for position, record := range records {
		ids[position], content[position], facts[position] = record.doc.ID, record.doc.Text, record.metadata
		vector, err := policy.vector(vectors[position])
		if err != nil {
			return nil, err
		}
		dense[position] = vector
	}
	return []column.Column{
		column.NewColumnVarChar(fieldID, ids), column.NewColumnVarChar(fieldContent, content),
		column.NewColumnVarChar(fieldMeta, facts), column.NewColumnFloatVector(fieldVector, policy.dimensions, dense),
	}, nil
}

func decodeRows(rows milvusclient.ResultSet, policy nativePolicy) ([]nativeRecord, error) {
	columns, err := currentColumns(rows)
	if err != nil || columns == nil {
		return nil, err
	}
	records := make([]nativeRecord, rows.Len())
	for position := range rows.Len() {
		if records[position], err = columns.record(position, policy); err != nil {
			return nil, err
		}
	}
	return records, nil
}

// nativeColumns holds the four current columns of one result set by name.
type nativeColumns map[string]column.Column

// currentColumns returns nil for a result without rows or columns.
func currentColumns(rows milvusclient.ResultSet) (nativeColumns, error) {
	if rows.Err != nil {
		return nil, rows.Err
	}
	if rows.Len() < 0 {
		return nil, errors.New("milvus: negative result count")
	}
	if rows.Len() == 0 && len(rows.Fields) == 0 {
		return nil, nil
	}
	if len(rows.Fields) != 4 {
		return nil, errors.New("milvus: native rows do not have the current four-column shape")
	}
	columns := make(nativeColumns, 4)
	for _, value := range rows.Fields {
		if value == nil || columns[value.Name()] != nil || value.Len() != rows.Len() {
			return nil, errors.New("milvus: native column shape is inconsistent")
		}
		columns[value.Name()] = value
	}
	for _, name := range []string{fieldID, fieldContent, fieldMeta, fieldVector} {
		if columns[name] == nil {
			return nil, fmt.Errorf("milvus: missing native column %s", name)
		}
	}
	return columns, nil
}

func (n nativeColumns) record(position int, policy nativePolicy) (nativeRecord, error) {
	id, err := n.varChar(fieldID, position)
	if err != nil {
		return nativeRecord{}, err
	}
	content, err := n.varChar(fieldContent, position)
	if err != nil {
		return nativeRecord{}, err
	}
	facts, err := n.varChar(fieldMeta, position)
	if err != nil {
		return nativeRecord{}, err
	}
	doc := new(document.Document)
	if err = doc.UnmarshalStoredMetadata([]byte(facts)); err != nil {
		return nativeRecord{}, err
	}
	doc.ID, doc.Text = id, content
	if err = vectorstore.ValidateDocument(doc); err != nil {
		return nativeRecord{}, err
	}
	if len(id) > policy.idBytes || len(content) > policy.contentBytes || len(facts) > policy.metadataBytes {
		return nativeRecord{}, errors.New("milvus: native value exceeds its VARCHAR capacity")
	}
	raw, err := n[fieldVector].Get(position)
	if err != nil {
		return nativeRecord{}, err
	}
	vector, ok := raw.(entity.FloatVector)
	if !ok {
		return nativeRecord{}, errors.New("milvus: native vector is not FLOAT_VECTOR")
	}
	if err := policy.validateVector(vector); err != nil {
		return nativeRecord{}, err
	}
	return nativeRecord{doc: doc, metadata: facts}, nil
}

func (n nativeColumns) varChar(name string, position int) (string, error) {
	raw, err := n[name].Get(position)
	if err != nil {
		return "", err
	}
	value, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("milvus: column %s is not a non-null VARCHAR", name)
	}
	return value, nil
}

func matchesMetadata(predicate filter.Predicate, doc *document.Document) (bool, error) {
	if predicate == nil {
		return true, nil
	}
	return filter.Match(predicate, doc.Metadata)
}
