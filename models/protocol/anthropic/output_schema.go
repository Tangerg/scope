package anthropic

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"fmt"
	"net/url"
	"regexp/syntax"
	"slices"
	"strconv"
	"strings"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/metadata"
)

const outputSchemaTypeObject = "object"

// Native admission must preserve the portable contract. The SDK's convenience
// transformer drops bounds and adds them to descriptions; doing that here would
// promise enforcement that neither the endpoint nor the streaming model owns.
type outputSchema struct {
	Version              string                   `json:"$schema,omitempty"`
	Reference            string                   `json:"$ref,omitempty"`
	Definitions          map[string]*outputSchema `json:"$defs,omitzero"`
	LegacyDefinitions    map[string]*outputSchema `json:"definitions,omitzero"`
	Type                 json.RawMessage          `json:"type,omitzero"`
	Title                string                   `json:"title,omitempty"`
	Description          string                   `json:"description,omitempty"`
	Default              json.RawMessage          `json:"default,omitzero"`
	Properties           map[string]*outputSchema `json:"properties,omitzero"`
	Required             []string                 `json:"required,omitzero"`
	AdditionalProperties *bool                    `json:"additionalProperties,omitzero"`
	Items                *outputSchema            `json:"items,omitzero"`
	AnyOf                []*outputSchema          `json:"anyOf,omitzero"`
	AllOf                []*outputSchema          `json:"allOf,omitzero"`
	Enum                 []json.RawMessage        `json:"enum,omitzero"`
	Const                json.RawMessage          `json:"const,omitzero"`
	MinItems             *uint64                  `json:"minItems,omitzero"`
	Pattern              string                   `json:"pattern,omitempty"`
	Format               string                   `json:"format,omitempty"`
}

func nativeOutputSchema(format *corechat.OutputFormat) (map[string]any, error) {
	if format.Type != corechat.OutputFormatJSONSchema {
		return nil, fmt.Errorf("%w: anthropic requires an explicit closed JSON schema", corechat.ErrUnsupportedOutputFormat)
	}
	if err := format.Validate(); err != nil {
		return nil, err
	}
	var schema outputSchema
	if err := jsonv2.Unmarshal(format.Schema, &schema, jsonv2.RejectUnknownMembers(true)); err != nil {
		return nil, fmt.Errorf("%w: anthropic output schema: %w", corechat.ErrUnsupportedOutputFormat, err)
	}
	if err := schema.validate(format.Schema, make(map[string]bool), false); err != nil {
		return nil, fmt.Errorf("%w: anthropic output schema: %w", corechat.ErrUnsupportedOutputFormat, err)
	}
	var fields metadata.Map
	if err := jsonv2.Unmarshal(format.Schema, &fields); err != nil {
		return nil, err
	}
	native := make(map[string]any, len(fields))
	for name, value := range fields {
		native[name] = value
	}
	return native, nil
}

func (o *outputSchema) validate(document json.RawMessage, references map[string]bool, intersection bool) error {
	if o == nil {
		return fmt.Errorf("boolean and null schemas are unsupported")
	}
	if err := o.validateConstraints(); err != nil {
		return err
	}
	if o.Reference != "" {
		if intersection || len(o.AllOf) != 0 {
			return fmt.Errorf("allOf with $ref is unsupported")
		}
		if references[o.Reference] {
			return fmt.Errorf("recursive $ref %q is unsupported", o.Reference)
		}
		resolved, err := resolveOutputSchemaReference(document, o.Reference)
		if err != nil {
			return err
		}
		references[o.Reference] = true
		err = resolved.validate(document, references, false)
		delete(references, o.Reference)
		if err != nil {
			return err
		}
	}
	for _, children := range []map[string]*outputSchema{o.Properties, o.Definitions, o.LegacyDefinitions} {
		for name, child := range children {
			if err := child.validate(document, references, intersection); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
		}
	}
	if o.Items != nil {
		if err := o.Items.validate(document, references, intersection); err != nil {
			return fmt.Errorf("items: %w", err)
		}
	}
	for index, child := range o.AnyOf {
		if err := child.validate(document, references, intersection); err != nil {
			return fmt.Errorf("anyOf[%d]: %w", index, err)
		}
	}
	for index, child := range o.AllOf {
		if err := child.validate(document, references, true); err != nil {
			return fmt.Errorf("allOf[%d]: %w", index, err)
		}
	}
	return nil
}

func (o *outputSchema) validateConstraints() error {
	var types []string
	if len(o.Type) != 0 {
		var single string
		if jsonv2.Unmarshal(o.Type, &single) == nil {
			types = []string{single}
		} else if err := jsonv2.Unmarshal(o.Type, &types); err != nil {
			return err
		}
	}
	if o.AdditionalProperties != nil && *o.AdditionalProperties {
		return fmt.Errorf("additionalProperties must be false")
	}
	if (slices.Contains(types, outputSchemaTypeObject) || o.Properties != nil) && o.AdditionalProperties == nil {
		return fmt.Errorf("objects require additionalProperties:false")
	}
	if o.MinItems != nil && *o.MinItems > 1 {
		return fmt.Errorf("minItems supports only 0 or 1")
	}
	for _, value := range o.Enum {
		value = bytes.TrimSpace(value)
		if len(value) != 0 && (value[0] == '{' || value[0] == '[') {
			return fmt.Errorf("enum values must be scalar")
		}
	}
	if err := validateOutputSchemaFormat(o.Format); err != nil {
		return err
	}
	if o.Pattern != "" {
		pattern, err := syntax.Parse(o.Pattern, syntax.Perl)
		if err != nil {
			return fmt.Errorf("unsupported pattern: %w", err)
		}
		if err := validateOutputSchemaPattern(pattern); err != nil {
			return err
		}
	}
	return nil
}

func resolveOutputSchemaReference(document json.RawMessage, reference string) (*outputSchema, error) {
	fragment, local := strings.CutPrefix(reference, "#")
	path, err := url.PathUnescape(fragment)
	pointer := jsontext.Pointer(path)
	if !local || err != nil || !pointer.IsValid() {
		return nil, fmt.Errorf("$ref %q must be an internal JSON pointer", reference)
	}
	current := document
	for part := range pointer.Tokens() {
		current = bytes.TrimSpace(current)
		if len(current) == 0 {
			return nil, fmt.Errorf("unresolved $ref %q", reference)
		}
		if current[0] == '[' {
			var values []json.RawMessage
			index, err := strconv.Atoi(part)
			if err != nil || index < 0 || strconv.Itoa(index) != part || jsonv2.Unmarshal(current, &values) != nil || index >= len(values) {
				return nil, fmt.Errorf("unresolved $ref %q", reference)
			}
			current = values[index]
			continue
		}
		var fields map[string]json.RawMessage
		if err := jsonv2.Unmarshal(current, &fields); err != nil {
			return nil, fmt.Errorf("$ref %q: %w", reference, err)
		}
		var exists bool
		current, exists = fields[part]
		if !exists {
			return nil, fmt.Errorf("unresolved $ref %q", reference)
		}
	}
	var resolved outputSchema
	if err := jsonv2.Unmarshal(current, &resolved); err != nil {
		return nil, fmt.Errorf("$ref %q: %w", reference, err)
	}
	return &resolved, nil
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
	outputFormatURI      = "uri"
)

func validateOutputSchemaFormat(format string) error {
	switch format {
	case "", outputFormatDateTime, outputFormatTime, outputFormatDate, outputFormatDuration, outputFormatEmail, outputFormatHostname, outputFormatURI, outputFormatIPv4, outputFormatIPv6, outputFormatUUID:
		return nil
	default:
		return fmt.Errorf("string format %q is unsupported", format)
	}
}

func validateOutputSchemaPattern(pattern *syntax.Regexp) error {
	if pattern.Op == syntax.OpWordBoundary || pattern.Op == syntax.OpNoWordBoundary {
		return fmt.Errorf("pattern word boundaries are unsupported")
	}
	for _, child := range pattern.Sub {
		if err := validateOutputSchemaPattern(child); err != nil {
			return err
		}
	}
	return nil
}
