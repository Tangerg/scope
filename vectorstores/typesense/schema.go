package typesense

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"

	"github.com/typesense/typesense-go/v3/typesense/api"

	"github.com/Tangerg/scope/core/document"
	corevector "github.com/Tangerg/scope/core/vector"
	"github.com/Tangerg/scope/core/vectorstore"
)

type collectionSchema struct{ dimensions int }

// SpaceField binds a provisioned collection to its immutable encoder space.
func SpaceField(space vectorstore.SpaceID) api.Field {
	return api.Field{Name: space.StorageKey(), Type: fieldTypeString, Optional: new(true), Index: new(false)}
}

func newCollectionSchema(schema *api.CollectionResponse, name string, space vectorstore.SpaceID) (collectionSchema, error) {
	if schema == nil || schema.Name != name {
		return collectionSchema{}, fmt.Errorf("%w: native collection identity is invalid", ErrIncompatibleCollection)
	}
	fields := make(map[string]api.Field, len(schema.Fields))
	for _, field := range schema.Fields {
		if _, duplicate := fields[field.Name]; duplicate {
			return collectionSchema{}, fmt.Errorf("%w: duplicate field %s", ErrIncompatibleCollection, field.Name)
		}
		if field.Name == space.StorageKey() {
			if field.Type != fieldTypeString || field.Optional == nil || !*field.Optional || field.Index == nil || *field.Index || field.Embed != nil {
				return collectionSchema{}, ErrIncompatibleCollection
			}
			fields[field.Name] = field
			continue
		}
		switch field.Name {
		case fieldID, fieldContent, fieldMetadata, fieldEmbedding:
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
	if _, ok := fields[space.StorageKey()]; !ok {
		return collectionSchema{}, fmt.Errorf("%w: invalid coordinate space", ErrIncompatibleCollection)
	}
	content, contentOK := fields[fieldContent]
	facts, factsOK := fields[fieldMetadata]
	vector, vectorOK := fields[fieldEmbedding]
	if !contentOK || content.Type != fieldTypeString || (content.Index != nil && !*content.Index) || !factsOK || facts.Type != fieldTypeString || !vectorOK || vector.Type != fieldTypeFloatArray || vector.NumDim == nil || *vector.NumDim <= 0 || (vector.Index != nil && !*vector.Index) || (vector.VecDist != nil && *vector.VecDist != distanceCosine) {
		return collectionSchema{}, fmt.Errorf("%w: requires stored content and Core JSON strings plus an indexed cosine float[] embedding", ErrIncompatibleCollection)
	}
	if id, ok := fields[fieldID]; ok && id.Type != fieldTypeString {
		return collectionSchema{}, fmt.Errorf("%w: id is not a string", ErrIncompatibleCollection)
	}
	return collectionSchema{dimensions: *vector.NumDim}, nil
}

func (c collectionSchema) narrow(vector []float64) ([]float32, error) {
	narrowed := corevector.Float32(vector)
	if err := c.validateVector(narrowed); err != nil {
		return nil, err
	}
	return narrowed, nil
}

func (c collectionSchema) validateVector(vector []float32) error {
	if err := corevector.Coordinates[float32](vector).Validate(); err != nil {
		return err
	}
	if len(vector) != c.dimensions {
		return fmt.Errorf("typesense: vector dimension %d differs from native dimension %d", len(vector), c.dimensions)
	}
	nonzero := false
	for _, value := range vector {
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
	doc := new(document.Document)
	if err = doc.UnmarshalStoredMetadata([]byte(*record.Metadata)); err != nil {
		return storedRecord{}, nil, err
	}
	doc.ID, doc.Text = id, *record.Content
	if err = vectorstore.ValidateDocument(doc); err != nil {
		return storedRecord{}, nil, err
	}
	return record, doc, nil
}
