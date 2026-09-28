package openai

import (
	"fmt"

	"github.com/Tangerg/scope/core/metadata"
)

// RequestFields contains provider-owned top-level JSON fields that are not
// represented by Core. Fields owned by Core are rejected instead of allowing
// an extension to silently override the neutral request.
type RequestFields map[string]any

func coreOwnedChatField(name string) bool {
	switch name {
	case "model", "messages", "tools", "frequency_penalty", "max_tokens", "max_completion_tokens", "parallel_tool_calls", "presence_penalty", "reasoning_effort", "response_format", "stop", "stream", "temperature", "tool_choice", "top_p":
		return true
	default:
		return false
	}
}

func decodeRequestFields(values metadata.Extensions, key string, reserved ...string) (RequestFields, error) {
	fields, _, err := values.Decode[RequestFields](key)
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
