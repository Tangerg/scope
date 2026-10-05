package typesense

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"math"

	"github.com/typesense/typesense-go/v3/typesense/api"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
)

type collectionSchema struct{ dimensions int }

func newCollectionSchema(schema *api.CollectionResponse, name string) (collectionSchema, error) {
	if schema == nil || schema.Name != name {
		return collectionSchema{}, fmt.Errorf("%w: native collection identity is invalid", ErrIncompatibleCollection)
	}
	fields := make(map[string]api.Field, len(schema.Fields))
	for _, field := range schema.Fields {
		if _, duplicate := fields[field.Name]; duplicate {
			return collectionSchema{}, fmt.Errorf("%w: duplicate field %s", ErrIncompatibleCollection, field.Name)
		}
		switch field.Name {
		case "id", "content", "metadata", "embedding":
		default:
			return collectionSchema{}, fmt.Errorf("%w: unexpected field %s", ErrIncompatibleCollection, field.Name)
		}
		if field.Store != nil && !*field.Store {
			return collectionSchema{}, fmt.Errorf("%w: field %s is not stored", ErrIncompatibleCollection, field.Name)
		}
		if field.Embed != nil {
			return collectionSchema{}, fmt.Errorf("%w: native auto-embedding would create a second vector owner", ErrIncompatibleCollection)
		}
		fields[field.Name] = field
	}
	content, contentOK := fields["content"]
	facts, factsOK := fields["metadata"]
	vector, vectorOK := fields["embedding"]
	if !contentOK || content.Type != "string" || (content.Index != nil && !*content.Index) || !factsOK || facts.Type != "string" || !vectorOK || vector.Type != "float[]" || vector.NumDim == nil || *vector.NumDim <= 0 || (vector.Index != nil && !*vector.Index) || (vector.VecDist != nil && *vector.VecDist != "cosine") {
		return collectionSchema{}, fmt.Errorf("%w: requires stored content and Core JSON strings plus an indexed cosine float[] embedding", ErrIncompatibleCollection)
	}
	if id, ok := fields["id"]; ok && id.Type != "string" {
		return collectionSchema{}, fmt.Errorf("%w: id is not a string", ErrIncompatibleCollection)
	}
	return collectionSchema{dimensions: *vector.NumDim}, nil
}

func (c collectionSchema) narrow(vector []float64) ([]float32, error) {
	narrowed := embedding.Float32Vector(vector)
	if err := c.validateVector(narrowed); err != nil {
		return nil, err
	}
	return narrowed, nil
}

func (c collectionSchema) validateVector(vector []float32) error {
	if len(vector) != c.dimensions {
		return fmt.Errorf("typesense: vector dimension %d differs from native dimension %d", len(vector), c.dimensions)
	}
	nonzero := false
	for _, value := range vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return errors.New("typesense: vector is not finite float32")
		}
		nonzero = nonzero || value != 0
	}
	if !nonzero {
		return errors.New("typesense: cosine vector became all zero in float32")
	}
	return nil
}

func (c collectionSchema) decodeDocument(raw []byte) (storedRecord, *document.Document, error) {
	var record storedRecord
	if err := jsonv2.Unmarshal(raw, &record, jsonv2.RejectUnknownMembers(true)); err != nil {
		return storedRecord{}, nil, err
	}
	if record.ID == nil || record.Content == nil || record.Metadata == nil || record.Embedding == nil {
		return storedRecord{}, nil, errors.New("typesense: record is missing required current fields")
	}
	id, err := decodeKey(*record.ID)
	if err != nil {
		return storedRecord{}, nil, err
	}
	if err = c.validateVector(*record.Embedding); err != nil {
		return storedRecord{}, nil, err
	}
	var facts metadata.Map
	if err = facts.UnmarshalJSON([]byte(*record.Metadata)); err != nil {
		return storedRecord{}, nil, err
	}
	doc := &document.Document{ID: id, Text: *record.Content, Metadata: facts}
	if err = (&vectorstore.IndexRequest{Documents: []*document.Document{doc}}).Validate(); err != nil {
		return storedRecord{}, nil, err
	}
	return record, doc, nil
}
