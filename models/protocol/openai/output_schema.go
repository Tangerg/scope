package openai

import (
	"bytes"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"math/big"
	"slices"
	"strings"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/metadata"
)

type outputSchemaKeyword string

const (
	outputSchemaVersion              outputSchemaKeyword = "$schema"
	outputSchemaRef                  outputSchemaKeyword = "$ref"
	outputSchemaDefinitions          outputSchemaKeyword = "$defs"
	outputSchemaLegacyDefinitions    outputSchemaKeyword = "definitions"
	outputSchemaType                 outputSchemaKeyword = "type"
	outputSchemaTitle                outputSchemaKeyword = "title"
	outputSchemaDescription          outputSchemaKeyword = "description"
	outputSchemaProperties           outputSchemaKeyword = "properties"
	outputSchemaRequired             outputSchemaKeyword = "required"
	outputSchemaAdditionalProperties outputSchemaKeyword = "additionalProperties"
	outputSchemaItems                outputSchemaKeyword = "items"
	outputSchemaAnyOf                outputSchemaKeyword = "anyOf"
	outputSchemaOneOf                outputSchemaKeyword = "oneOf"
	outputSchemaAllOf                outputSchemaKeyword = "allOf"
	outputSchemaEnum                 outputSchemaKeyword = "enum"
	outputSchemaConst                outputSchemaKeyword = "const"
	outputSchemaMinimum              outputSchemaKeyword = "minimum"
	outputSchemaMaximum              outputSchemaKeyword = "maximum"
	outputSchemaExclusiveMinimum     outputSchemaKeyword = "exclusiveMinimum"
	outputSchemaExclusiveMaximum     outputSchemaKeyword = "exclusiveMaximum"
	outputSchemaMultipleOf           outputSchemaKeyword = "multipleOf"
	outputSchemaMinItems             outputSchemaKeyword = "minItems"
	outputSchemaMaxItems             outputSchemaKeyword = "maxItems"
	outputSchemaPattern              outputSchemaKeyword = "pattern"
	outputSchemaFormat               outputSchemaKeyword = "format"
)

const (
	outputTypeObject  = "object"
	outputTypeInteger = "integer"
	outputTypeNumber  = "number"
)

// Raw tokens preserve machine-integer bounds while this boundary lowers only
// intersections whose accepted values are unchanged. Unsupported contracts stay
// with the caller instead of becoming a weaker strict:true promise on the wire.
type outputSchema map[outputSchemaKeyword]json.RawMessage

func nativeOutputSchema(format *corechat.OutputFormat) (map[string]any, error) {
	schema, err := format.SchemaAs[outputSchema]()
	if err != nil {
		return nil, err
	}
	if normalizeErr := schema.normalize(); normalizeErr != nil {
		return nil, fmt.Errorf("%w: openai output schema: %w", corechat.ErrUnsupportedOutputFormat, normalizeErr)
	}
	if validateErr := schema.validate(true); validateErr != nil {
		return nil, fmt.Errorf("%w: openai output schema: %w", corechat.ErrUnsupportedOutputFormat, validateErr)
	}
	data, err := jsonv2.Marshal(schema)
	if err != nil {
		return nil, err
	}
	var fields metadata.Map
	if err := jsonv2.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	return nativeSchemaFields(fields), nil
}

func (o outputSchema) normalize() error {
	if raw, found := o[outputSchemaAllOf]; found {
		var intersections []outputSchema
		if err := jsonv2.Unmarshal(raw, &intersections); err != nil {
			return fmt.Errorf("allOf: %w", err)
		}
		delete(o, outputSchemaAllOf)
		for _, intersection := range intersections {
			if err := intersection.normalize(); err != nil {
				return err
			}
			if err := o.intersect(intersection); err != nil {
				return err
			}
		}
	}
	if err := o.visitChildren(func(child outputSchema) error { return child.normalize() }); err != nil {
		return err
	}
	if raw, found := o[outputSchemaOneOf]; found {
		var children []outputSchema
		if err := jsonv2.Unmarshal(raw, &children); err != nil {
			return err
		}
		if _, exists := o[outputSchemaAnyOf]; exists || !disjointOutputTypes(children) {
			return fmt.Errorf("oneOf cannot be represented losslessly as anyOf")
		}
		delete(o, outputSchemaOneOf)
		o[outputSchemaAnyOf] = raw
	}
	return nil
}

func (o outputSchema) visitChildren(visit func(outputSchema) error) error {
	for key, raw := range o {
		switch key {
		case outputSchemaProperties, outputSchemaDefinitions, outputSchemaLegacyDefinitions:
			var children map[string]outputSchema
			if err := jsonv2.Unmarshal(raw, &children); err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			for name, child := range children {
				if err := visit(child); err != nil {
					return fmt.Errorf("%s.%s: %w", key, name, err)
				}
			}
			if err := o.set(key, children); err != nil {
				return err
			}
		case outputSchemaItems:
			var child outputSchema
			if err := jsonv2.Unmarshal(raw, &child); err != nil {
				return fmt.Errorf("items: %w", err)
			}
			if err := visit(child); err != nil {
				return fmt.Errorf("items: %w", err)
			}
			if err := o.set(key, child); err != nil {
				return err
			}
		case outputSchemaAnyOf, outputSchemaOneOf:
			var children []outputSchema
			if err := jsonv2.Unmarshal(raw, &children); err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			for index, child := range children {
				if err := visit(child); err != nil {
					return fmt.Errorf("%s[%d]: %w", key, index, err)
				}
			}
			if err := o.set(key, children); err != nil {
				return err
			}
		}
	}
	return nil
}

func (o outputSchema) set(key outputSchemaKeyword, value any) error {
	raw, err := jsonv2.Marshal(value)
	if err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	o[key] = raw
	return nil
}

func (o outputSchema) intersect(other outputSchema) error {
	if _, closed := other[outputSchemaAdditionalProperties]; closed && o.hasConstraints() {
		return fmt.Errorf("allOf with an independently closed object is unsupported")
	}
	closed := falseJSON(o[outputSchemaAdditionalProperties])
	for key, right := range other {
		left, collision := o[key]
		if !collision {
			if key == outputSchemaProperties && closed {
				return fmt.Errorf("allOf cannot add properties to a closed object")
			}
			o[key] = right
			continue
		}
		if bytes.Equal(bytes.TrimSpace(left), bytes.TrimSpace(right)) {
			continue
		}
		if err := o.intersectKeyword(key, left, right); err != nil {
			return err
		}
	}
	return nil
}

// intersectKeyword merges two different values of one keyword: annotations
// keep the first, bounds keep the tighter, required lists unite and property
// maps intersect recursively.
func (o outputSchema) intersectKeyword(key outputSchemaKeyword, left, right json.RawMessage) error {
	switch key {
	case outputSchemaTitle, outputSchemaDescription:
		return nil
	case outputSchemaMinimum, outputSchemaExclusiveMinimum, outputSchemaMinItems:
		return o.tighten(key, left, right, 1)
	case outputSchemaMaximum, outputSchemaExclusiveMaximum, outputSchemaMaxItems:
		return o.tighten(key, left, right, -1)
	case outputSchemaRequired:
		var required, extra []string
		if err := jsonv2.Unmarshal(left, &required); err != nil {
			return err
		}
		if err := jsonv2.Unmarshal(right, &extra); err != nil {
			return err
		}
		for _, name := range extra {
			if !slices.Contains(required, name) {
				required = append(required, name)
			}
		}
		return o.set(key, required)
	case outputSchemaProperties:
		return o.intersectProperties(left, right)
	default:
		return fmt.Errorf("allOf has unsupported overlapping %s constraints", key)
	}
}

// tighten keeps right when it is the tighter bound: larger for a lower bound
// (tighter = 1) and smaller for an upper bound (tighter = -1).
func (o outputSchema) tighten(key outputSchemaKeyword, left, right json.RawMessage, tighter int) error {
	leftNumber, leftOK := new(big.Rat).SetString(string(left))
	rightNumber, rightOK := new(big.Rat).SetString(string(right))
	if !leftOK || !rightOK {
		return fmt.Errorf("allOf.%s requires numeric bounds", key)
	}
	if rightNumber.Cmp(leftNumber) == tighter {
		o[key] = right
	}
	return nil
}

func (o outputSchema) hasConstraints() bool {
	for key := range o {
		switch key {
		case outputSchemaVersion, outputSchemaTitle, outputSchemaDescription, outputSchemaDefinitions, outputSchemaLegacyDefinitions:
		default:
			return true
		}
	}
	return false
}

func (o outputSchema) intersectProperties(left, right json.RawMessage) error {
	var properties, refinements map[string]outputSchema
	if err := jsonv2.Unmarshal(left, &properties); err != nil {
		return err
	}
	if err := jsonv2.Unmarshal(right, &refinements); err != nil {
		return err
	}
	for name, refinement := range refinements {
		property, exists := properties[name]
		if !exists {
			return fmt.Errorf("allOf refines undeclared property %q", name)
		}
		if err := property.normalize(); err != nil {
			return err
		}
		if err := property.intersect(refinement); err != nil {
			return fmt.Errorf("properties.%s: %w", name, err)
		}
	}
	return o.set(outputSchemaProperties, properties)
}

func (o outputSchema) validate(root bool) error {
	types, err := o.types()
	if err != nil {
		return err
	}
	if root && (!slices.Equal(types, []string{outputTypeObject}) || o[outputSchemaAnyOf] != nil) {
		return fmt.Errorf("root must be an object without anyOf")
	}
	if slices.Contains(types, outputTypeObject) {
		if err := o.validateObject(); err != nil {
			return err
		}
	}
	if len(types) == 0 && o[outputSchemaRef] == nil && o[outputSchemaAnyOf] == nil {
		return fmt.Errorf("schema requires a type, $ref or anyOf")
	}
	for key, raw := range o {
		switch key {
		case outputSchemaProperties, outputSchemaDefinitions, outputSchemaLegacyDefinitions, outputSchemaItems, outputSchemaAnyOf:
		case outputSchemaRef:
			if len(o) != 1 {
				return fmt.Errorf("$ref with sibling keywords is unsupported; supply the explicit intersected schema")
			}
			var reference string
			if err := jsonv2.Unmarshal(raw, &reference); err != nil {
				return err
			}
			if !strings.HasPrefix(reference, "#") {
				return fmt.Errorf("external $ref is unsupported")
			}
		case outputSchemaFormat:
			if err := validateOutputSchemaFormat(raw); err != nil {
				return err
			}
		case outputSchemaVersion, outputSchemaType, outputSchemaTitle, outputSchemaDescription,
			outputSchemaRequired, outputSchemaAdditionalProperties, outputSchemaEnum, outputSchemaConst,
			outputSchemaMinimum, outputSchemaMaximum, outputSchemaExclusiveMinimum, outputSchemaExclusiveMaximum,
			outputSchemaMultipleOf, outputSchemaMinItems, outputSchemaMaxItems, outputSchemaPattern:
		default:
			return fmt.Errorf("keyword %q is unsupported", key)
		}
	}
	return o.visitChildren(func(child outputSchema) error { return child.validate(false) })
}

func (o outputSchema) validateObject() error {
	if !falseJSON(o[outputSchemaAdditionalProperties]) {
		return fmt.Errorf("objects require additionalProperties:false")
	}
	var properties map[string]json.RawMessage
	if raw := o[outputSchemaProperties]; raw != nil {
		if err := jsonv2.Unmarshal(raw, &properties); err != nil {
			return err
		}
	}
	var required []string
	if raw := o[outputSchemaRequired]; raw != nil {
		if err := jsonv2.Unmarshal(raw, &required); err != nil {
			return err
		}
	}
	if len(required) != len(properties) {
		return fmt.Errorf("every object property must be required")
	}
	for name := range properties {
		if !slices.Contains(required, name) {
			return fmt.Errorf("property %q must be required", name)
		}
	}
	return nil
}

func (o outputSchema) types() ([]string, error) {
	raw := o[outputSchemaType]
	if raw == nil {
		return nil, nil
	}
	var single string
	if jsonv2.Unmarshal(raw, &single) == nil {
		return []string{single}, nil
	}
	var types []string
	if err := jsonv2.Unmarshal(raw, &types); err != nil {
		return nil, err
	}
	return types, nil
}

func disjointOutputTypes(schemas []outputSchema) bool {
	seen := make(map[string]struct{})
	for _, schema := range schemas {
		types, err := schema.types()
		if err != nil || len(types) == 0 {
			return false
		}
		for _, name := range types {
			if name == outputTypeInteger {
				name = outputTypeNumber
			}
			if _, exists := seen[name]; exists {
				return false
			}
			seen[name] = struct{}{}
		}
	}
	return true
}

const (
	outputFormatDateTime = "date-time"
	outputFormatTime     = "time"
	outputFormatDate     = "date"
	outputFormatDuration = "duration"
	outputFormatEmail    = "email"
	outputFormatHostname = "hostname"
	outputFormatIPv4     = "ipv4"
	outputFormatIPv6     = "ipv6"
	outputFormatUUID     = "uuid"
)

func validateOutputSchemaFormat(raw json.RawMessage) error {
	var format string
	if err := jsonv2.Unmarshal(raw, &format); err != nil {
		return err
	}
	switch format {
	case outputFormatDateTime, outputFormatTime, outputFormatDate, outputFormatDuration, outputFormatEmail, outputFormatHostname, outputFormatIPv4, outputFormatIPv6, outputFormatUUID:
		return nil
	default:
		return fmt.Errorf("string format %q is unsupported", format)
	}
}
