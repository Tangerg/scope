package managed

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"strings"

	sdk "github.com/Tangerg/go-sdk/mcp"

	"github.com/Tangerg/scope/agent/run"
)

// Project only the flat primitive form vocabulary. Never silently erase a
// schema constraint or turn a structured question into a guessed prompt.
func form(q run.Wait) (*sdk.ElicitParams, error) {
	var message string
	if jsonv2.Unmarshal(q.Question.JSON(), &message) != nil || strings.TrimSpace(message) == "" {
		return nil, errForm
	}
	var root map[string]json.RawMessage
	if jsonv2.Unmarshal(q.AnswerSchema.JSON(), &root) != nil {
		return nil, errForm
	}
	var kind string
	if jsonv2.Unmarshal(root["type"], &kind) != nil || kind != "object" {
		return nil, errForm
	}
	for k := range root {
		switch k {
		case "type", "properties", "required", "title", "description", "additionalProperties", "$schema":
		default:
			return nil, errForm
		}
	}
	if v, ok := root["additionalProperties"]; ok && string(v) != "false" {
		return nil, errForm
	}
	var properties map[string]map[string]json.RawMessage
	if jsonv2.Unmarshal(root["properties"], &properties) != nil || len(properties) == 0 {
		return nil, errForm
	}
	for _, field := range properties {
		if jsonv2.Unmarshal(field["type"], &kind) != nil {
			return nil, errForm
		}
		switch kind {
		case "string", "boolean":
		default:
			return nil, errForm
		}
		for k := range field {
			switch k {
			case "type", "title", "description":
			case "default":
				var v any
				if jsonv2.Unmarshal(field[k], &v) != nil {
					return nil, errForm
				}
				switch v.(type) {
				case string:
					if kind != "string" {
						return nil, errForm
					}
				case bool:
					if kind != "boolean" {
						return nil, errForm
					}
				default:
					return nil, errForm
				}
			case "enum":
				var options []string
				if kind != "string" || jsonv2.Unmarshal(field[k], &options) != nil || len(options) == 0 {
					return nil, errForm
				}
			case "format":
				var format string
				if kind != "string" || jsonv2.Unmarshal(field[k], &format) != nil {
					return nil, errForm
				}
				switch format {
				case "date", "date-time", "email", "uri":
				default:
					return nil, errForm
				}
			case "maxLength", "minLength":
				if kind != "string" {
					return nil, errForm
				}
			default:
				return nil, errForm
			}
		}
	}
	return &sdk.ElicitParams{Mode: formMode, Message: message, RequestedSchema: q.AnswerSchema.JSON()}, nil
}
