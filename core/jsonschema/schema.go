// Package jsonschema derives, parses, and validates JSON Schema contracts.
package jsonschema

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"reflect"
	"strconv"

	reflection "github.com/invopop/jsonschema"
	validation "github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	maxDocumentBytes      = 1 << 20
	definitionPrefix      = "#/$defs/"
	resourceURL           = "urn:scope:json-schema"
	schemaTypeNull        = "null"
	schemaTypeString      = "string"
	schemaTypeNumber      = "number"
	schemaTypeInteger     = "integer"
	base64ContentEncoding = "base64"
)

var (
	ErrInvalid    = errors.New("jsonschema: invalid schema")
	ErrValidation = errors.New("jsonschema: instance validation failed")
)

// Modeler is implemented by values whose encoding/json/v2 representation is
// described by a separate typed model. Implementations must use a value
// receiver; nested and recursive models share one schema definition set. Each
// domain type retains its own constraints, so sharing a wire model never couples
// unrelated domain rules.
type Modeler interface {
	// JSONSchemaModel returns a non-nil typed value whose encoding/json/v2 wire shape
	// exactly matches the receiver's custom encoding. The concrete model type is
	// part of the schema contract and must remain stable across calls.
	JSONSchemaModel() any
}

// Enumerator is implemented by named string types whose JSON value is one of a
// fixed set, so the schema derives the enum from the type that owns the
// vocabulary instead of restating it in field tags. Use a value receiver.
type Enumerator interface {
	JSONSchemaEnum() []string
}

// Patterned is implemented by named string types constrained to a regular
// expression, so the schema derives the pattern from the type that owns the
// rule instead of restating it in field tags. Use a value receiver.
type Patterned interface {
	JSONSchemaPattern() string
}

// Constrained projects domain-owned limits onto a type or its JSON properties.
// Runtime validation and schema generation must derive from the same declarations.
// Implementations use a value receiver.
type Constrained interface{ JSONSchemaConstraints() Constraints }

type Constraints struct {
	Minimum    *float64
	Maximum    *float64
	MinLength  *uint64
	MaxLength  *uint64
	MinItems   *uint64
	MaxItems   *uint64
	Properties map[string]Constraints
}

func (c Constraints) schema(shape *reflection.Schema, definitions reflection.Definitions) (*reflection.Schema, error) {
	schema := &reflection.Schema{
		MinLength: c.MinLength, MaxLength: c.MaxLength,
		MinItems: c.MinItems, MaxItems: c.MaxItems,
	}
	if c.Minimum != nil {
		schema.Minimum = json.Number(strconv.FormatFloat(*c.Minimum, 'g', -1, 64))
	}
	if c.Maximum != nil {
		schema.Maximum = json.Number(strconv.FormatFloat(*c.Maximum, 'g', -1, 64))
	}
	if len(c.Properties) == 0 {
		return schema, nil
	}
	shape, err := constraintObjectShape(shape, definitions)
	if err != nil {
		return nil, err
	}
	schema.Properties = reflection.NewProperties()
	for name, constraint := range c.Properties {
		if shape == nil || shape.Properties == nil {
			return nil, fmt.Errorf("constraint names unknown property %q", name)
		}
		property, exists := shape.Properties.Get(name)
		if !exists {
			return nil, fmt.Errorf("constraint names unknown property %q", name)
		}
		projected, err := constraint.schema(property, definitions)
		if err != nil {
			return nil, err
		}
		schema.Properties.Set(name, projected)
	}
	return schema, nil
}

// Schema is an immutable, compiled JSON Schema. Its zero value is invalid and
// successfully constructed values are safe for concurrent validation.
type Schema struct {
	raw      json.RawMessage
	compiled *validation.Schema
}

// For derives and compiles the JSON Schema contract for T. It follows
// encoding/json/v2 field names and options and the invopop/jsonschema tag dialect.
// Structs reject additional properties by default. Integer bounds preserve the
// Go representation even when field tags specify a wider semantic range.
// Named definitions include their Go package; moving a type changes its canonical
// schema and any deployment identities derived from it.
// Custom encodings require Modeler, except standard-library types with known
// wire contracts. Opaque Schema values cannot be derived from their Go fields.
func For[T any]() (Schema, error) {
	typeOf := reflect.TypeFor[T]()
	definition, err := reflectType(typeOf)
	if err != nil {
		return Schema{}, fmt.Errorf("%w: derive %v: %w", ErrInvalid, typeOf, err)
	}
	raw, err := jsonv2.Marshal(definition)
	if err != nil {
		return Schema{}, fmt.Errorf("%w: encode derived schema: %w", ErrInvalid, err)
	}
	return Parse(raw)
}

// Parse validates, compiles, and takes ownership of one self-contained JSON Schema
// document. References may resolve within the document or to standard metaschemas;
// implicit loading of external resources is prohibited.
func Parse(raw []byte) (Schema, error) {
	normalized, err := normalize(raw)
	if err != nil {
		return Schema{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	document, err := validation.UnmarshalJSON(bytes.NewReader(normalized))
	if err != nil {
		return Schema{}, fmt.Errorf("%w: decode: %w", ErrInvalid, err)
	}
	compiler := validation.NewCompiler()
	compiler.UseLoader(nil)
	compiler.DefaultDraft(validation.Draft2020)
	compiler.AssertFormat()
	if err = compiler.AddResource(resourceURL, document); err != nil {
		return Schema{}, fmt.Errorf("%w: add resource: %w", ErrInvalid, err)
	}
	compiled, err := compiler.Compile(resourceURL)
	if err != nil {
		return Schema{}, fmt.Errorf("%w: compile: %w", ErrInvalid, err)
	}
	return Schema{raw: normalized, compiled: compiled}, nil
}

func normalize(raw []byte) (json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, errors.New("document is empty")
	}
	if len(raw) > maxDocumentBytes {
		return nil, fmt.Errorf("document exceeds %d bytes", maxDocumentBytes)
	}
	if !jsontext.Value(raw).IsValid() {
		return nil, errors.New("document is not valid RFC 7493 JSON")
	}
	normalized := jsontext.Value(bytes.Clone(raw))
	if err := normalized.Format(jsontext.ReorderRawObjects(true)); err != nil {
		return nil, fmt.Errorf("normalize document: %w", err)
	}
	if len(normalized) > maxDocumentBytes {
		return nil, fmt.Errorf("normalized document exceeds %d bytes", maxDocumentBytes)
	}
	return normalized, nil
}

func (s Schema) JSON() json.RawMessage { return bytes.Clone(s.raw) }

func (s Schema) Valid() bool { return len(s.raw) > 0 && s.compiled != nil }

// Equal compares valid schemas by their canonical JSON contract.
func (s Schema) Equal(other Schema) bool {
	return s.Valid() && other.Valid() && bytes.Equal(s.raw, other.raw)
}

// Validate requires one RFC 7493 JSON document and validates it against the
// compiled schema without rounding JSON numbers through floating-point values.
func (s Schema) Validate(raw []byte) error {
	if !s.Valid() {
		return ErrInvalid
	}
	if !jsontext.Value(raw).IsValid() {
		return fmt.Errorf("%w: value must be one valid RFC 7493 JSON document", ErrValidation)
	}
	value, err := validation.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("%w: decode value: %w", ErrValidation, err)
	}
	if err := s.compiled.Validate(value); err != nil {
		return fmt.Errorf("%w: %w", ErrValidation, err)
	}
	return nil
}

func (s Schema) MarshalJSON() ([]byte, error) {
	if !s.Valid() {
		return nil, ErrInvalid
	}
	return bytes.Clone(s.raw), nil
}

func (s *Schema) UnmarshalJSON(raw []byte) error {
	if s == nil {
		return fmt.Errorf("%w: nil receiver", ErrInvalid)
	}
	parsed, err := Parse(raw)
	if err != nil {
		return err
	}
	*s = parsed
	return nil
}
