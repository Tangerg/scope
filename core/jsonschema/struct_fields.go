package jsonschema

import (
	"cmp"
	"encoding"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

const (
	jsonFieldTag                  = "json"
	jsonSchemaFieldTag            = "jsonschema"
	stringTagOption               = "string"
	formatTagOption               = "format"
	ignoredFieldTag               = "-"
	embedTagOption                = "embed"
	omitEmptyTagOption            = "omitempty"
	omitZeroTagOption             = "omitzero"
	jsonSchemaDescriptionFieldTag = "jsonschema_description"
	jsonSchemaExtrasFieldTag      = "jsonschema_extras"
)

type wireField struct {
	field       reflect.StructField
	name        string
	index       []int
	explicit    bool
	embedded    bool
	optional    bool
	hidden      bool
	stringified bool
}

func parseWireField(field reflect.StructField, index []int, optional, hidden bool) (wireField, error) {
	tags := strings.Split(field.Tag.Get(jsonFieldTag), ",")
	for _, option := range tags[1:] {
		if name, _, _ := strings.Cut(option, ":"); name == formatTagOption {
			return wireField{}, ErrUnsupportedFormatTag
		}
	}
	name := tags[0]
	member := wireField{
		field: field, name: name, index: index, explicit: name != "",
		optional: optional, hidden: hidden || field.Tag.Get(jsonSchemaFieldTag) == ignoredFieldTag,
	}
	if member.name == "" {
		member.name = field.Name
	}
	member.embedded = field.Anonymous && !member.explicit
	for _, option := range tags[1:] {
		switch option {
		case embedTagOption:
			member.embedded = true
		case omitEmptyTagOption, omitZeroTagOption:
			member.optional = true
		case stringTagOption:
			member.stringified = true
		}
	}
	if err := member.validateTag(tags); err != nil {
		return wireField{}, err
	}
	if name == ignoredFieldTag {
		return wireField{}, errors.New("a literal hyphen member name requires an explicit schema")
	}
	return member, nil
}

func (w wireField) validateTag(tags []string) error {
	if w.embedded && !slices.Contains(tags[1:], embedTagOption) {
		tags = append(tags, embedTagOption)
	}
	fieldType := w.field.Type
	if w.embedded {
		child := fieldType
		if child.Kind() == reflect.Pointer && child.Name() == "" {
			child = child.Elem()
		}
		if child.Kind() == reflect.Struct && !hasCustomEncoding(child) && !hasCustomDecoding(child) {
			// Nested fields enter this same admission boundary when visited.
			fieldType = reflect.TypeFor[struct{}]()
		}
	}
	// The codec owns tag syntax. An empty object checks its field metadata
	// without invoking user codecs, validators, or IsZero methods. A named
	// synthetic field prevents method promotion onto this inspection type.
	inspection := reflect.StructOf([]reflect.StructField{{
		Name: "Field", Type: fieldType,
		Tag: reflect.StructTag(jsonFieldTag + ":" + strconv.Quote(strings.Join(tags, ","))),
	}})
	if err := jsonv2.Unmarshal([]byte(`{}`), reflect.New(inspection).Interface()); err != nil {
		return err
	}
	member := w.field.Type
	for member.Kind() == reflect.Pointer {
		member = member.Elem()
	}
	if !w.field.IsExported() && !w.embedded &&
		(hasCustomEncoding(member) || hasCustomDecoding(member) ||
			(slices.Contains(tags[1:], omitZeroTagOption) &&
				(member.Implements(reflect.TypeFor[interface{ IsZero() bool }]()) || reflect.PointerTo(member).Implements(reflect.TypeFor[interface{ IsZero() bool }]())))) {
		return errors.New("unexported embedded field cannot expose codec or IsZero methods")
	}
	if !w.hidden && w.stringified && (!member.Implements(reflect.TypeFor[Modeler]()) || !hasCustomEncoding(member)) {
		return ErrUnsupportedStringTag
	}
	return nil
}

func (w wireField) projection(position int) reflect.StructField {
	jsonTag := w.name
	if w.optional {
		jsonTag += "," + omitZeroTagOption
	}
	if w.stringified {
		jsonTag += "," + stringTagOption
	}
	var tag strings.Builder
	tag.WriteString(jsonFieldTag + ":" + strconv.Quote(jsonTag))
	for _, key := range []string{jsonSchemaFieldTag, jsonSchemaDescriptionFieldTag, jsonSchemaExtrasFieldTag} {
		value, present := w.field.Tag.Lookup(key)
		if key == jsonSchemaFieldTag && w.hidden {
			value, present = ignoredFieldTag, true
		}
		if present {
			tag.WriteString(" " + key + ":" + strconv.Quote(value))
		}
	}
	return reflect.StructField{
		Name: "Field" + strconv.Itoa(position), Type: w.field.Type,
		Tag: reflect.StructTag(tag.String()),
	}
}

// The dependency's embedding rules predate JSON v2. Resolve the codec's field
// precedence once before applying its schema-tag dialect, so hidden fields
// cannot change a contract merely by moving their declaration.
func projectStructFields(typeOf reflect.Type) (reflect.Type, error) {
	fields, err := collectWireFields(typeOf)
	if err != nil {
		return nil, err
	}
	slices.SortStableFunc(fields, func(left, right wireField) int {
		named := 0
		if left.explicit != right.explicit {
			named = 1
			if left.explicit {
				named = -1
			}
		}
		return cmp.Or(strings.Compare(left.name, right.name), cmp.Compare(len(left.index), len(right.index)), named)
	})
	selected := fields[:0]
	for len(fields) != 0 {
		end := 1
		for end < len(fields) && fields[end].name == fields[0].name {
			end++
		}
		if end == 1 || len(fields[0].index) != len(fields[1].index) || fields[0].explicit != fields[1].explicit {
			selected = append(selected, fields[0])
		}
		fields = fields[end:]
	}
	slices.SortFunc(selected, func(left, right wireField) int { return slices.Compare(left.index, right.index) })
	projection := make([]reflect.StructField, len(selected))
	for index, field := range selected {
		projection[index] = field.projection(index)
	}
	return reflect.StructOf(projection), nil
}

type wireFieldVisit struct {
	typeOf   reflect.Type
	index    []int
	children bool
	optional bool
	hidden   bool
}

func collectWireFields(typeOf reflect.Type) ([]wireField, error) {
	queue := []wireFieldVisit{{typeOf: typeOf, children: true}}
	seen := map[reflect.Type]bool{typeOf: true}
	var fields []wireField
	// JSON v2 visits direct fields at every occurrence of an embedded type,
	// but descends through that type only at its first breadth-first occurrence.
	for position := 0; position < len(queue); position++ {
		visit := queue[position]
		names := make(map[string]struct{})
		hasTag, hasField := false, false
		for field := range visit.typeOf.Fields() {
			tag, tagged := field.Tag.Lookup(jsonFieldTag)
			hasTag = hasTag || tagged
			if tag == ignoredFieldTag {
				continue
			}
			if !field.IsExported() && !field.Anonymous {
				if tagged {
					return nil, fmt.Errorf("%w: unexported field %v.%s has a JSON tag", ErrUnsupportedFieldLayout, visit.typeOf, field.Name)
				}
				continue
			}
			hasField = true
			index := append(slices.Clone(visit.index), field.Index...)
			member, err := parseWireField(field, index, visit.optional, visit.hidden)
			if err != nil {
				return nil, fmt.Errorf("%w: %v.%s: %w", ErrUnsupportedFieldLayout, visit.typeOf, field.Name, err)
			}
			if member.embedded {
				child := field.Type
				if child.Kind() == reflect.Pointer && child.Name() == "" {
					child = child.Elem()
					member.optional = true
				}
				if child.Kind() != reflect.Struct || hasCustomEncoding(child) || hasCustomDecoding(child) {
					return nil, fmt.Errorf("%w: %v.%s must embed an ordinary struct", ErrUnsupportedFieldLayout, visit.typeOf, field.Name)
				}
				if ownsSchema(child) {
					return nil, fmt.Errorf("%w: %v.%s embeds a schema owner; use a named JSON field or an explicit enclosing wire model", ErrUnsupportedFieldLayout, visit.typeOf, field.Name)
				}
				if visit.children {
					queue = append(queue, wireFieldVisit{typeOf: child, index: index, children: !seen[child], optional: member.optional, hidden: member.hidden})
				}
				seen[child] = true
				continue
			}
			if _, duplicate := names[member.name]; duplicate {
				return nil, fmt.Errorf("%w: %v has duplicate JSON member %q", ErrUnsupportedFieldLayout, visit.typeOf, member.name)
			}
			names[member.name] = struct{}{}
			fields = append(fields, member)
		}
		if visit.typeOf.NumField() != 0 && !hasTag && !hasField {
			return nil, fmt.Errorf("%w: %v has no exported fields", ErrUnsupportedFieldLayout, visit.typeOf)
		}
	}
	return fields, nil
}

func ownsSchema(typeOf reflect.Type) bool {
	for _, owner := range []reflect.Type{reflect.TypeFor[Modeler](), reflect.TypeFor[Enumerator](), reflect.TypeFor[Patterned](), reflect.TypeFor[Constrained]()} {
		if typeOf.Implements(owner) || reflect.PointerTo(typeOf).Implements(owner) {
			return true
		}
	}
	return false
}

func hasCustomDecoding(typeOf reflect.Type) bool {
	for _, codec := range []reflect.Type{reflect.TypeFor[json.Unmarshaler](), reflect.TypeFor[jsonv2.UnmarshalerFrom](), reflect.TypeFor[encoding.TextUnmarshaler]()} {
		if typeOf.Implements(codec) || reflect.PointerTo(typeOf).Implements(codec) {
			return true
		}
	}
	return false
}
