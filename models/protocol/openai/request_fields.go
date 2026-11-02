package openai

import (
	"errors"
	"fmt"
	"strings"

	"github.com/Tangerg/scope/core/metadata"
)

// RequestFields contains provider-owned top-level JSON fields that are not
// represented by Core. Fields owned by Core are rejected instead of allowing
// an extension to silently override the neutral request.
// Numbers decoded from extensions remain json.Number until a typed/native
// projection chooses their representation.
type RequestFields map[string]any

func validateChatExtraField(name string) error {
	if name == "" || strings.TrimSpace(name) != name {
		return errors.New("openai: extra field name is required and must not have surrounding whitespace")
	}
	switch name {
	case "model", "messages", "tools", "functions", "function_call", "frequency_penalty", "max_tokens", "max_completion_tokens", "parallel_tool_calls", "presence_penalty", "reasoning_effort", "response_format", "stop", "stream", "temperature", "tool_choice", "top_p":
		return fmt.Errorf("openai: extra field %q is owned by Core", name)
	case "n":
		return fmt.Errorf("openai: extra field %q is unsupported; Core Chat produces one output", name)
	default:
		return nil
	}
}

func decodeRequestFields(values metadata.Extensions, key string, reserved ...string) (RequestFields, error) {
	encoded, _, err := values.Decode[metadata.Map](key)
	if err != nil {
		return nil, fmt.Errorf("openai: extension %q: %w", key, err)
	}
	fields, err := encoded.Values()
	if err != nil {
		return nil, fmt.Errorf("openai: extension %q: %w", key, err)
	}
	for _, name := range reserved {
		if _, exists := fields[name]; exists {
			return nil, fmt.Errorf("openai: extension %q field %q is owned by Core", key, name)
		}
	}
	return fields, nil
}
