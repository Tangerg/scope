package mcp

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"

	"github.com/Tangerg/scope/core/jsonschema"
	"github.com/Tangerg/scope/mcp/internal/elicitation"
)

const (
	answerActionProperty  = "action"
	answerContentProperty = "content"
)

type elicitationAnswerSchema struct {
	OneOf []elicitationAnswerBranch `json:"oneOf"`
}

type elicitationAnswerBranch struct {
	Type                 string                      `json:"type"`
	Properties           elicitationAnswerProperties `json:"properties"`
	Required             []string                    `json:"required"`
	AdditionalProperties bool                        `json:"additionalProperties"`
}

type elicitationAnswerProperties struct {
	Action  elicitationActionSchema `json:"action"`
	Content json.RawMessage         `json:"content,omitzero"`
}

type elicitationActionSchema struct {
	Const string   `json:"const,omitzero"`
	Enum  []string `json:"enum,omitzero"`
}

// The form schema remains intact inside the affirmative branch; decline and
// cancel cannot carry content that could be mistaken for an answer.
func elicitationResponseSchema(content json.RawMessage) (jsonschema.Schema, error) {
	schema := elicitationAnswerSchema{OneOf: []elicitationAnswerBranch{
		{
			Type: elicitation.ObjectSchemaType,
			Properties: elicitationAnswerProperties{
				Action:  elicitationActionSchema{Const: elicitation.Accept},
				Content: content,
			},
			Required: []string{answerActionProperty, answerContentProperty},
		},
		{
			Type:       elicitation.ObjectSchemaType,
			Properties: elicitationAnswerProperties{Action: elicitationActionSchema{Enum: []string{elicitation.Decline, elicitation.Cancel}}},
			Required:   []string{answerActionProperty},
		},
	}}
	encoded, err := jsonv2.Marshal(schema)
	if err != nil {
		return jsonschema.Schema{}, err
	}
	return jsonschema.Parse(encoded)
}
