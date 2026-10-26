package jsonschema

import (
	"encoding"
	"encoding/base64"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"net"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	reflection "github.com/invopop/jsonschema"
	"github.com/samber/lo"
)

const (
	typeDefinitionPrefix = "go_"
	jsonFieldTag         = "json"
	jsonSchemaFieldTag   = "jsonschema"
	stringTagOption      = "string"
	ignoredFieldTag      = "-"
)

// derivation owns the definition set across nested wire projections. A model's
// placeholder exists before reflecting its wire type so recursive references
// retain the domain identity instead of re-entering its model method.
type derivation struct {
	definitions reflection.Definitions
	types       map[string]reflect.Type
	models      map[reflect.Type]*reflection.Schema
}

func reflectType(typeOf reflect.Type) (definition *reflection.Schema, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			if cause, ok := recovered.(error); ok {
				err = fmt.Errorf("unsupported type: %w", cause)
			} else {
				err = fmt.Errorf("unsupported type: %v", recovered)
			}
		}
	}()
	derivation := &derivation{
		definitions: make(reflection.Definitions),
		types:       make(map[string]reflect.Type),
		models:      make(map[reflect.Type]*reflection.Schema),
	}
	return derivation.derive(typeOf)
}

func (d *derivation) derive(typeOf reflect.Type) (*reflection.Schema, error) {
	root := d.reflect(typeOf)
	for model, definition := range d.models {
		if _, err := referencedShape(definition, d.definitions); err != nil {
			return nil, fmt.Errorf("model %v: %w", model, err)
		}
	}
	if err := d.applyConstraints(); err != nil {
		return nil, err
	}
	if name, referenced := strings.CutPrefix(root.Ref, definitionPrefix); referenced {
		definition := d.definitions[name]
		if definition == nil {
			return nil, fmt.Errorf("root definition %q is missing", name)
		}
		expanded := *definition
		root = &expanded
	}
	root.Version = reflection.Version
	root.Definitions = d.definitions
	return root, nil
}

func (d *derivation) reflect(typeOf reflect.Type) *reflection.Schema {
	reflector := reflection.Reflector{Anonymous: true, Mapper: d.mapType, Namer: d.typeName}
	shape := reflector.ReflectFromType(typeOf)
	for name, definition := range shape.Definitions {
		d.definitions[name] = definition
	}
	shape.Definitions = nil
	shape.Version = ""
	return shape
}

func (d *derivation) typeName(typeOf reflect.Type) string {
	// The reflector handles this hook before Mapper, which would bypass the
	// domain owner and silently reinstate the replaced modeling contract.
	if typeOf.Implements(reflect.TypeFor[interface{ JSONSchemaAlias() any }]()) {
		panic(fmt.Sprintf("%v: JSONSchemaAlias is unsupported; use JSONSchemaModel", typeOf))
	}
	name := qualifiedTypeName(typeOf)
	if name != "" {
		d.types[name] = typeOf
	}
	return name
}

func (d *derivation) mapType(typeOf reflect.Type) *reflection.Schema {
	requireValueReceiver(typeOf, reflect.TypeFor[Modeler](), "JSONSchemaModel")
	requireValueReceiver(typeOf, reflect.TypeFor[Enumerator](), "JSONSchemaEnum")
	requireValueReceiver(typeOf, reflect.TypeFor[Patterned](), "JSONSchemaPattern")
	requireValueReceiver(typeOf, reflect.TypeFor[Constrained](), "JSONSchemaConstraints")
	if typeOf.Implements(reflect.TypeFor[Modeler]()) {
		return d.model(typeOf)
	}
	return reflectWireType(typeOf)
}

func (d *derivation) model(typeOf reflect.Type) *reflection.Schema {
	definition, exists := d.models[typeOf]
	name := qualifiedTypeName(typeOf)
	if !exists {
		definition = &reflection.Schema{}
		d.models[typeOf] = definition
		if name != "" {
			d.definitions[name] = definition
		}
		model := reflect.Zero(typeOf).Interface().(Modeler).JSONSchemaModel()
		if lo.IsNil(model) {
			panic(fmt.Sprintf("%v.JSONSchemaModel returned nil", typeOf))
		}
		*definition = *d.reflect(reflect.TypeOf(model))
	}
	if name != "" {
		return &reflection.Schema{Ref: definitionPrefix + name}
	}
	return &reflection.Schema{AllOf: []*reflection.Schema{definition}}
}

func (d *derivation) applyConstraints() error {
	projections := make(map[*reflection.Schema]*reflection.Schema)
	for name, definition := range d.definitions {
		if err := d.projectConstraints(d.types[name], definition, projections); err != nil {
			return err
		}
	}
	for owner, definition := range d.models {
		if qualifiedTypeName(owner) == "" {
			if err := d.projectConstraints(owner, definition, projections); err != nil {
				return err
			}
		}
	}
	// Shape lookup observes only wire definitions, regardless of map order.
	// Refinements become intersections after every property path is validated.
	for definition, projection := range projections {
		definition.AllOf = append(definition.AllOf, projection)
	}
	return nil
}

func (d *derivation) projectConstraints(owner reflect.Type, definition *reflection.Schema, projections map[*reflection.Schema]*reflection.Schema) error {
	if owner == nil {
		return nil
	}
	value := reflect.Zero(owner).Interface()
	var projection *reflection.Schema
	if constrained, ok := value.(Constrained); ok {
		var err error
		projection, err = constrained.JSONSchemaConstraints().schema(definition, d.definitions)
		if err != nil {
			return fmt.Errorf("constraints for %v: %w", owner, err)
		}
	}
	if _, modeled := d.models[owner]; modeled {
		if stringSchema := stringConstraint(value); stringSchema != nil {
			if projection == nil {
				projection = &reflection.Schema{}
			}
			projection.AllOf = append(projection.AllOf, stringSchema)
		}
	}
	if projection != nil {
		projections[definition] = projection
	}
	return nil
}

func referencedShape(shape *reflection.Schema, definitions reflection.Definitions) (*reflection.Schema, error) {
	visited := make(map[*reflection.Schema]struct{})
	for shape != nil {
		if _, repeated := visited[shape]; repeated {
			return nil, errors.New("model references a cycle without a wire shape")
		}
		visited[shape] = struct{}{}
		if shape.Ref != "" {
			name, local := strings.CutPrefix(shape.Ref, definitionPrefix)
			if !local || definitions[name] == nil {
				return nil, fmt.Errorf("unknown schema definition %q", shape.Ref)
			}
			shape = definitions[name]
			continue
		}
		if shape.Type == "" && shape.Properties == nil && len(shape.AllOf) == 1 {
			shape = shape.AllOf[0]
			continue
		}
		return shape, nil
	}
	return nil, nil
}

func constraintObjectShape(shape *reflection.Schema, definitions reflection.Definitions) (*reflection.Schema, error) {
	shape, err := referencedShape(shape, definitions)
	if err != nil || shape == nil || len(shape.OneOf) != 2 {
		return shape, err
	}
	// invopop represents a nullable property as its wire shape plus null.
	// Only that shape supplies property names; the projection leaves null valid.
	left, right := shape.OneOf[0], shape.OneOf[1]
	if left.Type == schemaTypeNull && right.Type != schemaTypeNull {
		return referencedShape(right, definitions)
	}
	if right.Type == schemaTypeNull && left.Type != schemaTypeNull {
		return referencedShape(left, definitions)
	}
	return shape, nil
}

func reflectWireType(typeOf reflect.Type) *reflection.Schema {
	requireSupportedStringTags(typeOf)
	switch typeOf {
	case reflect.TypeFor[json.RawMessage](), reflect.TypeFor[time.Time](), reflect.TypeFor[net.IP](), reflect.TypeFor[url.URL]():
		return nil
	case reflect.TypeFor[json.Number]():
		return &reflection.Schema{Type: schemaTypeNumber}
	}
	stringModel := typeOf.Kind() == reflect.String && (typeOf.Implements(reflect.TypeFor[Enumerator]()) || typeOf.Implements(reflect.TypeFor[Patterned]()))
	if !stringModel && hasCustomEncoding(typeOf) {
		panic(fmt.Sprintf("%v: custom encoding requires JSONSchemaModel", typeOf))
	}
	if typeOf == reflect.TypeFor[[]byte]() {
		return &reflection.Schema{OneOf: []*reflection.Schema{
			{Type: schemaTypeNull},
			{Type: schemaTypeString, ContentEncoding: base64ContentEncoding},
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
					definition = &reflection.Schema{Type: schemaTypeNumber}
				case reflect.String:
					definition = &reflection.Schema{Type: schemaTypeString}
				default:
					panic("constraints require a supported scalar or struct")
				}
			}
			owned, err := constraint.JSONSchemaConstraints().schema(nil, nil)
			if err != nil {
				panic(err)
			}
			definition.AllOf = append(definition.AllOf, owned)
			return definition
		}
		return stringConstraint(value)
	}
	// The reflector writes field tags onto the outer schema. Keeping machine
	// bounds in an intersection lets tags narrow them without widening the type.
	definition := &reflection.Schema{Type: schemaTypeInteger, AllOf: []*reflection.Schema{{Minimum: json.Number(minimum), Maximum: json.Number(maximum)}}}
	if owner, ok := reflect.Zero(typeOf).Interface().(Constrained); ok {
		constraint, err := owner.JSONSchemaConstraints().schema(nil, nil)
		if err != nil {
			panic(err)
		}
		definition.AllOf = append(definition.AllOf, constraint)
	}
	return definition
}

func requireSupportedStringTags(typeOf reflect.Type) {
	if typeOf.Kind() != reflect.Struct {
		return
	}
	for field := range typeOf.Fields() {
		if !field.IsExported() && !field.Anonymous {
			continue
		}
		tags := strings.Split(field.Tag.Get(jsonFieldTag), ",")
		if tags[0] == ignoredFieldTag || field.Tag.Get(jsonSchemaFieldTag) == ignoredFieldTag || !slices.Contains(tags[1:], stringTagOption) {
			continue
		}
		member := field.Type
		for member.Kind() == reflect.Pointer {
			member = member.Elem()
		}
		if member.Implements(reflect.TypeFor[Modeler]()) && hasCustomEncoding(member) {
			continue
		}
		panic(fmt.Errorf("%w: field %v.%s", ErrUnsupportedStringTag, typeOf, field.Name))
	}
}

func hasCustomEncoding(typeOf reflect.Type) bool {
	for _, codec := range []reflect.Type{reflect.TypeFor[json.Marshaler](), reflect.TypeFor[jsonv2.MarshalerTo](), reflect.TypeFor[encoding.TextMarshaler]()} {
		if typeOf.Implements(codec) || reflect.PointerTo(typeOf).Implements(codec) {
			return true
		}
	}
	return false
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
	return &reflection.Schema{Type: schemaTypeString, AllOf: owned}
}

func qualifiedTypeName(typeOf reflect.Type) string {
	if typeOf.Name() == "" || typeOf.PkgPath() == "" {
		return ""
	}
	packageName := base64.RawURLEncoding.EncodeToString([]byte(typeOf.PkgPath()))
	return typeDefinitionPrefix + packageName + "_" + typeOf.Name()
}
