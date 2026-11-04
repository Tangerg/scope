package managed

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
)

type formValueType string

const (
	formString  formValueType = "string"
	formBoolean formValueType = "boolean"
)

func (f formValueType) valid() bool { return f == formString || f == formBoolean }

func (f formValueType) acceptsDefault(raw json.RawMessage) bool {
	switch f {
	case formString:
		var value *string
		return jsonv2.Unmarshal(raw, &value) == nil && value != nil
	case formBoolean:
		var value *bool
		return jsonv2.Unmarshal(raw, &value) == nil && value != nil
	default:
		return false
	}
}

type formStringFormat string

const (
	formDate     formStringFormat = "date"
	formDateTime formStringFormat = "date-time"
	formEmail    formStringFormat = "email"
	formURI      formStringFormat = "uri"
)

func (f formStringFormat) valid() bool {
	switch f {
	case formDate, formDateTime, formEmail, formURI:
		return true
	default:
		return false
	}
}

// Core already validates JSON Schema values. Keeping length constraints raw
// preserves integral spellings such as 1.0 and 1e2 that Go integers cannot decode.
type formProperty struct {
	Type        formValueType   `json:"type"`
	Title       json.RawMessage `json:"title"`
	Description json.RawMessage `json:"description"`
	Default     json.RawMessage `json:"default"`
	Enum        json.RawMessage `json:"enum"`
	Format      json.RawMessage `json:"format"`
	MinLength   json.RawMessage `json:"minLength"`
	MaxLength   json.RawMessage `json:"maxLength"`
}

func (f formProperty) validate() error {
	if !f.Type.valid() || len(f.Default) != 0 && !f.Type.acceptsDefault(f.Default) {
		return errForm
	}
	if f.Type != formString && (len(f.Enum) != 0 || len(f.Format) != 0 || len(f.MinLength) != 0 || len(f.MaxLength) != 0) {
		return errForm
	}
	if len(f.Enum) != 0 {
		var options []string
		if jsonv2.Unmarshal(f.Enum, &options) != nil || len(options) == 0 {
			return errForm
		}
	}
	if len(f.Format) != 0 {
		var format formStringFormat
		if jsonv2.Unmarshal(f.Format, &format) != nil || !format.valid() {
			return errForm
		}
	}
	return nil
}
