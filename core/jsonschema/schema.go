// Package jsonschema derives, parses, and validates JSON Schema contracts.
package jsonschema

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"

	reflection "github.com/invopop/jsonschema"
	validation "github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	maxDocumentBytes = 1 << 20
	definitionPrefix = "#/$defs/"
	resourceURL      = "urn:scope:json-schema"
)

var ErrInvalid = errors.New("jsonschema: invalid schema")

// Modeler is implemented by rich values whose encoding/json representation is
// described by a separate typed model. Implementations must return the same
// non-nil model type on every call and use a value receiver. The native reflector
// keeps nested and recursive model references in the same schema definition set.
type Modeler interface {
	// JSONSchemaAlias returns a non-nil typed value whose encoding/json wire shape
	// exactly matches the receiver's custom encoding. The concrete model type is
	// part of the schema contract and must remain stable across calls.
	JSONSchemaAlias() any
}

// Schema is an immutable, compiled JSON Schema. Its zero value is invalid and
// successfully constructed values are safe for concurrent validation.
type Schema struct {
	raw      json.RawMessage
	compiled *validation.Schema
}

// For derives and compiles the JSON Schema contract for T. It follows
// encoding/json field names and options and the invopop/jsonschema tag dialect.
// Structs reject additional properties by default.
func For[T any]() (Schema, error) {
	typeOf := reflect.TypeFor[T]()
	definition, err := reflectType(typeOf)
	if err != nil {
		return Schema{}, fmt.Errorf("%w: derive %v: %w", ErrInvalid, typeOf, err)
	}
	raw, err := json.Marshal(definition)
	if err != nil {
		return Schema{}, fmt.Errorf("%w: encode derived schema: %w", ErrInvalid, err)
	}
	return Parse(raw)
}

// Parse validates, compiles, and takes ownership of one JSON Schema document.
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

func reflectType(typeOf reflect.Type) (definition *reflection.Schema, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("unsupported type: %v", recovered)
		}
	}()
	reflector := reflection.Reflector{
		Anonymous: true,
		Mapper:    reflectWireType,
		Namer:     qualifiedTypeName,
	}
	reflected := reflector.ReflectFromType(typeOf)
	name, referenced := strings.CutPrefix(reflected.Ref, definitionPrefix)
	if !referenced {
		return reflected, nil
	}
	root, exists := reflected.Definitions[name]
	if !exists {
		return nil, fmt.Errorf("root definition %q is missing", name)
	}
	expanded := *root
	expanded.Version = reflected.Version
	expanded.Definitions = reflected.Definitions
	return &expanded, nil
}

func reflectWireType(typeOf reflect.Type) *reflection.Schema {
	modeler := reflect.TypeFor[Modeler]()
	if !typeOf.Implements(modeler) && reflect.PointerTo(typeOf).Implements(modeler) {
		panic(fmt.Sprintf("%v.JSONSchemaAlias requires a value receiver", typeOf))
	}
	if typeOf == reflect.TypeFor[[]byte]() {
		return &reflection.Schema{OneOf: []*reflection.Schema{
			{Type: "null"},
			{Type: "string", ContentEncoding: "base64"},
		}}
	}
	return nil
}

func qualifiedTypeName(typeOf reflect.Type) string {
	if typeOf.Name() == "" || typeOf.PkgPath() == "" {
		return ""
	}
	packageName := base64.RawURLEncoding.EncodeToString([]byte(typeOf.PkgPath()))
	return "go_" + packageName + "_" + typeOf.Name()
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
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("decode document: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("document contains multiple JSON values")
		}
		return nil, fmt.Errorf("decode trailing document data: %w", err)
	}
	normalized, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("normalize document: %w", err)
	}
	if len(normalized) > maxDocumentBytes {
		return nil, fmt.Errorf("normalized document exceeds %d bytes", maxDocumentBytes)
	}
	return normalized, nil
}

func (s Schema) JSON() json.RawMessage { return bytes.Clone(s.raw) }

func (s Schema) Valid() bool { return len(s.raw) > 0 && s.compiled != nil }

func (s Schema) Validate(raw []byte) error {
	if !s.Valid() {
		return ErrInvalid
	}
	value, err := validation.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("jsonschema: decode value: %w", err)
	}
	if err := s.compiled.Validate(value); err != nil {
		return fmt.Errorf("jsonschema: validate value: %w", err)
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
