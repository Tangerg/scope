package milvus

import (
	"errors"
	"fmt"

	"github.com/milvus-io/milvus/client/v2/column"
	"github.com/milvus-io/milvus/client/v2/entity"
	"github.com/milvus-io/milvus/client/v2/milvusclient"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/metadata"
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
		facts, err := doc.Metadata.MarshalJSON()
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
	columns := make(map[string]column.Column, 4)
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
	records := make([]nativeRecord, rows.Len())
	for position := range rows.Len() {
		var values [3]string
		for index, name := range []string{fieldID, fieldContent, fieldMeta} {
			raw, err := columns[name].Get(position)
			if err != nil {
				return nil, err
			}
			value, ok := raw.(string)
			if !ok {
				return nil, fmt.Errorf("milvus: column %s is not a non-null VARCHAR", name)
			}
			values[index] = value
		}
		var facts metadata.Map
		if err := facts.UnmarshalJSON([]byte(values[2])); err != nil {
			return nil, err
		}
		doc := &document.Document{ID: values[0], Text: values[1], Metadata: facts}
		if err := (&vectorstore.IndexRequest{Documents: []*document.Document{doc}}).Validate(); err != nil {
			return nil, err
		}
		if len(values[0]) > policy.idBytes || len(values[1]) > policy.contentBytes || len(values[2]) > policy.metadataBytes {
			return nil, errors.New("milvus: native value exceeds its VARCHAR capacity")
		}
		raw, err := columns[fieldVector].Get(position)
		if err != nil {
			return nil, err
		}
		vector, ok := raw.(entity.FloatVector)
		if !ok {
			return nil, errors.New("milvus: native vector is not FLOAT_VECTOR")
		}
		if err := policy.validateVector(vector); err != nil {
			return nil, err
		}
		records[position] = nativeRecord{doc: doc, metadata: values[2]}
	}
	return records, nil
}

func matchesMetadata(predicate filter.Predicate, doc *document.Document) (bool, error) {
	if predicate == nil {
		return true, nil
	}
	values, err := doc.Metadata.Values()
	if err != nil {
		return false, err
	}
	return filter.Match(predicate, values)
}
