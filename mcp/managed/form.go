package managed

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"strings"

	sdk "github.com/Tangerg/go-sdk/mcp"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/mcp/internal/elicitation"
)

// Project only the flat primitive form vocabulary. The admitted schema itself
// is forwarded intact, so projection cannot erase an answer constraint.
func form(q run.Wait) (*sdk.ElicitParams, error) {
	var message string
	if jsonv2.Unmarshal(q.Question.JSON(), &message) != nil || strings.TrimSpace(message) == "" {
		return nil, errForm
	}
	encoded := q.AnswerSchema.JSON()
	var schema formSchema
	if jsonv2.Unmarshal(encoded, &schema, jsonv2.RejectUnknownMembers(true)) != nil || schema.validate() != nil {
		return nil, errForm
	}
	return &sdk.ElicitParams{Mode: elicitation.FormMode, Message: message, RequestedSchema: encoded}, nil
}

type formSchema struct {
	Type                 string                  `json:"type"`
	Properties           map[string]formProperty `json:"properties"`
	Required             json.RawMessage         `json:"required"`
	Title                json.RawMessage         `json:"title"`
	Description          json.RawMessage         `json:"description"`
	AdditionalProperties json.RawMessage         `json:"additionalProperties"`
	Dialect              json.RawMessage         `json:"$schema"`
}

func (f formSchema) validate() error {
	if f.Type != elicitation.ObjectSchemaType || len(f.Properties) == 0 {
		return errForm
	}
	if len(f.AdditionalProperties) != 0 {
		var allowed *bool
		if jsonv2.Unmarshal(f.AdditionalProperties, &allowed) != nil || allowed == nil || *allowed {
			return errForm
		}
	}
	for _, property := range f.Properties {
		if err := property.validate(); err != nil {
			return err
		}
	}
	return nil
}
