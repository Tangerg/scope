// Package jsonschema derives, parses, and validates JSON Schema contracts.
package jsonschema

import (
	"bytes"
	"encoding"
	"encoding/base64"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"net"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"

	reflection "github.com/invopop/jsonschema"
	validation "github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	maxDocumentBytes = 1 << 20
	definitionPrefix = "#/$defs/"
	resourceURL      = "urn:scope:json-schema"
)

var (
	ErrInvalid    = errors.New("jsonschema: invalid schema")
	ErrValidation = errors.New("jsonschema: instance validation failed")
)

// Modeler is implemented by values whose encoding/json/v2 representation is
// described by a separate typed model. Implementations must use a value
// receiver; nested and recursive models share one schema definition set.
type Modeler interface {
	// JSONSchemaAlias returns a non-nil typed value whose encoding/json/v2 wire shape
	// exactly matches the receiver's custom encoding. The concrete model type is
	// part of the schema contract and must remain stable across calls.
	JSONSchemaAlias() any
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

func (c Constraints) apply(schema *reflection.Schema) error {
	if c.Minimum != nil {
		schema.Minimum = json.Number(strconv.FormatFloat(*c.Minimum, 'g', -1, 64))
	}
	if c.Maximum != nil {
		schema.Maximum = json.Number(strconv.FormatFloat(*c.Maximum, 'g', -1, 64))
	}
	if c.MinLength != nil {
		schema.MinLength = c.MinLength
	}
	if c.MaxLength != nil {
		schema.MaxLength = c.MaxLength
	}
	if c.MinItems != nil {
		schema.MinItems = c.MinItems
	}
	if c.MaxItems != nil {
		schema.MaxItems = c.MaxItems
	}
	for name, constraint := range c.Properties {
		if schema.Properties == nil {
			return fmt.Errorf("constraint names unknown property %q", name)
		}
		property, exists := schema.Properties.Get(name)
		if !exists {
			return fmt.Errorf("constraint names unknown property %q", name)
		}
		if err := constraint.apply(property); err != nil {
			return err
		}
	}
	return nil
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

func reflectType(typeOf reflect.Type) (definition *reflection.Schema, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("unsupported type: %v", recovered)
		}
	}()
	named := make(map[string]reflect.Type)
	reflector := reflection.Reflector{
		Anonymous: true,
		Mapper:    reflectWireType,
		Namer: func(typeOf reflect.Type) string {
			name := qualifiedTypeName(typeOf)
			named[name] = typeOf
			return name
		},
	}
	reflected := reflector.ReflectFromType(typeOf)
	for name, definition := range reflected.Definitions {
		if owner := named[name]; owner != nil && owner.Implements(reflect.TypeFor[Constrained]()) {
			constraint := reflect.Zero(owner).Interface().(Constrained).JSONSchemaConstraints()
			if err := constraint.apply(definition); err != nil {
				return nil, err
			}
		}
	}
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
	requireValueReceiver(typeOf, reflect.TypeFor[Modeler](), "JSONSchemaAlias")
	requireValueReceiver(typeOf, reflect.TypeFor[Enumerator](), "JSONSchemaEnum")
	requireValueReceiver(typeOf, reflect.TypeFor[Patterned](), "JSONSchemaPattern")
	requireValueReceiver(typeOf, reflect.TypeFor[Constrained](), "JSONSchemaConstraints")
	switch typeOf {
	case reflect.TypeFor[json.RawMessage](), reflect.TypeFor[time.Time](), reflect.TypeFor[net.IP](), reflect.TypeFor[url.URL]():
		return nil
	case reflect.TypeFor[json.Number]():
		return &reflection.Schema{Type: "number"}
	}
	stringModel := typeOf.Kind() == reflect.String && (typeOf.Implements(reflect.TypeFor[Enumerator]()) || typeOf.Implements(reflect.TypeFor[Patterned]()))
	if !stringModel {
		for _, codec := range []reflect.Type{reflect.TypeFor[json.Marshaler](), reflect.TypeFor[jsonv2.MarshalerTo](), reflect.TypeFor[encoding.TextMarshaler]()} {
			if typeOf.Implements(codec) || reflect.PointerTo(typeOf).Implements(codec) {
				panic(fmt.Sprintf("%v: custom encoding requires JSONSchemaAlias", typeOf))
			}
		}

	}
	if typeOf == reflect.TypeFor[[]byte]() {
		return &reflection.Schema{OneOf: []*reflection.Schema{
			{Type: "null"},
			{Type: "string", ContentEncoding: "base64"},
		}}
	}
	var minimum, maximum string
	switch typeOf.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		bound := int64(^uint64(0) >> (65 - typeOf.Bits()))
		minimum = strconv.FormatInt(-bound-1, 10)
		maximum = strconv.FormatInt(bound, 10)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		minimum = "0"
		maximum = strconv.FormatUint(^uint64(0)>>(64-typeOf.Bits()), 10)
	default:
		value := reflect.Zero(typeOf).Interface()
		if constraint, ok := value.(Constrained); ok && typeOf.Kind() != reflect.Struct {
			definition := stringConstraint(value)
			if definition == nil {
				switch typeOf.Kind() {
				case reflect.Float32, reflect.Float64:
					definition = &reflection.Schema{Type: "number"}
				case reflect.String:
					definition = &reflection.Schema{Type: "string"}
				default:
					panic("constraints require a supported scalar or struct")
				}
			}
			owned := &reflection.Schema{}
			if err := constraint.JSONSchemaConstraints().apply(owned); err != nil {
				panic(err)
			}
			definition.AllOf = append(definition.AllOf, owned)
			return definition
		}
		return stringConstraint(value)
	}
	// The reflector writes field tags onto the outer schema. Keeping machine
	// bounds in an intersection lets tags narrow them without widening the type.
	definition := &reflection.Schema{Type: "integer", AllOf: []*reflection.Schema{{Minimum: json.Number(minimum), Maximum: json.Number(maximum)}}}
	if owner, ok := reflect.Zero(typeOf).Interface().(Constrained); ok {
		constraint := &reflection.Schema{}
		if err := owner.JSONSchemaConstraints().apply(constraint); err != nil {
			panic(err)
		}
		definition.AllOf = append(definition.AllOf, constraint)
	}
	return definition
}

func requireValueReceiver(typeOf, iface reflect.Type, method string) {
	if !typeOf.Implements(iface) && reflect.PointerTo(typeOf).Implements(iface) {
		panic(fmt.Sprintf("%v.%s requires a value receiver", typeOf, method))
	}
}

// stringConstraint derives the vocabulary a named string type owns. Enum and
// pattern live in an intersection so field tags written onto the outer schema can
// only narrow the owned set, never widen or replace it, mirroring integer bounds.
// A type that owns both an enum and a pattern contributes both; neither is dropped.
func stringConstraint(value any) *reflection.Schema {
	var owned []*reflection.Schema
	if enumerator, ok := value.(Enumerator); ok {
		members := enumerator.JSONSchemaEnum()
		enum := make([]any, len(members))
		for index, member := range members {
			enum[index] = member
		}
		owned = append(owned, &reflection.Schema{Enum: enum})
	}
	if patterned, ok := value.(Patterned); ok {
		owned = append(owned, &reflection.Schema{Pattern: patterned.JSONSchemaPattern()})
	}
	if owned == nil {
		return nil
	}
	return &reflection.Schema{Type: "string", AllOf: owned}
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
