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
	fields, err := documentFields(schema.Fields, space)
	if err != nil {
		return collectionSchema{}, err
	}
	content, contentOK := fields[fieldContent]
	facts, factsOK := fields[fieldMetadata]
	vector, vectorOK := fields[fieldEmbedding]
	if !contentOK || !indexedString(content) || !factsOK || facts.Type != fieldTypeString || !vectorOK || !cosineEmbedding(vector) {
		return collectionSchema{}, fmt.Errorf("%w: requires stored content and Core JSON strings plus an indexed cosine float[] embedding", ErrIncompatibleCollection)
	}
	if id, ok := fields[fieldID]; ok && id.Type != fieldTypeString {
		return collectionSchema{}, fmt.Errorf("%w: id is not a string", ErrIncompatibleCollection)
	}
	return collectionSchema{dimensions: *vector.NumDim}, nil
}

// documentFields admits exactly the space binding and the current document
// fields, keyed by name. No field may be unstored or natively embedded.
func documentFields(native []api.Field, space vectorstore.SpaceID) (map[string]api.Field, error) {
	fields := make(map[string]api.Field, len(native))
	for _, field := range native {
		if _, duplicate := fields[field.Name]; duplicate {
			return nil, fmt.Errorf("%w: duplicate field %s", ErrIncompatibleCollection, field.Name)
		}
		if err := admitField(field, space); err != nil {
			return nil, err
		}
		fields[field.Name] = field
	}
	if _, ok := fields[space.StorageKey()]; !ok {
		return nil, fmt.Errorf("%w: invalid coordinate space", ErrIncompatibleCollection)
	}
	return fields, nil
}

func admitField(field api.Field, space vectorstore.SpaceID) error {
	if field.Name == space.StorageKey() {
		if !spaceBinding(field) {
			return ErrIncompatibleCollection
		}
		return nil
	}
	switch field.Name {
	case fieldID, fieldContent, fieldMetadata, fieldEmbedding:
	default:
		return fmt.Errorf("%w: unexpected field %s", ErrIncompatibleCollection, field.Name)
	}
	if field.Store != nil && !*field.Store {
		return fmt.Errorf("%w: field %s is not stored", ErrIncompatibleCollection, field.Name)
	}
	if field.Embed != nil {
		return fmt.Errorf("%w: native auto-embedding would create a second vector owner", ErrIncompatibleCollection)
	}
	return nil
}

// spaceBinding is an optional, unindexed string that only names the space.
func spaceBinding(field api.Field) bool {
	return field.Type == fieldTypeString && field.Optional != nil && *field.Optional && field.Index != nil && !*field.Index && field.Embed == nil
}

func indexedString(field api.Field) bool {
	return field.Type == fieldTypeString && (field.Index == nil || *field.Index)
}

func cosineEmbedding(field api.Field) bool {
	return field.Type == fieldTypeFloatArray && field.NumDim != nil && *field.NumDim > 0 &&
		(field.Index == nil || *field.Index) && (field.VecDist == nil || *field.VecDist == distanceCosine)
}

func (c collectionSchema) narrow(vector []float64) ([]float32, error) {
	if err := vectorstore.RequireWidth(vector, c.dimensions); err != nil {
		return nil, err
	}
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
	return corevector.Coordinates[float32](vector).ValidateDirection()
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
