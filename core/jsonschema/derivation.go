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

const typeDefinitionPrefix = "go_"

// derivation owns the definition set across nested wire projections. A model's
// placeholder exists before reflecting its wire type so recursive references
// retain the domain identity instead of re-entering its model method. Inline
// wire projections retain their constraint sources when a domain copies them.
type derivation struct {
	definitions       reflection.Definitions
	projections       map[reflect.Type]*reflection.Schema
	constraintSources map[*reflection.Schema][]reflect.Type
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
		definitions:       make(reflection.Definitions),
		projections:       make(map[reflect.Type]*reflection.Schema),
		constraintSources: make(map[*reflection.Schema][]reflect.Type),
	}
	return derivation.derive(typeOf)
}

func (d *derivation) derive(typeOf reflect.Type) (*reflection.Schema, error) {
	root := d.reflect(typeOf)
	path, err := wireShapePath(root, d.definitions)
	if err != nil {
		return nil, err
	}
	if len(path) == 0 {
		return nil, errors.New("root model has no wire shape")
	}
	for definition, sources := range d.constraintSources {
		if _, err := referencedShape(definition, d.definitions); err != nil {
			return nil, fmt.Errorf("types %v: %w", sources, err)
		}
	}
	if err := d.applyConstraints(); err != nil {
		return nil, err
	}
	return d.rootSchema(path), nil
}

func (d *derivation) rootSchema(path []*reflection.Schema) *reflection.Schema {
	var refinements []*reflection.Schema
	for _, shape := range path[:len(path)-1] {
		siblings := *shape
		if shape.Ref != "" {
			siblings.Ref = ""
		} else {
			siblings.AllOf = nil
			if len(shape.AllOf) > 1 {
				siblings.AllOf = slices.Clone(shape.AllOf[1:])
			}
		}
		if !reflect.ValueOf(siblings).IsZero() {
			refinements = append(refinements, &siblings)
		}
	}
	// Tool and provider boundaries require the complete root wire shape.
	// Keep every intermediate owner's refinement without mutating its
	// definition or expanding recursive references in child properties.
	root := *path[len(path)-1]
	root.AllOf = append(slices.Clone(root.AllOf), refinements...)
	root.Version = reflection.Version
	root.Definitions = d.definitions
	return &root
}

func (d *derivation) reflect(typeOf reflect.Type) *reflection.Schema {
	rootType := typeOf
	for rootType.Kind() == reflect.Pointer {
		rootType = rootType.Elem()
	}
	var owned *reflection.Schema
	reflector := reflection.Reflector{
		Anonymous: true,
		Namer:     d.typeName,
		Mapper: func(candidate reflect.Type) *reflection.Schema {
			shape := d.mapType(candidate)
			if candidate == rootType {
				owned = shape
			}
			return shape
		},
	}
	shape := d.collect(reflector.ReflectFromType(typeOf))
	// ReflectFromType copies Mapper's result. Deferred refinements must still
	// reach the owned root; only final publication may copy that representation.
	if owned != nil {
		return owned
	}
	return shape
}

func (d *derivation) collect(shape *reflection.Schema) *reflection.Schema {
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
	return qualifiedTypeName(typeOf)
}

func (d *derivation) mapType(typeOf reflect.Type) *reflection.Schema {
	requireValueReceiver(typeOf, reflect.TypeFor[Modeler](), "JSONSchemaModel")
	requireValueReceiver(typeOf, reflect.TypeFor[Enumerator](), "JSONSchemaEnum")
	requireValueReceiver(typeOf, reflect.TypeFor[Patterned](), "JSONSchemaPattern")
	requireValueReceiver(typeOf, reflect.TypeFor[Constrained](), "JSONSchemaConstraints")
	if typeOf.Implements(reflect.TypeFor[Modeler]()) {
		return d.model(typeOf)
	}
	if typeOf == reflect.TypeFor[url.URL]() {
		// URL has no JSON codec. Userinfo has no exported fields, so JSON v2
		// can encode User only as null; the remaining fields retain their owner.
		fields := slices.Collect(typeOf.Fields())
		for index := range fields {
			switch {
			case fields[index].Type == reflect.TypeFor[*url.Userinfo]():
				fields[index].Type = reflect.TypeFor[any]()
				fields[index].Tag += reflect.StructTag(` jsonschema:"type=` + schemaTypeNull + `"`)
			case fields[index].Type.Kind() == reflect.Pointer:
				fields[index].Tag += ` jsonschema:"nullable"`
			}
		}
		return d.reflect(reflect.StructOf(fields))
	}
	if shape := reflectWireType(typeOf); shape != nil {
		return shape
	}
	if typeOf.Kind() == reflect.Struct && typeOf != reflect.TypeFor[time.Time]() {
		return d.object(typeOf)
	}
	return nil
}

func (d *derivation) model(typeOf reflect.Type) *reflection.Schema {
	return d.project(typeOf, func() *reflection.Schema {
		model := reflect.Zero(typeOf).Interface().(Modeler).JSONSchemaModel()
		if lo.IsNil(model) {
			panic(fmt.Sprintf("%v.JSONSchemaModel returned nil", typeOf))
		}
		return d.reflect(reflect.TypeOf(model))
	})
}

func (d *derivation) object(typeOf reflect.Type) *reflection.Schema {
	return d.project(typeOf, func() *reflection.Schema {
		projection, err := projectStructFields(typeOf)
		if err != nil {
			panic(err)
		}
		reflector := reflection.Reflector{
			Anonymous: true,
			Namer:     d.typeName,
			Mapper: func(candidate reflect.Type) *reflection.Schema {
				if candidate == projection {
					return nil
				}
				return d.mapType(candidate)
			},
		}
		return d.collect(reflector.ReflectFromType(projection))
	})
}

func (d *derivation) project(typeOf reflect.Type, derive func() *reflection.Schema) *reflection.Schema {
	definition, exists := d.projections[typeOf]
	name := qualifiedTypeName(typeOf)
	shared := name != "" || typeOf.Implements(reflect.TypeFor[Modeler]())
	if !shared || !exists {
		definition = &reflection.Schema{}
		d.constraintSources[definition] = []reflect.Type{typeOf}
		if shared {
			d.projections[typeOf] = definition
		}
		if name != "" {
			d.definitions[name] = definition
		}
		wire := derive()
		*definition = *wire
		d.constraintSources[definition] = append(d.constraintSources[definition], d.constraintSources[wire]...)
	}
	if name != "" {
		return &reflection.Schema{Ref: definitionPrefix + name}
	}
	if shared {
		return &reflection.Schema{AllOf: []*reflection.Schema{definition}}
	}
	// Anonymous objects have no reference boundary. Each occurrence must own
	// the schema that the reflector will refine with that field's tags.
	return definition
}

func (d *derivation) applyConstraints() error {
	projections := make(map[*reflection.Schema][]*reflection.Schema)
	for definition, sources := range d.constraintSources {
		for _, source := range sources {
			if err := d.projectConstraints(source, definition, projections); err != nil {
				return err
			}
		}
	}
	// Shape lookup observes only wire definitions, regardless of map order.
	// Refinements become intersections after every property path is validated.
	for definition, refinements := range projections {
		definition.AllOf = append(definition.AllOf, refinements...)
	}
	return nil
}

func (d *derivation) projectConstraints(owner reflect.Type, definition *reflection.Schema, projections map[*reflection.Schema][]*reflection.Schema) error {
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
	if owner.Implements(reflect.TypeFor[Modeler]()) {
		if stringSchema := stringConstraint(value); stringSchema != nil {
			if projection == nil {
				projection = &reflection.Schema{}
			}
			projection.AllOf = append(projection.AllOf, stringSchema)
		}
	}
	if projection != nil {
		projections[definition] = append(projections[definition], projection)
	}
	return nil
}

func referencedShape(shape *reflection.Schema, definitions reflection.Definitions) (*reflection.Schema, error) {
	path, err := wireShapePath(shape, definitions)
	if err != nil || len(path) == 0 {
		return nil, err
	}
	return path[len(path)-1], nil
}

// A lone allOf wraps an unnamed Modeler. Capture that path before owned
// refinements are appended, so root publication cannot mistake a constraint
// intersection for the model's wire shape.
func wireShapePath(shape *reflection.Schema, definitions reflection.Definitions) ([]*reflection.Schema, error) {
	var path []*reflection.Schema
	visited := make(map[*reflection.Schema]struct{})
	for shape != nil {
		if _, repeated := visited[shape]; repeated {
			return nil, errors.New("model references a cycle without a wire shape")
		}
		visited[shape] = struct{}{}
		path = append(path, shape)
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
		return path, nil
	}
	return path, nil
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
	switch typeOf {
	case reflect.TypeFor[json.RawMessage](), reflect.TypeFor[time.Time]():
		return nil
	case reflect.TypeFor[net.IP]():
		return &reflection.Schema{Type: schemaTypeString, OneOf: []*reflection.Schema{
			{Format: ipv4Format},
			{Format: ipv6Format},
			{Enum: []any{""}},
		}}
	case reflect.TypeFor[time.Duration]():
		panic("time.Duration has no default encoding/json/v2 representation; use an explicit wire model and codec")
	case reflect.TypeFor[json.Number]():
		return &reflection.Schema{Type: schemaTypeNumber}
	case reflect.TypeFor[Schema]():
		return &reflection.Schema{OneOf: []*reflection.Schema{
			{Type: schemaTypeBoolean},
			{Type: schemaTypeObject},
		}}
	}
	stringModel := typeOf.Kind() == reflect.String && (typeOf.Implements(reflect.TypeFor[Enumerator]()) || typeOf.Implements(reflect.TypeFor[Patterned]()))
	if !stringModel && hasCustomEncoding(typeOf) {
		panic(fmt.Sprintf("%v: custom encoding requires JSONSchemaModel", typeOf))
	}
	if typeOf.Kind() == reflect.Array && typeOf.Elem() == reflect.TypeFor[byte]() {
		return &reflection.Schema{Type: schemaTypeString, ContentEncoding: base64ContentEncoding}
	}
	if typeOf.Kind() == reflect.Slice && typeOf.Elem() == reflect.TypeFor[byte]() {
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
